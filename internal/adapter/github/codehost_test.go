package github

import (
	"bytes"
	"context"
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

var testRepo = domain.RepoRef{Owner: "org", Name: "repo"}

var testGitHubWindow = domain.Window{
	Start: time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC),
}

// A merged pull request whose branch has been deleted — the ordinary state of
// finished work — alongside one whose branch still exists, and one belonging to
// nobody we asked about.
const pullListFixture = `[
	{"number":7,"title":"OTCO-9999 unrelated","draft":false,
	 "created_at":"2026-08-06T09:00:00Z","updated_at":"2026-08-10T09:00:00Z",
	 "head":{"ref":"OTCO-9999-other-team"}},
	{"number":8,"title":"PROJ-11-api","draft":true,
	 "created_at":"2026-08-07T09:00:00Z","updated_at":"2026-08-07T09:00:00Z",
	 "head":{"ref":"PROJ-11-api"}},
	{"number":9,"title":"PROJ-10-add-picker-and-align-shared-routes (#4683)","draft":false,
	 "created_at":"2026-08-06T09:00:00Z","updated_at":"2026-08-10T09:00:00Z",
	 "merged_at":"2026-08-10T09:00:00Z","head":{"ref":"deleted-branch-name"}}
]`

// fixtures trimmed from real GitHub responses.
var fixtures = map[string]string{
	"/repos/org/repo": `{"default_branch":"dev"}`,

	// matching-refs answers a raw-character prefix query, so PROJ-1 also
	// returns PROJ-11 and PROJ-110. The adapter must reject those itself.
	"/repos/org/repo/git/matching-refs/heads/PROJ-": `[
		{"ref":"refs/heads/PROJ-10-add-picker"},
		{"ref":"refs/heads/PROJ-11-api"},
		{"ref":"refs/heads/PROJ-110-unrelated-work"}]`,
	"/repos/org/repo/git/matching-refs/heads/proj-": `[]`,

	"/repos/org/repo/compare/dev...PROJ-10-add-picker": `{"commits":[
		{"commit":{"committer":{"date":"2026-08-04T13:00:00Z"}}},
		{"commit":{"committer":{"date":"2026-08-05T13:00:00Z"}}}]}`,
	"/repos/org/repo/compare/dev...PROJ-11-api": `{"commits":[
		{"commit":{"committer":{"date":"2026-08-06T13:00:00Z"}}}]}`,

	"/repos/org/repo/pulls/9/reviews": `[
		{"user":{"login":"alice","type":"User"},"state":"CHANGES_REQUESTED","submitted_at":"2026-08-08T10:00:00Z"},
		{"user":{"login":"alice","type":"User"},"state":"APPROVED","submitted_at":"2026-08-09T10:00:00Z"},
		{"user":{"login":"copilot-pull-request-reviewer[bot]","type":"Bot"},"state":"APPROVED","submitted_at":"2026-08-07T10:00:00Z"}]`,
	"/repos/org/repo/issues/9/timeline": `[
		{"event":"ready_for_review","created_at":"2026-08-07T09:00:00Z"},
		{"event":"review_requested","created_at":"2026-08-07T09:05:00Z","requested_reviewer":{"login":"alice","type":"User"}},
		{"event":"merged","created_at":"2026-08-10T09:00:00Z"},
		{"event":"closed","created_at":"2026-08-10T09:00:00Z"}]`,

	"/repos/org/repo/pulls/8/reviews":   `[]`,
	"/repos/org/repo/issues/8/timeline": `[]`,

	"/repos/org/repo/pulls/8/commits": `[{"commit":{"committer":{"date":"2026-08-06T13:00:00Z"}}}]`,
	"/repos/org/repo/pulls/9/commits": `[{"commit":{"committer":{"date":"2026-08-04T13:00:00Z"}}}]`,
}

func newFakeGitHub(t *testing.T) (*CodeHost, *requestLog) {
	return fakeGitHubIn(t, graphQLNormal)
}

// fakeGitHubIn serves both transports from one fixture set. The mode decides
// how the GraphQL half misbehaves, so every fallback the adapter has is
// exercised against the same assertions rather than only described.
func fakeGitHubIn(t *testing.T, mode graphQLMode) (*CodeHost, *requestLog) {
	t.Helper()
	seen := &requestLog{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r.URL.Path)

		if r.URL.Path == "/graphql" {
			serveGraphQL(w, r, mode)
			return
		}

		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.URL.Path == "/repos/org/repo/pulls" {
			if r.URL.Query().Get("page") != "1" {
				io.WriteString(w, `[]`)
				return
			}
			io.WriteString(w, pullListFixture)
			return
		}

		body, ok := fixtures[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"no fixture for `+r.URL.Path+`"}`)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	host := NewCodeHost(
		Config{BaseURL: server.URL, Token: "test-token"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })),
	)
	return host, seen
}

// requestLog records what the fake was asked for. It is mutex-guarded because
// this adapter fans out across pull requests, so an unsynchronised slice here
// would make the harness itself the source of a data race — the last place
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

func TestLinkedEventsBuildsATimelineFromBranchesPullsAndReviews(t *testing.T) {
	host, _ := newFakeGitHub(t)

	result, err := host.LinkedEvents(
		context.Background(),
		[]domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true},
		[]domain.IssueKey{"PROJ-10", "PROJ-11"},
		testGitHubWindow,
	)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("got %d linked issues, want 2: %v", len(result), result)
	}

	// Replaying the events must reproduce the story the fixtures tell.
	events := result["PROJ-10"]
	window := domain.Window{
		Start: time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC),
	}
	intervals := domain.BuildTimeline(events, domain.IssueStatus{Name: "To Do"},
		time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), window)

	want := []domain.State{
		domain.StateToDo,            // until the branch's first commit
		domain.StateInProgress,      // branch, then a draft pull request
		domain.StateReviewRequested, // ready for review, alice requested
		domain.StateFeedbackGiven,   // alice requests changes
		domain.StateApproved,        // alice approves
		domain.StateDone,            // merged
	}
	if len(intervals) != len(want) {
		t.Fatalf("got %d intervals, want %d: %+v", len(intervals), len(want), intervals)
	}
	for i, state := range want {
		if intervals[i].State != state {
			t.Errorf("interval %d = %s, want %s", i, intervals[i].State, state)
		}
	}
}

func TestLinkedEventsIgnoresBranchesWithNoRequestedKey(t *testing.T) {
	host, seen := newFakeGitHub(t)

	result, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	if _, ok := result["PROJ-11"]; ok {
		t.Error("PROJ-11 was not requested but appeared in the result")
	}
	for _, path := range seen.snapshot() {
		if path == "/repos/org/repo/compare/dev...dependabot/npm/react-19" {
			t.Error("a branch with no issue key was investigated")
		}
	}
}

func TestLinkedEventsExcludesBotApprovalsFromReviewStates(t *testing.T) {
	host, _ := newFakeGitHub(t)

	result, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true}, []domain.IssueKey{"PROJ-10"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	for _, event := range result["PROJ-10"] {
		if submitted, ok := event.(domain.ReviewSubmitted); ok {
			if submitted.Actor != "alice" {
				t.Errorf("a non-human review reached the domain: %+v", submitted)
			}
		}
	}
}

func TestLinkedEventsReadsTheDefaultBranchOncePerRepo(t *testing.T) {
	host, seen := newFakeGitHub(t)

	if _, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	var repoCalls int
	for _, path := range seen.snapshot() {
		if path == "/repos/org/repo" {
			repoCalls++
		}
	}
	if repoCalls != 1 {
		t.Errorf("repository metadata fetched %d times, want 1", repoCalls)
	}
}

func TestLinkedEventsSkipsWorkWhenThereIsNothingToLink(t *testing.T) {
	host, seen := newFakeGitHub(t)

	if _, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo}, domain.ReviewerPolicy{}, nil, testGitHubWindow); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}
	if _, err := host.LinkedEvents(context.Background(), nil, domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10"}, testGitHubWindow); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}
	if len(seen.snapshot()) != 0 {
		t.Errorf("made %d requests with nothing to do", len(seen.snapshot()))
	}
}

func TestLinkedEventsReportsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)
	}))
	defer server.Close()

	host := NewCodeHost(Config{BaseURL: server.URL, Token: "x"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })))

	_, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10"}, testGitHubWindow)
	if err == nil {
		t.Fatal("expected an error rather than a silently empty result")
	}
}

var errBoom = errorString("boom")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestQueryPrefixesCollapsesKeysToProjectPrefixes(t *testing.T) {
	got := queryPrefixes([]domain.IssueKey{"PROJ-10", "PROJ-11", "OTHER-3", "malformed"})
	want := []string{"OTHER-", "PROJ-", "other-", "proj-"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMatchingBranchesRejectsRawPrefixFalsePositives(t *testing.T) {
	host, seen := newFakeGitHub(t)

	// PROJ-110 shares a character prefix with PROJ-11 but is a different issue,
	// and nobody asked about it.
	result, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	if _, ok := result["PROJ-110"]; ok {
		t.Error("a raw-prefix false positive was linked")
	}
	if len(result) != 2 {
		t.Errorf("got %d linked issues, want 2: %v", len(result), keysOf(result))
	}
	for _, path := range seen.snapshot() {
		if strings.Contains(path, "PROJ-110") {
			t.Errorf("investigated a branch nobody asked about: %s", path)
		}
	}
}

func TestMatchingBranchesQueriesOncePerProjectPrefixNotOncePerIssue(t *testing.T) {
	host, seen := newFakeGitHub(t)

	if _, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	var refQueries int
	for _, path := range seen.snapshot() {
		if strings.Contains(path, "/git/matching-refs/") {
			refQueries++
		}
		if strings.HasSuffix(path, "/branches") {
			t.Error("fell back to listing every branch in the repository")
		}
	}
	// One for the project prefix, one for its lowercase form — not one per key.
	if refQueries != 2 {
		t.Errorf("made %d ref queries for 2 issues in 1 project, want 2", refQueries)
	}
}

func keysOf(m map[domain.IssueKey][]domain.Event) []domain.IssueKey {
	out := make([]domain.IssueKey, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

// A squash merge leaves "Title (#123)" as the commit message and, by default,
// deletes the head branch. Discovery that starts from branches goes blind on
// precisely the work that finished, so this is the case that matters most.
func TestLinkedEventsFindsMergedWorkAfterItsBranchIsDeleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/org/repo":
			io.WriteString(w, `{"default_branch":"dev"}`)

		// No branches at all: every one has been deleted on merge.
		case strings.Contains(r.URL.Path, "/git/matching-refs/"):
			io.WriteString(w, `[]`)

		case r.URL.Path == "/repos/org/repo/pulls":
			if r.URL.Query().Get("page") != "1" {
				io.WriteString(w, `[]`)
				return
			}
			io.WriteString(w, `[{"number":4683,
				"title":"OTCO-5854-Update-Balance-of-Plant-shared-routes-to-align-with-Asset-Models-shared-routes",
				"draft":false,
				"created_at":"2026-08-05T09:00:00Z","updated_at":"2026-08-10T09:00:00Z",
				"merged_at":"2026-08-10T09:00:00Z",
				"head":{"ref":"OTCO-5854-Update-Balance-of-Plant-shared-routes"}}]`)

		case r.URL.Path == "/graphql":
			// This test is about finding the pull request at all after its
			// branch is gone, not about how its detail is fetched. Reporting
			// truncation sends the adapter down the REST path deliberately,
			// which is where this test's fixtures live.
			io.WriteString(w, `{"data":{"repository":{"p0":{"number":4683,
				"commits":{"totalCount":1,"nodes":[]},
				"reviews":{"totalCount":99,"nodes":[]},
				"timelineItems":{"totalCount":99,"nodes":[]}}}}}`)

		case r.URL.Path == "/repos/org/repo/pulls/4683/commits":
			io.WriteString(w, `[{"commit":{"committer":{"date":"2026-08-04T13:00:00Z"}}}]`)

		case r.URL.Path == "/repos/org/repo/issues/4683/timeline":
			io.WriteString(w, `[{"event":"review_requested","created_at":"2026-08-06T09:00:00Z",
				"requested_reviewer":{"login":"alice","type":"User"}},
				{"event":"merged","created_at":"2026-08-10T09:00:00Z"},
				{"event":"closed","created_at":"2026-08-10T09:00:00Z"}]`)

		case r.URL.Path == "/repos/org/repo/pulls/4683/reviews":
			io.WriteString(w, `[{"user":{"login":"alice","type":"User"},"state":"APPROVED",
				"submitted_at":"2026-08-09T10:00:00Z"}]`)

		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"no fixture for `+r.URL.Path+`"}`)
		}
	}))
	defer server.Close()

	host := NewCodeHost(Config{BaseURL: server.URL, Token: "t"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })))

	result, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true}, []domain.IssueKey{"OTCO-5854"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	events, ok := result["OTCO-5854"]
	if !ok {
		t.Fatal("the merged work was not found — branch-led discovery would miss it entirely")
	}

	intervals := domain.BuildTimeline(events, domain.IssueStatus{Name: "To Do"},
		time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), testGitHubWindow)

	want := []domain.State{
		domain.StateToDo,
		domain.StateInProgress,      // first commit on the branch
		domain.StateReviewRequested, // alice asked
		domain.StateApproved,        // alice approved
		domain.StateDone,            // merged
	}
	if len(intervals) != len(want) {
		t.Fatalf("got %d intervals, want %d: %+v", len(intervals), len(want), intervals)
	}
	for i, state := range want {
		if intervals[i].State != state {
			t.Errorf("interval %d = %s, want %s", i, intervals[i].State, state)
		}
	}
}

func TestLinkedEventsMatchesOnTitleWhenTheBranchNameDoesNot(t *testing.T) {
	// Some branches are named without the key even though the title carries it.
	host, _ := newFakeGitHub(t)

	result, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true}, []domain.IssueKey{"PROJ-10"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}
	// Pull request 9 has head "deleted-branch-name"; only its title says PROJ-10.
	var sawMerge bool
	for _, event := range result["PROJ-10"] {
		if _, ok := event.(domain.PRMerged); ok {
			sawMerge = true
		}
	}
	if !sawMerge {
		t.Error("a pull request matched only by title was not linked")
	}
}

func TestPullsInWindowIgnoresWorkOutsideTheSprint(t *testing.T) {
	host, _ := newFakeGitHub(t)
	matcher := newKeyMatcher([]domain.IssueKey{"PROJ-10", "PROJ-11"})

	// A window long after the fixture's pull requests: all are older than the
	// lookback, so none should survive.
	future := domain.Window{
		Start: time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2027, 6, 15, 0, 0, 0, 0, time.UTC),
	}
	matches, err := host.pullsInWindow(context.Background(), testRepo, future, matcher)
	if err != nil {
		t.Fatalf("pullsInWindow: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("got %d matches for a window with no activity: %+v", len(matches), matches)
	}
}

func TestPullsInWindowIgnoresARepeatedListing(t *testing.T) {
	// A listing that hands back the same pull requests on every page would
	// otherwise be replayed: a second "opened" event clears the merge recorded
	// by the first, leaving the timeline oscillating between states.
	var pages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		var repeated []string
		for i := 0; i < 100; i++ {
			repeated = append(repeated, `{"number":1,"title":"PROJ-10 work","draft":false,
				"created_at":"2026-08-05T13:00:00Z","updated_at":"2026-08-09T13:00:00Z",
				"merged_at":"2026-08-09T13:00:00Z","head":{"ref":"PROJ-10-work"}}`)
		}
		io.WriteString(w, "["+strings.Join(repeated, ",")+"]")
	}))
	defer server.Close()

	host := NewCodeHost(Config{BaseURL: server.URL, Token: "t"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })))

	matches, err := host.pullsInWindow(context.Background(), testRepo, testGitHubWindow,
		newKeyMatcher([]domain.IssueKey{"PROJ-10"}))
	if err != nil {
		t.Fatalf("pullsInWindow: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("got %d matches for one repeated pull request, want 1", len(matches))
	}
	// A wave is fetched at once, so the repeat is only detected after the whole
	// of it has arrived — that over-fetch is the deliberate cost of not paging
	// serially. What must not happen is a second wave.
	if pages.Load() > pullPageWave {
		t.Errorf("kept paging a listing that repeated itself: %d pages, want at most one wave of %d",
			pages.Load(), pullPageWave)
	}
}

// Paging in waves fetches pages the scan turns out not to need. Those pages
// must be discarded rather than merged in: the listing is newest-first, so
// anything past the page that crossed the cutoff is older than the lookback and
// has no business in the retrospective.
func TestPullsInWindowDiscardsPagesFetchedPastTheCutoff(t *testing.T) {
	// Page 2 crosses the cutoff. Pages 3 and 4 arrive in the same wave and
	// carry pull requests that match the key but are far too old.
	const stale = "2020-01-01T00:00:00Z"

	page := func(number int, updated string) string {
		var entries []string
		for i := 0; i < 100; i++ {
			entries = append(entries, fmt.Sprintf(`{"number":%d,"title":"PROJ-10 work","draft":false,
				"created_at":"2026-08-05T13:00:00Z","updated_at":%q,
				"head":{"ref":"PROJ-10-work"}}`, number+i, updated))
		}
		return "[" + strings.Join(entries, ",") + "]"
	}

	var requested sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		requested.Store(n, true)
		switch n {
		case 1:
			io.WriteString(w, page(1000, "2026-08-09T13:00:00Z"))
		default:
			// Everything from page 2 on is older than the lookback.
			io.WriteString(w, page(1000+n*1000, stale))
		}
	}))
	defer server.Close()

	host := NewCodeHost(Config{BaseURL: server.URL, Token: "t"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })))

	matches, err := host.pullsInWindow(context.Background(), testRepo, testGitHubWindow,
		newKeyMatcher([]domain.IssueKey{"PROJ-10"}))
	if err != nil {
		t.Fatalf("pullsInWindow: %v", err)
	}

	// Only page 1's hundred are inside the lookback.
	if len(matches) != 100 {
		t.Fatalf("got %d matches, want the 100 from page 1 — later pages in the wave leaked in", len(matches))
	}
	for _, match := range matches {
		if match.pull.Number >= 2000 {
			t.Fatalf("pull #%d came from a page past the cutoff", match.pull.Number)
		}
	}

	// The over-fetch itself is expected; the test is that it was discarded.
	if _, ok := requested.Load(3); !ok {
		t.Log("page 3 was never requested; the wave may have been narrower than expected")
	}
}

// Repositories share nothing but a read-only matcher, so one must not wait on
// another.
//
// The gate is per repository rather than per request, because a single
// repository already fires a wave of page requests concurrently — counting
// requests would show plenty of concurrency without the repositories
// overlapping at all.
func TestLinkedEventsQueriesRepositoriesConcurrently(t *testing.T) {
	var mu sync.Mutex
	blocked := map[string]bool{}

	release := make(chan struct{})
	var once sync.Once
	var timedOut atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			repo := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")[1]

			mu.Lock()
			first := !blocked[repo]
			blocked[repo] = true
			distinct := len(blocked)
			mu.Unlock()

			// Only the first request from each repository holds the gate, so
			// the wave of pages behind it cannot satisfy the barrier alone.
			if first {
				if distinct >= 2 {
					once.Do(func() { close(release) })
				}
				select {
				case <-release:
				case <-time.After(2 * time.Second):
					timedOut.Store(true)
				}
			}
		}
		io.WriteString(w, `[]`)
	}))
	defer server.Close()

	host := NewCodeHost(Config{BaseURL: server.URL, Token: "t"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })))

	repos := []domain.RepoRef{{Owner: "org", Name: "one"}, {Owner: "org", Name: "two"}}
	if _, err := host.LinkedEvents(context.Background(), repos, domain.ReviewerPolicy{},
		[]domain.IssueKey{"PROJ-10"}, testGitHubWindow); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	if timedOut.Load() {
		t.Error("a repository waited alone at the barrier — the repositories are still queried one after the other")
	}
}

// The timeline, reviews and commits of one pull request are three independent
// reads. Serially they made every pull request three round trips deep.
func TestEventsForPullRequestReadsItsThreeEndpointsConcurrently(t *testing.T) {
	const endpoints = 3

	var inFlight, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := inFlight.Add(1)
		for {
			high := peak.Load()
			if now <= high || peak.CompareAndSwap(high, now) {
				break
			}
		}
		if now >= endpoints {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		inFlight.Add(-1)
		io.WriteString(w, `[]`)
	}))
	defer server.Close()

	host := NewCodeHost(Config{BaseURL: server.URL, Token: "t"},
		httpclient.New(server.Client(), httpclient.WithSleep(func(context.Context, time.Duration) error { return nil })))

	pull := pullJSON{Number: 1, Title: "PROJ-10 work"}
	pull.Head.Ref = "PROJ-10-work"
	if _, err := host.eventsForPullRequest(context.Background(), testRepo, pull, domain.ReviewerPolicy{}); err != nil {
		t.Fatalf("eventsForPullRequest: %v", err)
	}
	if peak.Load() < endpoints {
		t.Errorf("peak concurrency was %d, want %d — the three reads still run in sequence", peak.Load(), endpoints)
	}
}

// linkedEventsIn runs the standard fixture through whichever transport the mode
// selects, and returns the events it produced.
func linkedEventsIn(t *testing.T, mode graphQLMode) map[domain.IssueKey][]domain.Event {
	t.Helper()
	host, _ := fakeGitHubIn(t, mode)

	events, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true},
		[]domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}
	return events
}

// The whole claim of the GraphQL path is that it produces exactly what the REST
// path produced. Identical, not similar: both transports read the same fixtures,
// so any difference in the events is a difference in the adapter.
//
// graphQLAlwaysTruncated forces every pull request down the REST fallback, which
// makes this one test cover both the parity claim and the fallback itself.
func TestBothTransportsProduceIdenticalEvents(t *testing.T) {
	viaGraphQL := linkedEventsIn(t, graphQLNormal)
	viaREST := linkedEventsIn(t, graphQLAlwaysTruncated)

	if len(viaGraphQL) != len(viaREST) {
		t.Fatalf("GraphQL produced events for %d keys, REST for %d", len(viaGraphQL), len(viaREST))
	}
	for key, graphEvents := range viaGraphQL {
		restEvents, ok := viaREST[key]
		if !ok {
			t.Fatalf("%s has events over GraphQL but none over REST", key)
		}
		if len(graphEvents) != len(restEvents) {
			t.Fatalf("%s: %d events over GraphQL, %d over REST\n graphql=%+v\n rest=%+v",
				key, len(graphEvents), len(restEvents), graphEvents, restEvents)
		}
		// Events are recorded in the order the adapter builds them, which is
		// itself part of what must not change.
		for i := range graphEvents {
			if fmt.Sprintf("%T%+v", graphEvents[i], graphEvents[i]) !=
				fmt.Sprintf("%T%+v", restEvents[i], restEvents[i]) {
				t.Errorf("%s event %d differs:\n graphql=%T%+v\n rest=%T%+v",
					key, i, graphEvents[i], graphEvents[i], restEvents[i], restEvents[i])
			}
		}
	}
}

// Ten pull requests in one query rather than three requests each.
func TestPullDetailIsBatchedRatherThanFetchedPerPullRequest(t *testing.T) {
	host, seen := fakeGitHubIn(t, graphQLNormal)
	if _, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true},
		[]domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	calls := seen.snapshot()
	graphQL := 0
	for _, path := range calls {
		switch {
		case path == "/graphql":
			graphQL++
		case strings.HasSuffix(path, "/timeline"),
			strings.HasSuffix(path, "/reviews"),
			strings.HasSuffix(path, "/commits"):
			t.Errorf("fetched %s per pull request despite the batch answering", path)
		}
	}
	if graphQL != 1 {
		t.Errorf("made %d GraphQL queries for two pull requests, want 1", graphQL)
	}
}

// GraphQL reports failure as a 200 carrying data and errors together, which the
// HTTP client cannot see. Charting half a sprint's review history as if that
// were all of it is the worst outcome available here.
func TestPartialGraphQLFailureIsAnErrorRatherThanAHalfBuiltTimeline(t *testing.T) {
	host, _ := fakeGitHubIn(t, graphQLPartialFailure)

	_, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true},
		[]domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow)
	if err == nil {
		t.Fatal("a 200 carrying an errors array was treated as success")
	}
	if !strings.Contains(err.Error(), "RATE_LIMITED") {
		t.Errorf("error does not say what went wrong: %v", err)
	}
}

// A pull request missing from the answer with no truncation flag and no error
// must be refetched, not charted as though nothing ever happened to it.
func TestAPullRequestMissingFromTheBatchIsRefetchedRatherThanBelieved(t *testing.T) {
	host, seen := fakeGitHubIn(t, graphQLOmitsAPull)

	events, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true},
		[]domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	refetched := false
	for _, path := range seen.snapshot() {
		if strings.HasSuffix(path, "/timeline") {
			refetched = true
		}
	}
	if !refetched {
		t.Error("the silently absent pull request was never refetched")
	}
	if len(events) == 0 {
		t.Error("no events at all after the refetch")
	}
}

func TestGraphQLEndpointHandlesEnterpriseAndDotCom(t *testing.T) {
	cases := map[string]string{
		"https://api.github.com":          "https://api.github.com/graphql",
		"https://api.github.com/":         "https://api.github.com/graphql",
		"https://ghe.example.com/api/v3":  "https://ghe.example.com/api/graphql",
		"https://ghe.example.com/api/v3/": "https://ghe.example.com/api/graphql",
		"http://127.0.0.1:8080":           "http://127.0.0.1:8080/graphql",
	}
	for base, want := range cases {
		// Enterprise serves GraphQL as a sibling of the REST root, not a child
		// of it, which a naive suffix would get wrong.
		if got := GraphQLEndpoint(base); got != want {
			t.Errorf("GraphQLEndpoint(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestBatchNumbersSplitsEvenlyAndKeepsTheRemainder(t *testing.T) {
	batches := batchNumbers([]int{1, 2, 3, 4, 5, 6, 7}, 3)
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3", len(batches))
	}
	if len(batches[2]) != 1 || batches[2][0] != 7 {
		t.Errorf("remainder batch = %v, want [7]", batches[2])
	}
}

// A credential that may not use the GraphQL API says so on every request. That
// is a reason to go back to REST, not a reason to fail the retrospective — the
// REST path worked before the batch existed and still works.
func TestAForbiddenGraphQLCredentialFallsBackInsteadOfFailing(t *testing.T) {
	for name, mode := range map[string]graphQLMode{
		"errors in a 200": graphQLForbidden,
		"an HTTP 403":     graphQLHTTPForbidden,
	} {
		t.Run(name, func(t *testing.T) {
			var log bytes.Buffer
			host, seen := fakeGitHubIn(t, mode)
			WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))(host)

			events, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
				domain.ReviewerPolicy{ExcludeBots: true},
				[]domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow)
			if err != nil {
				t.Fatalf("LinkedEvents failed rather than falling back: %v", err)
			}
			if len(events) == 0 {
				t.Fatal("fell back to nothing")
			}

			refetched := false
			for _, path := range seen.snapshot() {
				if strings.HasSuffix(path, "/timeline") {
					refetched = true
				}
			}
			if !refetched {
				t.Error("never used the REST path after GraphQL refused")
			}
			if !strings.Contains(log.String(), "cannot use the batched GraphQL query") {
				t.Error("degraded silently; nothing would explain a hundred extra requests per build")
			}
		})
	}
}

// Having learned the credential cannot use it, the adapter should stop asking.
func TestARefusedCredentialIsNotRetriedOnEveryBuild(t *testing.T) {
	var log bytes.Buffer
	host, seen := fakeGitHubIn(t, graphQLForbidden)
	WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))(host)

	for i := 0; i < 3; i++ {
		if _, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
			domain.ReviewerPolicy{ExcludeBots: true},
			[]domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow); err != nil {
			t.Fatalf("LinkedEvents: %v", err)
		}
	}

	queries := 0
	for _, path := range seen.snapshot() {
		if path == "/graphql" {
			queries++
		}
	}
	if queries != 1 {
		t.Errorf("asked GraphQL %d times after being refused, want 1", queries)
	}
	if strings.Count(log.String(), "cannot use the batched GraphQL query") != 1 {
		t.Error("warned more than once about a fact it already knew")
	}
}

// GraphQL answers partially by design. One pull request denied by path must not
// discard the other nine, and must not look like a broken deployment.
func TestOnePullRequestDeniedByPathFallsBackForThatPullRequestOnly(t *testing.T) {
	var log bytes.Buffer
	host, seen := fakeGitHubIn(t, graphQLForbidsOnePull)
	WithLogger(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))(host)

	events, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true},
		[]domain.IssueKey{"PROJ-10", "PROJ-11"}, testGitHubWindow)
	if err != nil {
		t.Fatalf("one inaccessible pull request failed the whole batch: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got events for %d keys, want 2", len(events))
	}

	// Exactly one pull request refetched, not both.
	timelines := 0
	for _, path := range seen.snapshot() {
		if strings.HasSuffix(path, "/timeline") {
			timelines++
		}
	}
	if timelines != 1 {
		t.Errorf("refetched %d timelines, want 1 — only the denied pull request needed it", timelines)
	}
	if strings.Contains(log.String(), "cannot use the batched GraphQL query") {
		t.Error("one denied pull request was mistaken for a credential that cannot use GraphQL at all")
	}
}
