package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
)

// routes maps a request path to the JSON the fake Jira returns. The fixtures
// are trimmed from real Jira Cloud responses, so field names and the timestamp
// format are the ones the adapter must actually cope with.
func newFakeJira(t *testing.T, routes map[string]string) (*Tracker, *requestLog) {
	t.Helper()
	seen := &requestLog{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r.Method + " " + r.URL.Path)

		if user, pass, ok := r.BasicAuth(); !ok || user != "someone@example.com" || pass != "token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok && r.URL.Path == "/rest/api/3/field" {
			// Field discovery is a prerequisite for most calls now, so the
			// fake answers it unless a test deliberately overrides it.
			body, ok = fieldsFixture, true
		}
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
	return tracker, seen
}

// requestLog records what the fake was asked for. It is mutex-guarded because
// the adapter now fetches changelogs concurrently, and an unsynchronised slice
// here would make the harness itself the source of a data race — the last place
// anyone would look for one.
type requestLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *requestLog) record(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

// snapshot returns a copy, so a caller can range over it while nothing else is
// in flight without holding the lock through the loop.
func (l *requestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func (l *requestLog) contains(call string) bool {
	return l.count(call) > 0
}

func (l *requestLog) count(call string) int {
	n := 0
	for _, seen := range l.snapshot() {
		if seen == call {
			n++
		}
	}
	return n
}

const fieldsFixture = `[
  {"id": "summary", "name": "Summary", "schema": {"type": "string"}},
  {"id": "customfield_10020", "name": "Sprint", "schema": {"type": "array", "custom": "com.pyxis.greenhopper.jira:gh-sprint"}},
  {"id": "customfield_10059", "name": "Story Points", "schema": {"type": "number", "custom": "com.pyxis.greenhopper.jira:gh-story-points"}}
]`

// Two issues carrying overlapping sprint membership, exactly as Jira reports
// it: an issue that carried over lists both sprints.
const sprintScanFixture = `{
  "issues": [
    {"key": "PROJ-1", "fields": {"customfield_10020": [
      {"id": 7354, "name": "Sprint 26-31", "state": "closed",
       "startDate": "2026-07-29T16:00:35.925Z", "endDate": "2026-08-12T18:00:00.000Z"},
      {"id": 7355, "name": "Sprint 26-33", "state": "active",
       "startDate": "2026-08-12T15:24:38.877Z", "endDate": "2026-08-26T18:00:00.000Z"}]}},
    {"key": "PROJ-2", "fields": {"customfield_10020": [
      {"id": 7354, "name": "Sprint 26-31", "state": "closed",
       "startDate": "2026-07-29T16:00:35.925Z", "endDate": "2026-08-12T18:00:00.000Z"},
      {"id": 7400, "name": "Never started", "state": "future"}]}}
  ]
}`

func TestListSprintsReadsSprintsFromTheProjectsOwnIssues(t *testing.T) {
	tracker, _ := newFakeJira(t, map[string]string{
		"/rest/api/3/field":      fieldsFixture,
		"/rest/api/3/search/jql": sprintScanFixture,
	})

	sprints, err := tracker.ListSprints(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"})
	if err != nil {
		t.Fatalf("ListSprints: %v", err)
	}

	// Deduplicated across issues, and the undated future sprint is unusable.
	if len(sprints) != 2 {
		t.Fatalf("got %d sprints, want 2: %+v", len(sprints), sprints)
	}
	if sprints[0].ID != "7355" {
		t.Errorf("first sprint = %s, want the most recently started", sprints[0].ID)
	}
	want := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	if !sprints[0].End.Equal(want) {
		t.Errorf("end = %s, want %s", sprints[0].End, want)
	}
}

func TestListSprintsNeverConsultsABoard(t *testing.T) {
	// A board's sprint list belongs to the board, not the project: on a shared
	// or long-lived board it carries other teams' sprints with no way to tell
	// them apart. Reading membership off the project's issues is what keeps the
	// list scoped, so the board endpoint must not be involved at all.
	tracker, seen := newFakeJira(t, map[string]string{
		"/rest/api/3/field":      fieldsFixture,
		"/rest/api/3/search/jql": sprintScanFixture,
	})

	if _, err := tracker.ListSprints(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}); err != nil {
		t.Fatalf("ListSprints: %v", err)
	}
	for _, call := range seen.snapshot() {
		if strings.Contains(call, "/board/") {
			t.Errorf("consulted a board: %s", call)
		}
	}
}

func TestListSprintsCachesTheScan(t *testing.T) {
	tracker, seen := newFakeJira(t, map[string]string{
		"/rest/api/3/field":      fieldsFixture,
		"/rest/api/3/search/jql": sprintScanFixture,
	})

	for i := 0; i < 3; i++ {
		if _, err := tracker.ListSprints(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}); err != nil {
			t.Fatalf("ListSprints: %v", err)
		}
	}

	var searches int
	for _, call := range seen.snapshot() {
		if strings.Contains(call, "/search/jql") {
			searches++
		}
	}
	if searches != 1 {
		t.Errorf("scanned the project %d times, want 1", searches)
	}
}

const sprintIssuesFixture = `{
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
		"/rest/api/3/search/jql": sprintIssuesFixture,
	})

	parents, err := tracker.SprintParents(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}, "7355")
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
	if len(subTasks) != 0 || len(seen.snapshot()) != 0 {
		t.Errorf("expected no request and no results, got %d requests", len(seen.snapshot()))
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
	for _, call := range seen.snapshot() {
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
		if r.URL.Path == "/rest/api/3/field" {
			io.WriteString(w, fieldsFixture)
			return
		}
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

func TestSprintParentsScopesToTheProject(t *testing.T) {
	// A sprint on a shared board holds other teams' issues too. The query must
	// say so, or a retrospective silently mixes them in.
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/field" {
			io.WriteString(w, fieldsFixture)
			return
		}
		json.NewDecoder(r.Body).Decode(&received)
		io.WriteString(w, `{"issues":[]}`)
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL}, httpclient.New(server.Client()))
	if _, err := tracker.SprintParents(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}, "7355"); err != nil {
		t.Fatalf("SprintParents: %v", err)
	}

	jql, _ := received["jql"].(string)
	if !strings.Contains(jql, `project = "PROJ"`) {
		t.Errorf("jql is not scoped to the project: %q", jql)
	}
	if !strings.Contains(jql, "sprint = 7355") {
		t.Errorf("jql does not name the sprint: %q", jql)
	}
}

func TestChangelogPagingAdvancesByWhatTheServerReturned(t *testing.T) {
	// Jira caps maxResults per endpoint, so a request for 100 may yield 50.
	// Advancing by the requested size would step straight over the entries it
	// declined to send — which is how recent sprints went missing from the
	// sprint list before this was fixed.
	const serverCap = 2
	var offsets []int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/status" {
			io.WriteString(w, statusesFixture)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/changelog") {
			// This site does not attach changelogs to a search, which is what
			// puts the per-issue endpoint below in play at all.
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"errorMessages":["Unrecognized field: expand"]}`)
			return
		}
		startAt, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
		offsets = append(offsets, startAt)

		// Six entries in total, handed out two at a time.
		var entries []string
		for i := startAt; i < startAt+serverCap && i < 6; i++ {
			entries = append(entries, fmt.Sprintf(
				`{"created":"2026-08-0%dT09:00:00.000-0400","items":[{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}`,
				i+1))
		}
		isLast := startAt+serverCap >= 6
		fmt.Fprintf(w, `{"isLast":%t,"values":[%s]}`, isLast, strings.Join(entries, ","))
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL}, httpclient.New(server.Client()))
	history, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-1"})
	if err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}

	if got := len(history["PROJ-1"]); got != 6 {
		t.Errorf("got %d changes, want all 6 — paging skipped entries", got)
	}
	want := []int{0, 2, 4}
	if len(offsets) != len(want) {
		t.Fatalf("requested offsets %v, want %v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("requested offsets %v, want %v", offsets, want)
		}
	}
}

// The changelog fan-out is where a wrong index would be most damaging: every
// issue would get a plausible history belonging to a different issue, and the
// chart would look entirely normal. Each fixture below transitions to a status
// named after its own key, so any mix-up is visible rather than merely wrong.
func TestStatusHistoryKeepsEachIssuesChangelogWithThatIssue(t *testing.T) {
	const issues = 24

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/status" {
			io.WriteString(w, `[]`)
			return
		}
		key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/"), "/changelog")
		fmt.Fprintf(w, `{"isLast": true, "values": [
		  {"created": "2026-08-13T09:00:00.000+0000",
		   "items": [{"field": "status", "fromString": "To Do", "toString": %q}]}]}`, "status-for-"+key)
	}))
	t.Cleanup(server.Close)

	tracker := NewTracker(
		Config{BaseURL: server.URL, Email: "someone@example.com", APIToken: "token"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })),
	)

	keys := make([]domain.IssueKey, 0, issues)
	for i := 1; i <= issues; i++ {
		keys = append(keys, domain.IssueKey("PROJ-"+strconv.Itoa(i)))
	}

	history, err := tracker.StatusHistory(context.Background(), keys)
	if err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}
	if len(history) != issues {
		t.Fatalf("got %d histories, want %d", len(history), issues)
	}
	for _, key := range keys {
		changes := history[key]
		if len(changes) != 1 {
			t.Fatalf("%s: got %d changes, want 1", key, len(changes))
		}
		if want := "status-for-" + string(key); changes[0].To.Name != want {
			t.Errorf("%s carries %q — a changelog belonging to another issue", key, changes[0].To.Name)
		}
	}
}

// Proves the fan-out is real rather than a serial loop wearing a concurrent
// shape. Each request blocks until enough of them have arrived together; run
// serially, none is ever released and the barrier times out.
func TestStatusHistoryFetchesChangelogsConcurrently(t *testing.T) {
	const barrier = 4

	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/status" {
			io.WriteString(w, `[]`)
			return
		}

		now := inFlight.Add(1)
		for {
			high := peak.Load()
			if now <= high || peak.CompareAndSwap(high, now) {
				break
			}
		}
		if now >= barrier {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		inFlight.Add(-1)

		io.WriteString(w, `{"isLast": true, "values": []}`)
	}))
	t.Cleanup(server.Close)

	tracker := NewTracker(
		Config{BaseURL: server.URL, Email: "someone@example.com", APIToken: "token"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })),
	)

	keys := make([]domain.IssueKey, 0, 16)
	for i := 1; i <= 16; i++ {
		keys = append(keys, domain.IssueKey("PROJ-"+strconv.Itoa(i)))
	}

	if _, err := tracker.StatusHistory(context.Background(), keys); err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}
	if peak.Load() < barrier {
		t.Errorf("peak concurrency was %d, want at least %d — the fetches ran serially", peak.Load(), barrier)
	}
	if peak.Load() > maxConcurrency {
		t.Errorf("peak concurrency was %d, want at most %d", peak.Load(), maxConcurrency)
	}
}

func TestSprintFetchesOneSprintWithoutScanningTheProject(t *testing.T) {
	tracker, seen := newFakeJira(t, map[string]string{
		"/rest/agile/1.0/sprint/7354": `{"id":7354,"name":"Sprint 26-31","state":"closed",
			"startDate":"2026-07-29T16:00:35.925Z","endDate":"2026-08-12T18:00:00.000Z"}`,
	})

	sprint, err := tracker.Sprint(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}, "7354")
	if err != nil {
		t.Fatalf("Sprint: %v", err)
	}
	if sprint.Name != "Sprint 26-31" {
		t.Errorf("name = %q", sprint.Name)
	}
	if !sprint.End.Equal(time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %s", sprint.End)
	}

	// The whole point: one request, and none of the scan behind ListSprints.
	for _, call := range seen.snapshot() {
		if strings.Contains(call, "/search/jql") || strings.Contains(call, "/rest/api/3/field") {
			t.Errorf("scanned the project to answer a single-sprint question: %s", call)
		}
	}
}

// The Agile API may be unavailable to a token that can read the platform API.
// Losing the retrospective entirely over that would be a poor trade for the
// requests saved, so the scan remains as a fallback.
func TestSprintFallsBackToTheScanWhenTheAgileAPIIsUnavailable(t *testing.T) {
	tracker, seen := newFakeJira(t, map[string]string{
		// No route for /rest/agile/1.0/sprint/7354, so the fake 404s it.
		"/rest/api/3/field":      fieldsFixture,
		"/rest/api/3/search/jql": sprintScanFixture,
	})

	sprint, err := tracker.Sprint(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}, "7354")
	if err != nil {
		t.Fatalf("Sprint: %v", err)
	}
	if sprint.Name != "Sprint 26-31" {
		t.Errorf("name = %q, want the sprint recovered from the scan", sprint.Name)
	}
	if !seen.contains("GET /rest/agile/1.0/sprint/7354") {
		t.Error("never tried the direct lookup first")
	}
	if seen.count("POST /rest/api/3/search/jql") == 0 {
		t.Error("did not fall back to the scan")
	}
}

// A sprint that exists but was never started carries no dates. Scanning the
// whole project cannot invent them, so the fallback is pointless there.
func TestSprintDoesNotScanForASprintThatHasNoDates(t *testing.T) {
	tracker, seen := newFakeJira(t, map[string]string{
		"/rest/agile/1.0/sprint/7400": `{"id":7400,"name":"Never started","state":"future"}`,
	})

	_, err := tracker.Sprint(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}, "7400")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	if seen.count("POST /rest/api/3/search/jql") != 0 {
		t.Error("scanned the project for dates that do not exist")
	}
}

// A silent fallback is the worst outcome available here: every build quietly
// pays for a full project scan and nothing says why, so the optimisation looks
// like it simply did not work.
func TestSprintWarnsOnceWhenItFallsBackToTheScan(t *testing.T) {
	var log bytes.Buffer
	tracker, _ := newFakeJira(t, map[string]string{
		"/rest/api/3/field":      fieldsFixture,
		"/rest/api/3/search/jql": sprintScanFixture,
	})
	WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))(tracker)

	for i := 0; i < 3; i++ {
		if _, err := tracker.Sprint(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}, "7354"); err != nil {
			t.Fatalf("Sprint: %v", err)
		}
	}

	warnings := strings.Count(log.String(), "falling back to a full project scan")
	if warnings != 1 {
		t.Errorf("logged the fallback %d times, want exactly once — worth saying loudly, worth saying once", warnings)
	}
}

func TestSprintSaysNothingOnTheFastPath(t *testing.T) {
	var log bytes.Buffer
	tracker, _ := newFakeJira(t, map[string]string{
		"/rest/agile/1.0/sprint/7354": `{"id":7354,"name":"Sprint 26-31","state":"closed",
			"startDate":"2026-07-29T16:00:35.925Z","endDate":"2026-08-12T18:00:00.000Z"}`,
	})
	WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))(tracker)

	if _, err := tracker.Sprint(context.Background(), domain.TrackerRef{ProjectKey: "PROJ"}, "7354"); err != nil {
		t.Fatalf("Sprint: %v", err)
	}
	if log.Len() != 0 {
		t.Errorf("warned on the fast path: %s", log.String())
	}
}

// The point of the fast path: a sprint's worth of status history in one search
// rather than one request per sub-task.
func TestStatusHistoryReadsChangelogsFromTheSearchWhenTheSiteAttachesThem(t *testing.T) {
	var searches, changelogs atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/status":
			io.WriteString(w, statusesFixture)
		case strings.HasSuffix(r.URL.Path, "/changelog"):
			changelogs.Add(1)
			io.WriteString(w, `{"isLast":true,"values":[]}`)
		default:
			searches.Add(1)
			io.WriteString(w, `{"issues":[
				{"key":"PROJ-1","changelog":{"startAt":0,"maxResults":100,"total":1,"histories":[
					{"created":"2026-08-04T09:00:00.000-0400","items":[
						{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}},
				{"key":"PROJ-2","changelog":{"startAt":0,"maxResults":100,"total":0,"histories":[]}}]}`)
		}
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL}, httpclient.New(server.Client()))
	history, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-1", "PROJ-2"})
	if err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}

	if len(history["PROJ-1"]) != 1 {
		t.Errorf("PROJ-1 has %d changes, want 1", len(history["PROJ-1"]))
	}
	if history["PROJ-1"][0].To.Name != "In Progress" {
		t.Errorf("PROJ-1 transitioned to %q", history["PROJ-1"][0].To.Name)
	}
	if _, present := history["PROJ-2"]; present {
		t.Error("PROJ-2 has no transitions and should be absent, not present-and-empty")
	}
	if searches.Load() != 1 {
		t.Errorf("made %d searches, want 1", searches.Load())
	}
	if changelogs.Load() != 0 {
		t.Errorf("made %d per-issue changelog requests, want none", changelogs.Load())
	}
}

// Jira caps what it will inline. An issue with a long history is exactly the
// one whose timeline matters most, so a truncated changelog must be refetched
// rather than charted as if that were all that happened.
func TestStatusHistoryRefetchesOnlyTheIssuesWhoseChangelogWasTruncated(t *testing.T) {
	var refetched sync.Map

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/status":
			io.WriteString(w, statusesFixture)
		case strings.HasSuffix(r.URL.Path, "/changelog"):
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/"), "/changelog")
			refetched.Store(key, true)
			io.WriteString(w, `{"isLast":true,"values":[
				{"created":"2026-08-01T09:00:00.000-0400","items":[
					{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]},
				{"created":"2026-08-02T09:00:00.000-0400","items":[
					{"field":"status","from":"3","fromString":"In Progress","to":"10024","toString":"Done"}]}]}`)
		default:
			// PROJ-1 says it has three entries but only one was inlined.
			io.WriteString(w, `{"issues":[
				{"key":"PROJ-1","changelog":{"startAt":0,"maxResults":1,"total":3,"histories":[
					{"created":"2026-08-04T09:00:00.000-0400","items":[
						{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}},
				{"key":"PROJ-2","changelog":{"startAt":0,"maxResults":100,"total":1,"histories":[
					{"created":"2026-08-05T09:00:00.000-0400","items":[
						{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}}]}`)
		}
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL}, httpclient.New(server.Client()))
	history, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-1", "PROJ-2"})
	if err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}

	if _, ok := refetched.Load("PROJ-1"); !ok {
		t.Error("PROJ-1's truncated changelog was charted as complete")
	}
	if _, ok := refetched.Load("PROJ-2"); ok {
		t.Error("PROJ-2's complete changelog was refetched for nothing")
	}
	if len(history["PROJ-1"]) != 2 {
		t.Errorf("PROJ-1 has %d changes, want the 2 from the refetch", len(history["PROJ-1"]))
	}
	if len(history["PROJ-2"]) != 1 {
		t.Errorf("PROJ-2 has %d changes, want 1", len(history["PROJ-2"]))
	}
}

// A site that accepts the expand and quietly ignores it returns issues with no
// changelog attached. Believing that would chart a whole sprint as if nothing
// had ever happened — far worse than making the extra requests.
func TestStatusHistoryFallsBackWhenTheSiteIgnoresTheExpand(t *testing.T) {
	var changelogs atomic.Int32
	var log bytes.Buffer

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/status":
			io.WriteString(w, statusesFixture)
		case strings.HasSuffix(r.URL.Path, "/changelog"):
			changelogs.Add(1)
			io.WriteString(w, `{"isLast":true,"values":[
				{"created":"2026-08-04T09:00:00.000-0400","items":[
					{"field":"status","from":"10039","fromString":"To Do","to":"3","toString":"In Progress"}]}]}`)
		default:
			// Accepted, and silently ignored: no changelog key at all.
			io.WriteString(w, `{"issues":[{"key":"PROJ-1"},{"key":"PROJ-2"}]}`)
		}
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL}, httpclient.New(server.Client()),
		WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn}))))

	history, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-1", "PROJ-2"})
	if err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("got %d histories, want 2 — the silent gap was believed", len(history))
	}
	if changelogs.Load() != 2 {
		t.Errorf("made %d per-issue requests, want 2", changelogs.Load())
	}
	if !strings.Contains(log.String(), "will not attach changelogs") {
		t.Error("degraded silently; nothing would explain why every build is slow")
	}

	// And it does not keep paying for the search on every later call.
	before := changelogs.Load()
	if _, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-1"}); err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}
	if changelogs.Load() != before+1 {
		t.Errorf("second call made %d requests, want 1", changelogs.Load()-before)
	}
	if strings.Count(log.String(), "will not attach changelogs") != 1 {
		t.Error("warned more than once about a fact it already knew")
	}
}

// An empty search result is no evidence either way: the site may inline
// changelogs perfectly well and simply have matched nothing. Concluding it does
// not, permanently, on that basis would be wrong.
func TestStatusHistoryDoesNotCondemnTheSiteOnAnEmptySearch(t *testing.T) {
	var log bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/status":
			io.WriteString(w, statusesFixture)
		case strings.HasSuffix(r.URL.Path, "/changelog"):
			io.WriteString(w, `{"isLast":true,"values":[]}`)
		default:
			io.WriteString(w, `{"issues":[]}`)
		}
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL}, httpclient.New(server.Client()),
		WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn}))))

	if _, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-1"}); err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}
	if tracker.expandKnownRefused() {
		t.Error("gave up on the fast path with no evidence against it")
	}
	if log.Len() != 0 {
		t.Errorf("warned about nothing: %s", log.String())
	}
}

// A timeout or a 500 is this request's problem. Disabling the fast path for the
// life of the process over one is a permanent penalty for a transient fault.
func TestStatusHistorySurfacesATransientSearchFailureWithoutCondemningTheSite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/status" {
			io.WriteString(w, statusesFixture)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tracker := NewTracker(Config{BaseURL: server.URL},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })))

	if _, err := tracker.StatusHistory(context.Background(), []domain.IssueKey{"PROJ-1"}); err == nil {
		t.Fatal("expected the failure to surface rather than be swallowed")
	}
	if tracker.expandKnownRefused() {
		t.Error("a transient failure permanently disabled the fast path")
	}
}
