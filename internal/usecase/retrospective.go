package usecase

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// RetrospectiveRequest names the analysis to perform.
type RetrospectiveRequest struct {
	ProjectID domain.ProjectID
	SprintID  domain.SprintID
	Scope     domain.Scope
}

// Retrospective assembles the sprint retrospective: it gathers planning facts
// from the tracker and delivery facts from the code host, replays them through
// the domain, and groups the resulting timelines by parent work item.
type Retrospective struct {
	projects ProjectStore
	tracker  IssueTracker
	code     CodeHost
	tracer   Tracer
}

// RetrospectiveOption adjusts an interactor at construction. Options rather
// than constructor parameters because a tracer is optional: nothing about the
// use case changes when nobody is measuring it.
type RetrospectiveOption func(*Retrospective)

// WithTracer reports what each build cost.
func WithTracer(tracer Tracer) RetrospectiveOption {
	return func(r *Retrospective) {
		if tracer != nil {
			r.tracer = tracer
		}
	}
}

func NewRetrospective(projects ProjectStore, tracker IssueTracker, code CodeHost, opts ...RetrospectiveOption) *Retrospective {
	retrospective := &Retrospective{projects: projects, tracker: tracker, code: code, tracer: noopTracer{}}
	for _, opt := range opts {
		opt(retrospective)
	}
	return retrospective
}

func (r *Retrospective) Build(ctx context.Context, req RetrospectiveRequest) (domain.Retrospective, error) {
	trace := r.tracer.Begin("retrospective built", map[string]string{
		"project": string(req.ProjectID),
		"sprint":  string(req.SprintID),
	})
	defer trace.End()

	project, err := r.projects.Get(ctx, req.ProjectID)
	if err != nil {
		return domain.Retrospective{}, err
	}

	location, err := project.Settings.Location()
	if err != nil {
		return domain.Retrospective{}, err
	}

	sprint, err := r.findSprint(ctx, trace, project.Tracker, req.SprintID)
	if err != nil {
		return domain.Retrospective{}, err
	}

	parents, err := r.sprintParents(ctx, trace, project.Tracker, sprint.ID)
	if err != nil {
		return domain.Retrospective{}, err
	}

	axis := domain.AxisSegments(sprint.Window(), project.Settings.Schedule(), location)

	parents = selectScope(parents, sprint, location, req.Scope)
	if len(parents) == 0 {
		return domain.Retrospective{Sprint: sprint, Groups: nil, Axis: axis}, nil
	}

	subTasks, err := r.subTasksOf(ctx, trace, keysOf(parents))
	if err != nil {
		return domain.Retrospective{}, err
	}
	if len(subTasks) == 0 {
		return domain.Retrospective{Sprint: sprint, Groups: nil, Axis: axis}, nil
	}

	subTaskKeys := subTaskKeysOf(subTasks)

	history, err := r.statusHistory(ctx, trace, subTaskKeys)
	if err != nil {
		return domain.Retrospective{}, err
	}

	codeEvents, err := r.linkedEvents(ctx, trace, project, subTaskKeys, sprint.Window())
	if err != nil {
		return domain.Retrospective{}, err
	}

	return r.assemble(sprint, location, project.Settings.Schedule(), parents, subTasks, history, codeEvents), nil
}

// The fetches below are wrapped one method each so every outward call is timed
// in exactly one place. Without that, a phase silently stops being measured the
// moment its call site moves.

func (r *Retrospective) sprintParents(ctx context.Context, trace Trace, tracker domain.TrackerRef, sprint domain.SprintID) ([]domain.WorkItem, error) {
	defer trace.Phase("parents")()

	parents, err := r.tracker.SprintParents(ctx, tracker, sprint)
	if err != nil {
		return nil, fmt.Errorf("loading sprint parents: %w", err)
	}
	return parents, nil
}

func (r *Retrospective) subTasksOf(ctx context.Context, trace Trace, parents []domain.IssueKey) ([]domain.SubTask, error) {
	defer trace.Phase("subtasks")()

	subTasks, err := r.tracker.SubTasksOf(ctx, parents)
	if err != nil {
		return nil, fmt.Errorf("loading sub-tasks: %w", err)
	}
	return subTasks, nil
}

func (r *Retrospective) statusHistory(ctx context.Context, trace Trace, keys []domain.IssueKey) (map[domain.IssueKey][]domain.StatusChange, error) {
	defer trace.Phase("history")()

	history, err := r.tracker.StatusHistory(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("loading status history: %w", err)
	}
	return history, nil
}

func (r *Retrospective) linkedEvents(ctx context.Context, trace Trace, project domain.Project, keys []domain.IssueKey, window domain.Window) (map[domain.IssueKey][]domain.Event, error) {
	defer trace.Phase("code")()

	events, err := r.code.LinkedEvents(ctx, project.Repos, project.Reviewers, keys, window)
	if err != nil {
		return nil, fmt.Errorf("loading code activity: %w", err)
	}
	return events, nil
}

func (r *Retrospective) findSprint(ctx context.Context, trace Trace, tracker domain.TrackerRef, id domain.SprintID) (domain.Sprint, error) {
	defer trace.Phase("sprint")()

	sprints, err := r.tracker.ListSprints(ctx, tracker)
	if err != nil {
		return domain.Sprint{}, fmt.Errorf("listing sprints: %w", err)
	}
	for _, sprint := range sprints {
		if sprint.ID == id {
			return sprint, nil
		}
	}
	return domain.Sprint{}, fmt.Errorf("%w: sprint %s", domain.ErrNotFound, id)
}

func (r *Retrospective) assemble(
	sprint domain.Sprint,
	location *time.Location,
	schedule domain.WorkingHours,
	parents []domain.WorkItem,
	subTasks []domain.SubTask,
	history map[domain.IssueKey][]domain.StatusChange,
	codeEvents map[domain.IssueKey][]domain.Event,
) domain.Retrospective {
	window := sprint.Window()
	byParent := map[domain.IssueKey][]domain.SubTaskTimeline{}
	var warnings []string

	// A sub-task whose parent is not in the selected set has nowhere to be
	// charted. Skipping it here rather than letting it fall through keeps the
	// warnings describing only what the reader can actually see, and makes the
	// result independent of a tracker returning more than it was asked for.
	selected := make(map[domain.IssueKey]struct{}, len(parents))
	for _, parent := range parents {
		selected[parent.Key] = struct{}{}
	}

	for _, subTask := range subTasks {
		if _, ok := selected[subTask.ParentKey]; !ok {
			continue
		}
		changes := history[subTask.Key]
		events := append(statusEvents(changes), codeEvents[subTask.Key]...)

		intervals := domain.BuildTimeline(events, initialStatus(subTask, changes), subTask.Created, window)
		if len(intervals) == 0 {
			// Created after the sprint closed: charting nothing is correct, but
			// say so rather than leaving a gap the reader must explain.
			warnings = append(warnings, fmt.Sprintf("%s: created after the sprint ended, so it has no timeline", subTask.Key))
			continue
		}
		if _, linked := codeEvents[subTask.Key]; !linked {
			// Linkage is a heuristic on branch names. A sub-task with no branch
			// may genuinely have none, or may have one the matcher missed;
			// silently charting it as never-started would hide the difference.
			warnings = append(warnings, fmt.Sprintf("%s: no linked branch or pull request found", subTask.Key))
		}

		byParent[subTask.ParentKey] = append(byParent[subTask.ParentKey], domain.SubTaskTimeline{
			SubTask:   subTask,
			Intervals: intervals,
		})
	}

	groups := make([]domain.ParentGroup, 0, len(parents))
	for _, parent := range parents {
		timelines := byParent[parent.Key]
		// A parent with nothing to chart is a header over empty space. That
		// includes parents whose only sub-tasks were skipped — created after
		// the sprint closed, say — which is why this filters on the rows that
		// survived rather than on whether the tracker reported any sub-tasks.
		if len(timelines) == 0 {
			continue
		}
		sort.SliceStable(timelines, func(i, j int) bool {
			return timelines[i].SubTask.Key < timelines[j].SubTask.Key
		})
		groups = append(groups, domain.ParentGroup{
			Parent:   parent,
			InScope:  domain.InScope(parent.DueDate, sprint, location),
			SubTasks: timelines,
		})
	}

	return domain.Retrospective{
		Sprint:   sprint,
		Groups:   groups,
		Warnings: warnings,
		Axis:     domain.AxisSegments(window, schedule, location),
	}
}

// initialStatus recovers the status a sub-task held before its first recorded
// change. With no history at all, the current status is the only evidence
// available and has to stand for the whole window.
func initialStatus(subTask domain.SubTask, changes []domain.StatusChange) domain.IssueStatus {
	if len(changes) == 0 {
		return subTask.Status
	}
	earliest := changes[0]
	for _, change := range changes[1:] {
		if change.At.Before(earliest.At) {
			earliest = change
		}
	}
	return earliest.From
}

func statusEvents(changes []domain.StatusChange) []domain.Event {
	events := make([]domain.Event, 0, len(changes))
	for _, change := range changes {
		events = append(events, domain.StatusChanged{At: change.At, To: change.To})
	}
	return events
}

func selectScope(parents []domain.WorkItem, sprint domain.Sprint, location *time.Location, scope domain.Scope) []domain.WorkItem {
	if scope != domain.ScopeCommitted {
		return parents
	}
	kept := make([]domain.WorkItem, 0, len(parents))
	for _, parent := range parents {
		if domain.InScope(parent.DueDate, sprint, location) {
			kept = append(kept, parent)
		}
	}
	return kept
}

func keysOf(parents []domain.WorkItem) []domain.IssueKey {
	keys := make([]domain.IssueKey, 0, len(parents))
	for _, parent := range parents {
		keys = append(keys, parent.Key)
	}
	return keys
}

func subTaskKeysOf(subTasks []domain.SubTask) []domain.IssueKey {
	keys := make([]domain.IssueKey, 0, len(subTasks))
	for _, subTask := range subTasks {
		keys = append(keys, subTask.Key)
	}
	return keys
}
