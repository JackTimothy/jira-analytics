// Package usecase holds application-specific business rules and the ports they
// depend on.
//
// The ports are declared here, by the code that consumes them, rather than
// beside the adapters that implement them. That is what keeps the dependency
// arrows pointing inward: an adapter imports this package, never the reverse,
// so swapping Jira for another tracker touches no use case.
package usecase

import (
	"context"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// IssueTracker is the source of planning facts: sprints, the work committed to
// them, and how each item's status changed over time.
type IssueTracker interface {
	// ListSprints returns the sprints of a project, most recent first.
	ListSprints(ctx context.Context, tracker domain.TrackerRef) ([]domain.Sprint, error)

	// SprintParents returns the parent work items in a sprint, each carrying
	// the due date that decides committed scope.
	SprintParents(ctx context.Context, tracker domain.TrackerRef, sprint domain.SprintID) ([]domain.WorkItem, error)

	// SubTasksOf returns the sub-tasks of the given parents.
	SubTasksOf(ctx context.Context, parents []domain.IssueKey) ([]domain.SubTask, error)

	// StatusHistory returns each issue's status transitions in chronological
	// order. Without it there is no way to know what a status was in the past.
	StatusHistory(ctx context.Context, keys []domain.IssueKey) (map[domain.IssueKey][]domain.StatusChange, error)
}

// CodeHost is the source of delivery facts: the branches and pull requests
// linked to an issue, and everything that happened to them.
type CodeHost interface {
	// LinkedEvents returns the domain events implied by code activity for each
	// issue key. Keys with no linked code are absent from the result, which is
	// how the interactor knows to warn rather than silently chart nothing.
	//
	// The window lets the implementation bound its search: a code host cannot
	// filter pull requests by issue key server-side, so it needs to know which
	// span of activity is worth looking at.
	LinkedEvents(ctx context.Context, repos []domain.RepoRef, policy domain.ReviewerPolicy, keys []domain.IssueKey, window domain.Window) (map[domain.IssueKey][]domain.Event, error)
}

// ProjectStore holds the configured projects and their user-editable settings.
type ProjectStore interface {
	List(ctx context.Context) ([]domain.Project, error)
	Get(ctx context.Context, id domain.ProjectID) (domain.Project, error)

	// UpdateSettings persists the settings a user may change at runtime. It is
	// the application's only write path; nothing is ever written back to the
	// tracker or the code host.
	UpdateSettings(ctx context.Context, id domain.ProjectID, settings domain.ProjectSettings) error
}

// Tracer observes what a use case cost. It is a port for the same reason the
// others are: which phases exist is application knowledge, while where the
// numbers should go — a log line, a metrics backend, nowhere at all — is not.
//
// It is deliberately about wall-clock phases rather than counters. The phases
// of a build overlap once the fetches run concurrently, and a report that shows
// two phases each taking most of the total is how you see that the overlap is
// working.
type Tracer interface {
	// Begin starts a trace of one operation.
	Begin(operation string, attrs map[string]string) Trace
}

// Trace collects the phases of a single operation. Implementations must be
// safe for concurrent use: phases overlap by design.
type Trace interface {
	// Phase starts a named phase and returns the function that ends it, so a
	// caller can write `defer trace.Phase("history")()`.
	Phase(name string) func()

	// End closes the trace and reports it.
	End()
}
