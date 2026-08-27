package usecase

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// gatedTracker blocks in StatusHistory until released, and reports whether the
// code host was already running by then.
type gatedTracker struct {
	fakeTracker
	entered chan struct{}
	release chan struct{}
	err     error
}

func (g *gatedTracker) StatusHistory(ctx context.Context, keys []domain.IssueKey) (map[domain.IssueKey][]domain.StatusChange, error) {
	close(g.entered)
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(2 * time.Second):
	}
	if g.err != nil {
		return nil, g.err
	}
	return g.fakeTracker.history, nil
}

type gatedCodeHost struct {
	entered chan struct{}
	events  map[domain.IssueKey][]domain.Event
	err     error
	sawCtx  atomic.Bool
}

func (g *gatedCodeHost) LinkedEvents(ctx context.Context, _ []domain.RepoRef, _ domain.ReviewerPolicy, _ []domain.IssueKey, _ domain.Window) (map[domain.IssueKey][]domain.Event, error) {
	close(g.entered)
	if g.err != nil {
		return nil, g.err
	}
	// Stay alive long enough that a cancelled sibling is observable.
	select {
	case <-ctx.Done():
		g.sawCtx.Store(true)
		return nil, ctx.Err()
	case <-time.After(50 * time.Millisecond):
	}
	return g.events, nil
}

// The two most expensive phases of a build depend on the same keys and on
// nothing else from each other. Run in sequence they simply added up.
func TestBuildRunsTheTrackerAndCodeHostConcurrently(t *testing.T) {
	projects, tracker, _ := buildFixture()

	gated := &gatedTracker{fakeTracker: tracker, entered: make(chan struct{}), release: make(chan struct{})}
	code := &gatedCodeHost{entered: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		_, err := NewRetrospective(projects, gated, code).
			Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
		done <- err
	}()

	// The tracker is parked inside StatusHistory. If the code host only ran
	// afterwards, this would time out.
	<-gated.entered
	select {
	case <-code.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the code host never started while the tracker was still fetching — the phases are still sequential")
	}
	close(gated.release)

	if err := <-done; err != nil {
		t.Fatalf("Build: %v", err)
	}
}

var errTracker = errors.New("tracker exploded")
var errCode = errors.New("code host exploded")

func TestBuildSurfacesATrackerFailureFromTheConcurrentFetch(t *testing.T) {
	projects, tracker, _ := buildFixture()

	gated := &gatedTracker{fakeTracker: tracker, entered: make(chan struct{}), release: make(chan struct{}), err: errTracker}
	close(gated.release)
	code := &gatedCodeHost{entered: make(chan struct{})}

	_, err := NewRetrospective(projects, gated, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if !errors.Is(err, errTracker) {
		t.Fatalf("got %v, want the tracker's error — a failure in one branch must not be swallowed by its sibling", err)
	}
}

// The tracker is deliberately left parked, so it is still in flight when the
// code host fails and is cancelled out from under it. Its error is then nothing
// but that cancellation, and reporting it instead of the code host's would send
// the reader to the wrong port. Releasing the tracker first would let it finish
// cleanly and quietly stop testing the case that matters.
func TestBuildSurfacesACodeHostFailureFromTheConcurrentFetch(t *testing.T) {
	projects, tracker, _ := buildFixture()

	gated := &gatedTracker{fakeTracker: tracker, entered: make(chan struct{}), release: make(chan struct{})}
	code := &gatedCodeHost{entered: make(chan struct{}), err: errCode}

	_, err := NewRetrospective(projects, gated, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if !errors.Is(err, errCode) {
		t.Fatalf("got %v, want the code host's error", err)
	}
}

// A failure on one side should stop the other paying for a result that is
// already being thrown away.
func TestBuildCancelsTheSurvivingFetchWhenItsSiblingFails(t *testing.T) {
	projects, tracker, _ := buildFixture()

	gated := &gatedTracker{fakeTracker: tracker, entered: make(chan struct{}), release: make(chan struct{}), err: errTracker}
	close(gated.release)
	code := &gatedCodeHost{entered: make(chan struct{})}

	_, err := NewRetrospective(projects, gated, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if !errors.Is(err, errTracker) {
		t.Fatalf("got %v, want the tracker's error", err)
	}
	if !code.sawCtx.Load() {
		t.Error("the code host ran to completion after its sibling had already failed")
	}
}

// recordingTracker notes which keys each port was asked about, so a test can
// assert the fetch does not depend on the requested scope.
type recordingTracker struct {
	fakeTracker
	mu       sync.Mutex
	askedFor []domain.IssueKey
}

func (r *recordingTracker) SubTasksOf(ctx context.Context, parents []domain.IssueKey) ([]domain.SubTask, error) {
	r.mu.Lock()
	r.askedFor = append(r.askedFor, parents...)
	r.mu.Unlock()
	return r.fakeTracker.SubTasksOf(ctx, parents)
}

// Both scopes must fetch identically, so flipping the toggle is a re-render
// rather than a rebuild. The fixture's PROJ-2 has no due date and so is out of
// the committed scope; it must still be fetched.
func TestBuildFetchesTheWholeSprintWhateverScopeWasAskedFor(t *testing.T) {
	asked := map[domain.Scope][]domain.IssueKey{}

	for _, scope := range []domain.Scope{domain.ScopeAll, domain.ScopeCommitted} {
		projects, tracker, code := buildFixture()
		recorder := &recordingTracker{fakeTracker: tracker}

		if _, err := NewRetrospective(projects, recorder, code).
			Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: scope}); err != nil {
			t.Fatalf("Build(%s): %v", scope, err)
		}
		asked[scope] = recorder.askedFor
	}

	if len(asked[domain.ScopeAll]) != 2 {
		t.Fatalf("scope=all asked about %v, want both parents", asked[domain.ScopeAll])
	}
	if len(asked[domain.ScopeCommitted]) != len(asked[domain.ScopeAll]) {
		t.Errorf("scope=committed asked about %v but scope=all asked about %v; the toggle should not change what is fetched",
			asked[domain.ScopeCommitted], asked[domain.ScopeAll])
	}
}

// The burndown answers the same scope question as the timeline, so the toggle
// has to move both. The fixture's PROJ-2 has no due date and so is out of the
// committed scope; its points must leave the total with it.
func TestBurndownFollowsTheScopeToggle(t *testing.T) {
	totals := map[domain.Scope]float64{}

	for _, scope := range []domain.Scope{domain.ScopeAll, domain.ScopeCommitted} {
		projects, tracker, code := buildFixture()
		tracker.parents[0].Points = 5 // PROJ-1, committed
		tracker.parents[1].Points = 3 // PROJ-2, no due date

		result, err := NewRetrospective(projects, tracker, code).
			Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: scope})
		if err != nil {
			t.Fatalf("Build(%s): %v", scope, err)
		}
		totals[scope] = float64(result.Burndown.Total)
	}

	if totals[domain.ScopeAll] != 8 {
		t.Errorf("scope=all total = %v, want 8", totals[domain.ScopeAll])
	}
	if totals[domain.ScopeCommitted] != 5 {
		t.Errorf("scope=committed total = %v, want 5 — the uncommitted 3 points should leave with it",
			totals[domain.ScopeCommitted])
	}
}

// The timeline drops a work item with nothing to chart. The burndown must not:
// a story with no sub-tasks still carries points and still has to be finished.
func TestBurndownKeepsWorkItemsTheTimelineDrops(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.parents = append(tracker.parents, domain.WorkItem{
		Key: "PROJ-3", Summary: "Committed but never broken down",
		DueDate: dueOn(2026, 8, 17), Created: ts(1, 9), Points: 8,
	})
	tracker.parents[0].Points = 5

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeCommitted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, group := range result.Groups {
		if group.Parent.Key == "PROJ-3" {
			t.Error("a parent with no sub-tasks reached the timeline")
		}
	}
	if result.Burndown.Total != 13 {
		t.Errorf("burndown total = %v, want 13 — the un-broken-down story still counts",
			result.Burndown.Total)
	}
}

// Points come off when the work item's own status reaches a done category, so
// the parents' history has to be fetched, not only the sub-tasks'.
func TestBurndownBurnsOnTheWorkItemsOwnStatus(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.parents[0].Points = 5
	tracker.parents[0].Status = statusDone
	tracker.history["PROJ-1"] = []domain.StatusChange{
		{At: ts(6, 9), From: statusInProgress, To: statusDone},
	}

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeCommitted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	line := result.Burndown.Remaining
	if line[0].Remaining != 5 {
		t.Errorf("remaining at the start = %v, want 5", line[0].Remaining)
	}
	if last := line[len(line)-1].Remaining; last != 0 {
		t.Errorf("remaining at the end = %v, want 0 — PROJ-1 was finished mid-sprint", last)
	}
}
