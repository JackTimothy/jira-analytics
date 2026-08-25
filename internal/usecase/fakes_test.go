package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// Fake ports. They exist so the interactor can be tested without a network,
// which is the practical payoff of declaring the ports in this package.

type fakeProjects struct {
	project domain.Project
	err     error
}

func (f fakeProjects) List(context.Context) ([]domain.Project, error) {
	return []domain.Project{f.project}, f.err
}

func (f fakeProjects) Get(_ context.Context, id domain.ProjectID) (domain.Project, error) {
	if f.err != nil {
		return domain.Project{}, f.err
	}
	if id != f.project.ID {
		return domain.Project{}, fmt.Errorf("%w: project %s", domain.ErrNotFound, id)
	}
	return f.project, nil
}

func (f fakeProjects) UpdateSettings(context.Context, domain.ProjectID, domain.ProjectSettings) error {
	return f.err
}

type fakeTracker struct {
	sprints  []domain.Sprint
	parents  []domain.WorkItem
	subTasks []domain.SubTask
	history  map[domain.IssueKey][]domain.StatusChange
	err      error
}

func (f fakeTracker) ListSprints(context.Context, domain.TrackerRef) ([]domain.Sprint, error) {
	return f.sprints, f.err
}

func (f fakeTracker) SprintParents(context.Context, domain.TrackerRef, domain.SprintID) ([]domain.WorkItem, error) {
	return f.parents, f.err
}

func (f fakeTracker) SubTasksOf(context.Context, []domain.IssueKey) ([]domain.SubTask, error) {
	return f.subTasks, f.err
}

func (f fakeTracker) StatusHistory(context.Context, []domain.IssueKey) (map[domain.IssueKey][]domain.StatusChange, error) {
	return f.history, f.err
}

type fakeCodeHost struct {
	events map[domain.IssueKey][]domain.Event
	err    error
}

func (f fakeCodeHost) LinkedEvents(context.Context, []domain.RepoRef, domain.ReviewerPolicy, []domain.IssueKey) (map[domain.IssueKey][]domain.Event, error) {
	return f.events, f.err
}

// Shared fixtures.

var (
	statusToDo       = domain.IssueStatus{Name: "To Do", Category: domain.CategoryToDo}
	statusInProgress = domain.IssueStatus{Name: "In Progress", Category: domain.CategoryInProgress}
	statusDone       = domain.IssueStatus{Name: "Done", Category: domain.CategoryDone}
)

func ts(day, hour int) time.Time {
	return time.Date(2026, 8, day, hour, 0, 0, 0, time.UTC)
}

func dueOn(year int, month time.Month, day int) *domain.CalendarDate {
	d := domain.NewCalendarDate(year, month, day)
	return &d
}

// testSprint runs 3 Aug 09:00 to 17 Aug 09:00 UTC, ending on 17 Aug Eastern.
var testSprint = domain.Sprint{ID: "100", Name: "Sprint 26-31", Start: ts(3, 9), End: ts(17, 9)}

func testProject() domain.Project {
	return domain.Project{
		ID:       "activation",
		Name:     "Activation",
		Settings: domain.ProjectSettings{Timezone: "America/New_York"},
		Tracker:  domain.TrackerRef{ProjectKey: "PROJ", BoardID: "45"},
		Repos:    []domain.RepoRef{{Owner: "org", Name: "repo"}},
	}
}
