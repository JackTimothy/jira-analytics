// Package integration wires the real adapters, interactors and HTTP handlers
// together against fake Jira and GitHub servers.
//
// Every other test exercises one layer with the rest faked. This one proves the
// layers actually fit: that Jira's timestamp format survives into an interval
// boundary, that a GitHub review lands on the right sub-task, and that the
// scope filter reaches the response.
package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/adapter/configstore"
	"github.com/jacktimothy/jira-analytics/internal/adapter/github"
	"github.com/jacktimothy/jira-analytics/internal/adapter/httpapi"
	"github.com/jacktimothy/jira-analytics/internal/adapter/jira"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
	"github.com/jacktimothy/jira-analytics/internal/usecase"
)

// The sprint runs 3 Aug 2026 to 17 Aug 2026, ending 2pm Eastern.
//
// PROJ-1 is committed (due on the sprint end) and has two sub-tasks: one that
// goes all the way to merged, one that stalls awaiting review.
// PROJ-2 was pulled in without a due date, so it is out of committed scope.
// PROJ-3 carried over from an earlier sprint and keeps its original due date,
// so it must still count as committed.
var jiraRoutes = map[string]string{
	"/rest/agile/1.0/board/45/sprint": `{"isLast":true,"values":[
		{"id":7354,"name":"Sprint 26-31","state":"closed",
		 "startDate":"2026-08-03T13:00:00.000Z","endDate":"2026-08-17T18:00:00.000Z"}]}`,

	"/rest/agile/1.0/sprint/7354/issue": `{"startAt":0,"maxResults":100,"total":3,"issues":[
		{"key":"PROJ-1","fields":{"summary":"Committed story","duedate":"2026-08-17",
		 "created":"2026-07-27T14:16:31.828-0400","issuetype":{"subtask":false},
		 "status":{"id":"10009","name":"Review","statusCategory":{"key":"indeterminate"}}}},
		{"key":"PROJ-2","fields":{"summary":"Pulled in mid-sprint","duedate":null,
		 "created":"2026-08-05T09:00:00.000-0400","issuetype":{"subtask":false},
		 "status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}}}},
		{"key":"PROJ-3","fields":{"summary":"Carried over","duedate":"2026-08-03",
		 "created":"2026-07-10T09:00:00.000-0400","issuetype":{"subtask":false},
		 "status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}}}}]}`,

	"/rest/api/3/search/jql": `{"issues":[
		{"key":"PROJ-10","fields":{"summary":"Build the API","created":"2026-07-28T09:00:00.000-0400",
		 "issuetype":{"subtask":true},"parent":{"key":"PROJ-1"},
		 "status":{"id":"10024","name":"Done","statusCategory":{"key":"done"}}}},
		{"key":"PROJ-11","fields":{"summary":"Build the UI","created":"2026-07-28T09:00:00.000-0400",
		 "issuetype":{"subtask":true},"parent":{"key":"PROJ-1"},
		 "status":{"id":"10009","name":"Review","statusCategory":{"key":"indeterminate"}}}},
		{"key":"PROJ-20","fields":{"summary":"No code needed","created":"2026-08-05T09:00:00.000-0400",
		 "issuetype":{"subtask":true},"parent":{"key":"PROJ-2"},
		 "status":{"id":"10039","name":"To Do","statusCategory":{"key":"new"}}}}]}`,

	"/rest/api/3/status": `[
		{"id":"10039","name":"To Do","statusCategory":{"key":"new"}},
		{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},
		{"id":"10009","name":"Review","statusCategory":{"key":"indeterminate"}},
		{"id":"10024","name":"Done","statusCategory":{"key":"done"}}]`,

	"/rest/api/3/issue/PROJ-10/changelog": `{"isLast":true,"values":[
		{"created":"2026-08-04T09:00:00.000-0400","items":[
			{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}`,
	"/rest/api/3/issue/PROJ-11/changelog": `{"isLast":true,"values":[
		{"created":"2026-08-04T09:00:00.000-0400","items":[
			{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}`,
	"/rest/api/3/issue/PROJ-20/changelog": `{"isLast":true,"values":[]}`,
}

var githubRoutes = map[string]string{
	"/repos/acme/service":          `{"default_branch":"dev"}`,
	"/repos/acme/service/branches": `[{"name":"dev"},{"name":"PROJ-10-api"},{"name":"PROJ-11-ui"}]`,

	"/repos/acme/service/compare/dev...PROJ-10-api": `{"commits":[{"commit":{"committer":{"date":"2026-08-04T13:00:00Z"}}}]}`,
	"/repos/acme/service/compare/dev...PROJ-11-ui":  `{"commits":[{"commit":{"committer":{"date":"2026-08-05T13:00:00Z"}}}]}`,

	"/repos/acme/service/issues/1/timeline": `[
		{"event":"review_requested","created_at":"2026-08-06T13:00:00Z","requested_reviewer":{"login":"alice","type":"User"}},
		{"event":"merged","created_at":"2026-08-09T13:00:00Z"},
		{"event":"closed","created_at":"2026-08-09T13:00:00Z"}]`,
	"/repos/acme/service/pulls/1/reviews": `[
		{"user":{"login":"alice","type":"User"},"state":"APPROVED","submitted_at":"2026-08-08T13:00:00Z"},
		{"user":{"login":"scanner[bot]","type":"Bot"},"state":"APPROVED","submitted_at":"2026-08-07T13:00:00Z"}]`,

	"/repos/acme/service/issues/2/timeline": `[
		{"event":"review_requested","created_at":"2026-08-07T13:00:00Z","requested_reviewer":{"login":"bob","type":"User"}}]`,
	"/repos/acme/service/pulls/2/reviews": `[]`,
}

func fakeAPI(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The pull request listing is keyed by query string, not by path.
		if body, handled := routeByQuery(r); handled {
			io.WriteString(w, body)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"no fixture for `+r.URL.Path+`"}`)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func routeByQuery(r *http.Request) (string, bool) {
	if r.URL.Path != "/repos/acme/service/pulls" {
		return "", false
	}
	switch r.URL.Query().Get("head") {
	case "acme:PROJ-10-api":
		return `[{"number":1,"title":"PROJ-10 api","draft":false,"created_at":"2026-08-05T13:00:00Z",
			"merged_at":"2026-08-09T13:00:00Z","head":{"ref":"PROJ-10-api"}}]`, true
	case "acme:PROJ-11-ui":
		return `[{"number":2,"title":"PROJ-11 ui","draft":false,"created_at":"2026-08-06T13:00:00Z",
			"head":{"ref":"PROJ-11-ui"}}]`, true
	default:
		return `[]`, true
	}
}

func buildStack(t *testing.T) http.Handler {
	t.Helper()

	jiraServer := fakeAPI(t, jiraRoutes)
	githubServer := fakeAPI(t, githubRoutes)

	path := filepath.Join(t.TempDir(), "projects.yaml")
	contents := `
projects:
  - id: team
    name: Team
    settings: { timezone: America/New_York }
    tracker: { type: jira, projectKey: PROJ, boardId: "45" }
    repos:
      - { host: github, owner: acme, name: service }
    reviewers: { excludeBots: true }
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	projects, err := configstore.Load(path)
	if err != nil {
		t.Fatalf("loading projects: %v", err)
	}

	client := httpclient.New(http.DefaultClient,
		httpclient.WithSleep(func(context.Context, time.Duration) error { return nil }))

	tracker := jira.NewTracker(jira.Config{BaseURL: jiraServer.URL, Email: "e", APIToken: "t"}, client)
	codeHost := github.NewCodeHost(github.Config{BaseURL: githubServer.URL, Token: "t"}, client)

	return httpapi.NewServer(
		projects,
		usecase.NewSprints(projects, tracker),
		usecase.NewRetrospective(projects, tracker, codeHost),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	).Routes()
}

type retrospectiveBody struct {
	Sprint struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"sprint"`
	Parents []struct {
		Key      string  `json:"key"`
		DueDate  *string `json:"dueDate"`
		InScope  bool    `json:"inScope"`
		SubTasks []struct {
			Key       string `json:"key"`
			Intervals []struct {
				State string    `json:"state"`
				From  time.Time `json:"from"`
				To    time.Time `json:"to"`
			} `json:"intervals"`
		} `json:"subtasks"`
	} `json:"parents"`
	Warnings []string `json:"warnings"`
}

func fetch(t *testing.T, handler http.Handler, target string) retrospectiveBody {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s -> %d: %s", target, rec.Code, rec.Body)
	}
	var body retrospectiveBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v\n%s", err, rec.Body)
	}
	return body
}

func TestRetrospectiveEndToEnd(t *testing.T) {
	handler := buildStack(t)
	body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective")

	if body.Sprint.Name != "Sprint 26-31" {
		t.Fatalf("sprint = %+v", body.Sprint)
	}
	if len(body.Parents) != 3 {
		t.Fatalf("got %d parents, want 3", len(body.Parents))
	}

	scope := map[string]bool{}
	for _, parent := range body.Parents {
		scope[parent.Key] = parent.InScope
	}
	if !scope["PROJ-1"] {
		t.Error("PROJ-1 is due on the sprint end and must be in scope")
	}
	if scope["PROJ-2"] {
		t.Error("PROJ-2 has no due date and must be out of scope")
	}
	if !scope["PROJ-3"] {
		t.Error("PROJ-3 carried over with an earlier due date and must still be in scope")
	}
}

func TestRetrospectiveDerivesTheFullLifecycleFromBothSources(t *testing.T) {
	handler := buildStack(t)
	body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective")

	var api []string
	var boundaries []time.Time
	for _, parent := range body.Parents {
		for _, subTask := range parent.SubTasks {
			if subTask.Key != "PROJ-10" {
				continue
			}
			for _, interval := range subTask.Intervals {
				api = append(api, interval.State)
				boundaries = append(boundaries, interval.From)
			}
		}
	}

	want := []string{"TO_DO", "IN_PROGRESS", "REVIEW_REQUESTED", "APPROVED", "DONE"}
	if strings.Join(api, ",") != strings.Join(want, ",") {
		t.Fatalf("PROJ-10 states = %v, want %v", api, want)
	}

	// Interval boundaries must land on the actual event instants, which is what
	// proves Jira's and GitHub's timestamps both survived translation.
	if !boundaries[1].Equal(time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("In Progress starts at %s, want the branch's first commit", boundaries[1])
	}
	if !boundaries[3].Equal(time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("Approved starts at %s, want alice's approval", boundaries[3])
	}
	if !boundaries[4].Equal(time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("Done starts at %s, want the merge", boundaries[4])
	}
}

func TestRetrospectiveStallsInReviewWhenNobodyResponds(t *testing.T) {
	handler := buildStack(t)
	body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective")

	for _, parent := range body.Parents {
		for _, subTask := range parent.SubTasks {
			if subTask.Key != "PROJ-11" {
				continue
			}
			last := subTask.Intervals[len(subTask.Intervals)-1]
			if last.State != "REVIEW_REQUESTED" {
				t.Errorf("PROJ-11 ends in %s, want it still awaiting review", last.State)
			}
			if !last.To.Equal(time.Date(2026, 8, 17, 18, 0, 0, 0, time.UTC)) {
				t.Errorf("the final interval should run to the sprint end, got %s", last.To)
			}
		}
	}
}

func TestRetrospectiveCommittedScopeDropsUncommittedParents(t *testing.T) {
	handler := buildStack(t)
	body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective?scope=committed")

	if len(body.Parents) != 2 {
		t.Fatalf("got %d parents, want the two committed ones", len(body.Parents))
	}
	for _, parent := range body.Parents {
		if parent.Key == "PROJ-2" {
			t.Error("an uncommitted parent survived the committed filter")
		}
	}
}

func TestRetrospectiveWarnsAboutSubTasksWithNoCode(t *testing.T) {
	handler := buildStack(t)
	body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective")

	var found bool
	for _, warning := range body.Warnings {
		if strings.Contains(warning, "PROJ-20") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning for the sub-task with no branch, got %v", body.Warnings)
	}
}

func TestSprintListingEndToEnd(t *testing.T) {
	handler := buildStack(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects/team/sprints", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Sprint 26-31") {
		t.Errorf("body = %s", rec.Body)
	}
}
