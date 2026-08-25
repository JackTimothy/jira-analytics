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
	// issue key. Keys with no linked branch are absent from the result, which
	// is how the interactor knows to warn rather than silently chart nothing.
	LinkedEvents(ctx context.Context, repos []domain.RepoRef, policy domain.ReviewerPolicy, keys []domain.IssueKey) (map[domain.IssueKey][]domain.Event, error)
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
