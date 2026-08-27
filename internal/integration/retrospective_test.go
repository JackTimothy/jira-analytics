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
	"sync"
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
	"/rest/api/3/field": `[
		{"id":"customfield_10020","name":"Sprint","schema":{"custom":"com.pyxis.greenhopper.jira:gh-sprint"}},
		{"id":"customfield_10059","name":"Story Points","schema":{"custom":"com.pyxis.greenhopper.jira:gh-story-points"}}]`,

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
	"/rest/api/3/issue/PROJ-30/changelog": `{"isLast":true,"values":[]}`,

	// The retrospective asks for its one sprint directly rather than scanning
	// the project for it.
	"/rest/agile/1.0/sprint/7354": `{"id":100,"name":"Sprint 26-31","state":"closed",
		"startDate":"2026-08-03T09:00:00.000-0400","endDate":"2026-08-17T14:00:00.000-0400"}`,
}

// The three JQL queries share one endpoint, so the fake routes on the query
// itself — which also asserts, implicitly, that each one is scoped as intended.
const sprintScanResponse = `{"issues":[
	{"key":"PROJ-1","fields":{"customfield_10020":[
		{"id":7354,"name":"Sprint 26-31","state":"closed",
		 "startDate":"2026-08-03T13:00:00.000Z","endDate":"2026-08-17T18:00:00.000Z"}]}}]}`

// Issue types and estimates are here because both matter to the chart now: the
// estimate drives the burndown, and the type is what a project's typesLast
// setting sorts on.
const sprintParentsResponse = `{"issues":[
	{"key":"PROJ-1","fields":{"summary":"Committed story","duedate":"2026-08-17",
	 "created":"2026-07-27T14:16:31.828-0400","issuetype":{"subtask":false,"name":"Story"},
	 "customfield_10059":5,
	 "status":{"id":"10009","name":"Review","statusCategory":{"key":"indeterminate"}}}},
	{"key":"PROJ-2","fields":{"summary":"Pulled in mid-sprint","duedate":null,
	 "created":"2026-08-05T09:00:00.000-0400","issuetype":{"subtask":false,"name":"Support"},
	 "customfield_10059":2,
	 "status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}}}},
	{"key":"PROJ-3","fields":{"summary":"Carried over","duedate":"2026-08-03",
	 "created":"2026-07-10T09:00:00.000-0400","issuetype":{"subtask":false,"name":"Bug"},
	 "customfield_10059":3,
	 "status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}}}},
	{"key":"PROJ-4","fields":{"summary":"Committed but never broken down","duedate":"2026-08-17",
	 "created":"2026-07-10T09:00:00.000-0400","issuetype":{"subtask":false,"name":"Task"},
	 "customfield_10059":8,
	 "status":{"id":"10039","name":"To Do","statusCategory":{"key":"new"}}}}]}`

const subTasksResponse = `{"issues":[
	{"key":"PROJ-10","fields":{"summary":"Build the API","created":"2026-07-28T09:00:00.000-0400",
	 "issuetype":{"subtask":true},"parent":{"key":"PROJ-1"},
	 "status":{"id":"10024","name":"Done","statusCategory":{"key":"done"}}}},
	{"key":"PROJ-11","fields":{"summary":"Build the UI","created":"2026-07-28T09:00:00.000-0400",
	 "issuetype":{"subtask":true},"parent":{"key":"PROJ-1"},
	 "status":{"id":"10009","name":"Review","statusCategory":{"key":"indeterminate"}}}},
	{"key":"PROJ-20","fields":{"summary":"No code needed","created":"2026-08-05T09:00:00.000-0400",
	 "issuetype":{"subtask":true},"parent":{"key":"PROJ-2"},
	 "status":{"id":"10039","name":"To Do","statusCategory":{"key":"new"}}}},
	{"key":"PROJ-30","fields":{"summary":"Carryover work","created":"2026-07-20T09:00:00.000-0400",
	 "issuetype":{"subtask":true},"parent":{"key":"PROJ-3"},
	 "status":{"id":"10024","name":"Done","statusCategory":{"key":"done"}}}}]}`

// jqlOf pulls the query out of a search request for the request log.
func jqlOf(body []byte) string {
	var request struct {
		JQL string `json:"jql"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "?"
	}
	return request.JQL
}

func routeJQL(jql string) (string, bool) {
	request := struct{ JQL string }{JQL: jql}
	switch {
	case strings.Contains(request.JQL, "sprint IS NOT EMPTY"):
		return sprintScanResponse, true
	case strings.HasPrefix(request.JQL, "sprint = "):
		return sprintParentsResponse, true
	case strings.HasPrefix(request.JQL, "parent IN "):
		return subTasksResponse, true
	case strings.HasPrefix(request.JQL, "key IN "):
		return statusHistoryResponse, true
	default:
		return "", false
	}
}

// This site attaches changelogs to a search, so the whole sprint's status
// history arrives in one request. The per-issue changelog fixtures above stay
// as the fallback for a site that will not.
const statusHistoryResponse = `{"issues":[
	{"key":"PROJ-10","changelog":{"startAt":0,"maxResults":100,"total":1,"histories":[
		{"created":"2026-08-04T09:00:00.000-0400","items":[
			{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}},
	{"key":"PROJ-11","changelog":{"startAt":0,"maxResults":100,"total":1,"histories":[
		{"created":"2026-08-04T09:00:00.000-0400","items":[
			{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}},
	{"key":"PROJ-20","changelog":{"startAt":0,"maxResults":100,"total":0,"histories":[]}},
	{"key":"PROJ-30","changelog":{"startAt":0,"maxResults":100,"total":0,"histories":[]}}]}`

var githubRoutes = map[string]string{
	"/repos/acme/service": `{"default_branch":"dev"}`,

	// PROJ-10 merged and its branch was deleted; PROJ-11 is still open.
	"/repos/acme/service/git/matching-refs/heads/PROJ-": `[{"ref":"refs/heads/PROJ-11-ui"}]`,
	"/repos/acme/service/git/matching-refs/heads/proj-": `[]`,

	"/repos/acme/service/pulls/1/commits": `[{"commit":{"committer":{"date":"2026-08-04T13:00:00Z"}}}]`,
	"/repos/acme/service/pulls/2/commits": `[{"commit":{"committer":{"date":"2026-08-05T13:00:00Z"}}}]`,

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
	return fakeAPIRecording(t, routes, &requestLog{})
}

// jqlRouter answers a search by its query. Passed in rather than fixed so a
// test can present a different tracker shape — a team with no sub-tasks, say —
// without a second fake.
type jqlRouter func(jql string) (string, bool)

// fakeAPIRecording is fakeAPI with a log of what was asked for, so a test can
// assert on requests that should not have been made at all.
func fakeAPIRecording(t *testing.T, routes map[string]string, seen *requestLog) *httptest.Server {
	return fakeAPIRouting(t, routes, seen, routeJQL)
}

func fakeAPIRouting(t *testing.T, routes map[string]string, seen *requestLog, router jqlRouter) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r.Method + " " + r.URL.Path)

		if r.URL.Path == "/graphql" {
			serveGraphQL(w, r, routes)
			return
		}
		// The pull request listing is keyed by query string, not by path.
		if body, handled := routeByQuery(r); handled {
			io.WriteString(w, body)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql" {
			raw, _ := io.ReadAll(r.Body)
			// Record the query itself, not just the path: three different
			// searches share this endpoint and only one of them is the scan.
			seen.record("JQL " + jqlOf(raw))
			body, ok := router(jqlOf(raw))
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"errorMessages":["unrouted jql"]}`)
				return
			}
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
	if r.URL.Query().Get("page") != "1" {
		return `[]`, true
	}
	// The merged pull request's branch is gone, so only its title names the
	// issue — the ordinary shape of finished work.
	return `[
		{"number":1,"title":"PROJ-10-api-endpoint (#4683)","draft":false,
		 "created_at":"2026-08-05T13:00:00Z","updated_at":"2026-08-09T13:00:00Z",
		 "merged_at":"2026-08-09T13:00:00Z","head":{"ref":"deleted-on-merge"}},
		{"number":2,"title":"PROJ-11 ui","draft":false,
		 "created_at":"2026-08-06T13:00:00Z","updated_at":"2026-08-07T13:00:00Z",
		 "head":{"ref":"PROJ-11-ui"}}]`, true
}

func buildStack(t *testing.T) http.Handler {
	handler, _ := buildStackRecording(t)
	return handler
}

func buildStackRecording(t *testing.T) (http.Handler, *requestLog) {
	return buildStackWith(t, jiraRoutes, githubRoutes, routeJQL)
}

func buildStackWith(t *testing.T, jiraFixtures, githubFixtures map[string]string, router jqlRouter) (http.Handler, *requestLog) {
	t.Helper()

	jiraCalls := &requestLog{}
	jiraServer := fakeAPIRouting(t, jiraFixtures, jiraCalls, router)
	githubServer := fakeAPI(t, githubFixtures)

	path := filepath.Join(t.TempDir(), "projects.yaml")
	contents := `
projects:
  - id: team
    name: Team
    settings: { timezone: America/New_York }
    tracker: { type: jira, projectKey: PROJ }
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
	).Routes(), jiraCalls
}

// The whole-project scan is what a retrospective used to pay to learn two
// timestamps: one request per hundred issues that have ever been in a sprint.
//
// The assertion is on the scan's own query rather than on the field lookup that
// precedes it, because the field listing is now also how the estimate field is
// discovered — a proxy that was sharp when it was written and stopped being so
// the moment something else needed the same endpoint.
func TestRetrospectiveNeverScansTheProjectToFindItsSprint(t *testing.T) {
	handler, jiraCalls := buildStackRecording(t)

	if body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective"); body.Sprint.Name != "Sprint 26-31" {
		t.Fatalf("sprint = %+v", body.Sprint)
	}
	for _, call := range jiraCalls.snapshot() {
		if strings.Contains(call, "sprint IS NOT EMPTY") {
			t.Errorf("the project scan ran after all: %s", call)
		}
	}
	if !jiraCalls.contains("GET /rest/agile/1.0/sprint/7354") {
		t.Error("the sprint was not fetched by id")
	}
}

// A whole sprint's status history in one search, rather than one request per
// sub-task. The per-issue fixtures remain in jiraRoutes precisely so that a
// regression here shows up as extra requests rather than as a failure.
func TestRetrospectiveReadsStatusHistoryFromTheSearch(t *testing.T) {
	handler, jiraCalls := buildStackRecording(t)

	if body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective"); len(body.Parents) == 0 {
		t.Fatal("no parents in the response")
	}
	for _, key := range []string{"PROJ-10", "PROJ-11", "PROJ-20", "PROJ-30"} {
		if jiraCalls.contains("GET /rest/api/3/issue/" + key + "/changelog") {
			t.Errorf("%s's changelog was fetched on its own despite arriving with the search", key)
		}
	}
}

type retrospectiveBody struct {
	Sprint struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"sprint"`
	Parents []struct {
		Key     string  `json:"key"`
		DueDate *string `json:"dueDate"`
		InScope bool    `json:"inScope"`
		Rows    []struct {
			Kind      string `json:"kind"`
			Key       string `json:"key"`
			Label     string `json:"label"`
			Intervals []struct {
				State string    `json:"state"`
				From  time.Time `json:"from"`
				To    time.Time `json:"to"`
			} `json:"intervals"`
		} `json:"rows"`
	} `json:"parents"`
	Warnings []string `json:"warnings"`
	Burndown struct {
		Total       float64  `json:"total"`
		Unestimated []string `json:"unestimated"`
	} `json:"burndown"`
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
	// PROJ-4 has no sub-tasks and is now charted in its own right, for the
	// teams that never break work down.
	if len(body.Parents) != 4 {
		t.Fatalf("got %d parents, want 4", len(body.Parents))
	}
	for _, parent := range body.Parents {
		if len(parent.Rows) == 0 {
			t.Errorf("%s was included with no rows to chart", parent.Key)
		}
		if parent.Key == "PROJ-4" {
			if len(parent.Rows) != 1 || parent.Rows[0].Kind != "WORK_ITEM" {
				t.Errorf("PROJ-4 has %d rows %+v, want one charting the work item itself",
					len(parent.Rows), parent.Rows)
			}
		}
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
		for _, subTask := range parent.Rows {
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
		for _, subTask := range parent.Rows {
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

	if len(body.Parents) != 3 {
		t.Fatalf("got %d parents, want the three committed ones", len(body.Parents))
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

// requestLog records what the fakes were asked for. Mutex-guarded because both
// adapters fan out.
type requestLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *requestLog) record(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

// snapshot returns a copy, so a caller can range over it without holding the
// lock through the loop.
func (l *requestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func (l *requestLog) contains(call string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, seen := range l.calls {
		if seen == call {
			return true
		}
	}
	return false
}

// The other team's shape: no sub-tasks anywhere. Every work item is charted in
// its own right, which is the case the app used to return an empty chart for.
func TestRetrospectiveChartsASprintWithNoSubTasks(t *testing.T) {
	jira := map[string]string{}
	for path, body := range jiraRoutes {
		jira[path] = body
	}

	handler, _ := buildStackWith(t, jira, githubRoutes, func(jql string) (string, bool) {
		switch {
		case strings.HasPrefix(jql, "sprint = "):
			return sprintParentsResponse, true
		case strings.HasPrefix(jql, "parent IN "):
			return `{"issues":[]}`, true // this team has none
		case strings.HasPrefix(jql, "key IN "):
			return statusHistoryResponse, true
		}
		return "", false
	})

	body := fetch(t, handler, "/api/v1/projects/team/sprints/7354/retrospective")

	if len(body.Parents) == 0 {
		t.Fatal("no work items charted for a sprint with no sub-tasks")
	}
	for _, parent := range body.Parents {
		if len(parent.Rows) != 1 {
			t.Fatalf("%s has %d rows, want one charting the work item itself", parent.Key, len(parent.Rows))
		}
		if parent.Rows[0].Kind != "WORK_ITEM" {
			t.Errorf("%s row kind is %q, want WORK_ITEM", parent.Key, parent.Rows[0].Kind)
		}
		if parent.Rows[0].Key != parent.Key {
			t.Errorf("%s row is keyed %q, want the work item's own key", parent.Key, parent.Rows[0].Key)
		}
	}
	if body.Burndown.Total <= 0 {
		t.Error("the burndown lost its points along with the sub-tasks")
	}
}
