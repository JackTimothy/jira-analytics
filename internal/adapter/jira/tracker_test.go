package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
)

// routes maps a request path to the JSON the fake Jira returns. The fixtures
// are trimmed from real Jira Cloud responses, so field names and the timestamp
// format are the ones the adapter must actually cope with.
func newFakeJira(t *testing.T, routes map[string]string) (*Tracker, *[]string) {
	t.Helper()
	var seen []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)

		if user, pass, ok := r.BasicAuth(); !ok || user != "someone@example.com" || pass != "token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"errorMessages":["no fixture for `+r.URL.Path+`"]}`)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	tracker := NewTracker(
		Config{BaseURL: server.URL, Email: "someone@example.com", APIToken: "token"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })),
	)
	return tracker, &seen
}

const sprintsFixture = `{
  "isLast": true,
  "values": [
    {"id": 7354, "name": "Sprint 26-31", "state": "closed",
     "startDate": "2026-07-29T16:00:35.925Z", "endDate": "2026-08-12T18:00:00.000Z"},
    {"id": 7355, "name": "Sprint 26-33", "state": "active",
     "startDate": "2026-08-12T15:24:38.877Z", "endDate": "2026-08-26T18:00:00.000Z"},
    {"id": 7400, "name": "Future sprint, never started", "state": "future"}
  ]
}`

func TestListSprintsOrdersMostRecentFirstAndSkipsUndatedSprints(t *testing.T) {
	tracker, _ := newFakeJira(t, map[string]string{
		"/rest/agile/1.0/board/45/sprint": sprintsFixture,
	})

	sprints, err := tracker.ListSprints(context.Background(), domain.TrackerRef{BoardID: "45"})
	if err != nil {
		t.Fatalf("ListSprints: %v", err)
	}

	if len(sprints) != 2 {
		t.Fatalf("got %d sprints, want 2 (the undated one is unusable)", len(sprints))
	}
	if sprints[0].ID != "7355" {
		t.Errorf("first sprint = %s, want the most recently started", sprints[0].ID)
	}
	want := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	if !sprints[0].End.Equal(want) {
		t.Errorf("end = %s, want %s", sprints[0].End, want)
	}
}

const sprintIssuesFixture = `{
  "startAt": 0, "maxResults": 100, "total": 3,
  "issues": [
    {"key": "PROJ-1", "fields": {
      "summary": "Committed story", "duedate": "2026-08-26",
      "created": "2026-07-27T14:16:31.828-0400",
      "issuetype": {"name": "Story", "subtask": false},
      "status": {"id": "10009", "name": "Review", "statusCategory": {"key": "indeterminate"}}}},
    {"key": "PROJ-2", "fields": {
      "summary": "Pulled in, never committed", "duedate": null,
      "created": "2026-08-04T09:00:00.000-0400",
      "issuetype": {"name": "Bug", "subtask": false},
      "status": {"id": "3", "name": "In Progress", "statusCategory": {"key": "indeterminate"}}}},
    {"key": "PROJ-11", "fields": {
      "summary": "A sub-task that should be ignored here",
      "created": "2026-08-04T09:00:00.000-0400",
      "issuetype": {"name": "Sub-task", "subtask": true},
      "status": {"id": "3", "name": "In Progress", "statusCategory": {"key": "indeterminate"}}}}
  ]
}`

func TestSprintParentsExcludesSubTasksAndParsesDueDates(t *testing.T) {
	tracker, _ := newFakeJira(t, map[string]string{
		"/rest/agile/1.0/sprint/7355/issue": sprintIssuesFixture,
	})

	parents, err := tracker.SprintParents(context.Background(), domain.TrackerRef{}, "7355")
	if err != nil {
		t.Fatalf("SprintParents: %v", err)
	}

	if len(parents) != 2 {
		t.Fatalf("got %d parents, want 2 — sub-tasks belong to their parent, not the sprint", len(parents))
	}
	if parents[0].DueDate == nil || parents[0].DueDate.String() != "2026-08-26" {
		t.Errorf("PROJ-1 due date = %v", parents[0].DueDate)
	}
	if parents[1].DueDate != nil {
		t.Errorf("PROJ-2 has no due date in Jira but parsed as %v", parents[1].DueDate)
	}
	// Jira's offset format has no colon, which time.RFC3339 rejects outright.
	if parents[0].Created.IsZero() {
		t.Error("created timestamp failed to parse")
	}
}

const subTasksFixture = `{
  "issues": [
    {"key": "PROJ-10", "fields": {
      "summary": "UI", "created": "2026-08-04T09:00:00.000-0400",
      "issuetype": {"name": "Sub-task", "subtask": true},
      "parent": {"key": "PROJ-1"},
      "status": {"id": "10024", "name": "Done", "statusCategory": {"key": "done"}}}},
    {"key": "PROJ-11", "fields": {
      "summary": "API", "created": "2026-08-04T09:00:00.000-0400",
      "issuetype": {"name": "Sub-task", "subtask": true},
      "parent": {"key": "PROJ-1"},
      "status": {"id": "10047", "name": "Blocked", "statusCategory": {"key": "new"}}}}
  ]
}`

func TestSubTasksOfMapsParentAndStatusCategory(t *testing.T) {
	tracker, _ := newFakeJira(t, map[string]string{
		"/rest/api/3/search/jql": subTasksFixture,
	})

	subTasks, err := tracker.SubTasksOf(context.Background(), []domain.IssueKey{"PROJ-1"})
	if err != nil {
		t.Fatalf("SubTasksOf: %v", err)
	}
	if len(subTasks) != 2 {
		t.Fatalf("got %d sub-tasks, want 2", len(subTasks))
	}
	if subTasks[0].ParentKey != "PROJ-1" {
		t.Errorf("parent = %s", subTasks[0].ParentKey)
	}
	if !subTasks[0].Status.IsDone() {
		t.Error("Done should map to the terminal category")
	}
	if !subTasks[1].Status.IsBlocked() {
		t.Error("Blocked should be recognised by name regardless of category")
	}
}

func TestSubTasksOfSkipsTheCallWhenThereAreNoParents(t *testing.T) {
	tracker, seen := newFakeJira(t, map[string]string{})
	subTasks, err := tracker.SubTasksOf(context.Background(), nil)
	if err != nil {
		t.Fatalf("SubTasksOf: %v", err)
	}
	if len(subTasks) != 0 || len(*seen) != 0 {
		t.Errorf("expected no request and no results, got %d requests", len(*seen))
	}
}

const statusesFixture = `[
  {"id": "10039", "name": "To Do", "statusCategory": {"key": "new"}},
  {"id": "3", "name": "In Progress", "statusCategory": {"key": "indeterminate"}},
  {"id": "10009", "name": "Review", "statusCategory": {"key": "indeterminate"}},
  {"id": "10047", "name": "Blocked", "statusCategory": {"key": "new"}},
  {"id": "10024", "name": "Done", "statusCategory": {"key": "done"}},
  {"id": "10209", "name": "Cancelled", "statusCategory": {"key": "done"}}
]`

const changelogFixture = `{
  "isLast": true,
  "values": [
    {"created": "2026-08-05T09:54:44.275-0400", "items": [
      {"field": "status", "from": "10039", "fromString": "To Do", "to": "3", "toString": "In Progress"}]},
    {"created": "2026-08-06T11:00:00.000-0400", "items": [
      {"field": "assignee", "from": null, "to": "abc"},
      {"field": "status", "from": "3", "fromString": "In Progress", "to": "10009", "toString": "Review"}]},
    {"created": "2026-08-07T15:30:00.000-0400", "items": [
      {"field": "status", "from": "10009", "fromString": "Review", "to": "99999", "toString": "A Deleted Status"}]}
  ]
}`

func TestStatusHistoryResolvesCategoriesAndIgnoresOtherFields(t *testing.T) {
	tracker, _ := newFakeJira(t, map[string]string{
		"/rest/api/3/status":                  statusesFixture,
		"/rest/api/3/issue/PROJ-10/changelog": changelogFixture,
	})

	history, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-10"})
	if err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}

	changes := history["PROJ-10"]
	if len(changes) != 3 {
		t.Fatalf("got %d status changes, want 3 (the assignee change is not one)", len(changes))
	}
	if changes[0].From.Name != "To Do" || changes[0].From.Category != domain.CategoryToDo {
		t.Errorf("first transition's From = %+v", changes[0].From)
	}
	if changes[1].To.Name != "Review" || changes[1].To.Category != domain.CategoryInProgress {
		t.Errorf("Review should resolve to the in-progress category, got %+v", changes[1].To)
	}
	// A status id absent from the site's list must not derail the timeline.
	if changes[2].To.Name != "A Deleted Status" || changes[2].To.Category != domain.CategoryInProgress {
		t.Errorf("unknown status fell back to %+v", changes[2].To)
	}
	if !changes[0].At.Before(changes[1].At) {
		t.Error("changes are not in chronological order")
	}
}

func TestStatusHistoryFetchesStatusDefinitionsOnlyOnce(t *testing.T) {
	tracker, seen := newFakeJira(t, map[string]string{
		"/rest/api/3/status":                  statusesFixture,
		"/rest/api/3/issue/PROJ-10/changelog": changelogFixture,
		"/rest/api/3/issue/PROJ-11/changelog": changelogFixture,
	})

	if _, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-10", "PROJ-11"}); err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}
	if _, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-10"}); err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}

	var statusCalls int
	for _, call := range *seen {
		if call == "GET /rest/api/3/status" {
			statusCalls++
		}
	}
	if statusCalls != 1 {
		t.Errorf("status definitions fetched %d times, want 1", statusCalls)
	}
}

func TestSubTasksOfSendsAParentInQuery(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		io.WriteString(w, `{"issues":[]}`)
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL}, httpclient.New(server.Client()))
	if _, err := tracker.SubTasksOf(context.Background(), []domain.IssueKey{"PROJ-1", "PROJ-2"}); err != nil {
		t.Fatalf("SubTasksOf: %v", err)
	}

	jql, _ := received["jql"].(string)
	if jql != `parent IN ("PROJ-1","PROJ-2") ORDER BY key ASC` {
		t.Errorf("jql = %q", jql)
	}
}
