package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
)

var testRepo = domain.RepoRef{Owner: "org", Name: "repo"}

// fixtures trimmed from real GitHub responses.
var fixtures = map[string]string{
	"/repos/org/repo":          `{"default_branch":"dev"}`,
	"/repos/org/repo/branches": `[{"name":"dev"},{"name":"PROJ-10-add-picker"},{"name":"dependabot/npm/react-19"},{"name":"PROJ-11-api"}]`,

	"/repos/org/repo/compare/dev...PROJ-10-add-picker": `{"commits":[
		{"commit":{"committer":{"date":"2026-08-04T13:00:00Z"}}},
		{"commit":{"committer":{"date":"2026-08-05T13:00:00Z"}}}]}`,
	"/repos/org/repo/compare/dev...PROJ-11-api": `{"commits":[
		{"commit":{"committer":{"date":"2026-08-06T13:00:00Z"}}}]}`,

	"/repos/org/repo/pulls/7/reviews": `[
		{"user":{"login":"alice","type":"User"},"state":"CHANGES_REQUESTED","submitted_at":"2026-08-08T10:00:00Z"},
		{"user":{"login":"alice","type":"User"},"state":"APPROVED","submitted_at":"2026-08-09T10:00:00Z"},
		{"user":{"login":"copilot-pull-request-reviewer[bot]","type":"Bot"},"state":"APPROVED","submitted_at":"2026-08-07T10:00:00Z"}]`,
	"/repos/org/repo/issues/7/timeline": `[
		{"event":"ready_for_review","created_at":"2026-08-07T09:00:00Z"},
		{"event":"review_requested","created_at":"2026-08-07T09:05:00Z","requested_reviewer":{"login":"alice","type":"User"}},
		{"event":"merged","created_at":"2026-08-10T09:00:00Z"},
		{"event":"closed","created_at":"2026-08-10T09:00:00Z"}]`,

	"/repos/org/repo/pulls/8/reviews":   `[]`,
	"/repos/org/repo/issues/8/timeline": `[]`,
}

func newFakeGitHub(t *testing.T) (*CodeHost, *[]string) {
	t.Helper()
	var seen []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)

		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// The pull request listing is keyed by the head branch.
		if r.URL.Path == "/repos/org/repo/pulls" {
			switch r.URL.Query().Get("head") {
			case "org:PROJ-10-add-picker":
				io.WriteString(w, `[{"number":7,"title":"PROJ-10 add picker","draft":false,
					"created_at":"2026-08-06T09:00:00Z","merged_at":"2026-08-10T09:00:00Z",
					"head":{"ref":"PROJ-10-add-picker"}}]`)
			case "org:PROJ-11-api":
				io.WriteString(w, `[{"number":8,"title":"PROJ-11 api","draft":true,
					"created_at":"2026-08-07T09:00:00Z","head":{"ref":"PROJ-11-api"}}]`)
			default:
				io.WriteString(w, `[]`)
			}
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
	return host, &seen
}

func TestLinkedEventsBuildsATimelineFromBranchesPullsAndReviews(t *testing.T) {
	host, _ := newFakeGitHub(t)

	result, err := host.LinkedEvents(
		context.Background(),
		[]domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true},
		[]domain.IssueKey{"PROJ-10", "PROJ-11"},
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
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10"})
	if err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	if _, ok := result["PROJ-11"]; ok {
		t.Error("PROJ-11 was not requested but appeared in the result")
	}
	for _, path := range *seen {
		if path == "/repos/org/repo/compare/dev...dependabot/npm/react-19" {
			t.Error("a branch with no issue key was investigated")
		}
	}
}

func TestLinkedEventsExcludesBotApprovalsFromReviewStates(t *testing.T) {
	host, _ := newFakeGitHub(t)

	result, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo},
		domain.ReviewerPolicy{ExcludeBots: true}, []domain.IssueKey{"PROJ-10"})
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
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10", "PROJ-11"}); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}

	var repoCalls int
	for _, path := range *seen {
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

	if _, err := host.LinkedEvents(context.Background(), []domain.RepoRef{testRepo}, domain.ReviewerPolicy{}, nil); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}
	if _, err := host.LinkedEvents(context.Background(), nil, domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10"}); err != nil {
		t.Fatalf("LinkedEvents: %v", err)
	}
	if len(*seen) != 0 {
		t.Errorf("made %d requests with nothing to do", len(*seen))
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
		domain.ReviewerPolicy{}, []domain.IssueKey{"PROJ-10"})
	if err == nil {
		t.Fatal("expected an error rather than a silently empty result")
	}
}

func TestForEachBoundedRespectsTheLimit(t *testing.T) {
	items := make([]int, 50)
	var mu sync.Mutex
	var running, peak int

	err := forEachBounded(context.Background(), items, 4, func(context.Context, int) error {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()

		time.Sleep(time.Millisecond)

		mu.Lock()
		running--
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("forEachBounded: %v", err)
	}
	if peak > 4 {
		t.Errorf("peak concurrency was %d, want at most 4", peak)
	}
}

func TestForEachBoundedReturnsTheFirstError(t *testing.T) {
	err := forEachBounded(context.Background(), []int{1, 2, 3}, 2, func(_ context.Context, i int) error {
		if i == 2 {
			return errBoom
		}
		return nil
	})
	if err != errBoom {
		t.Errorf("got %v, want errBoom", err)
	}
}

var errBoom = errorString("boom")

type errorString string

func (e errorString) Error() string { return string(e) }
