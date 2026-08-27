package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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

	// Everything the sprint contains is fetched, whatever scope was asked for,
	// and the scope is applied at the end. Filtering first would save a little
	// on the request that follows and cost a full rebuild every time the reader
	// flips the toggle — which they do constantly, because the comparison
	// between the two is the point.
	if len(parents) == 0 {
		return domain.Retrospective{Sprint: sprint, Groups: nil, Axis: axis}, nil
	}

	subTasks, err := r.subTasksOf(ctx, trace, keysOf(parents))
	if err != nil {
		return domain.Retrospective{}, err
	}
	// No early return on an empty sub-task list. A sprint with no sub-tasks at
	// all is not an empty sprint — it is the whole shape of a team that does
	// not break work down, and every one of its work items is charted directly.

	// The parents' own history is asked for alongside the sub-tasks'. The
	// burndown needs to know when each work item was finished, and the tracker
	// answers for a hundred keys at a time — so this is one query either way,
	// and asking twice would be the only way to make it cost anything.
	history, codeEvents, err := r.facts(ctx, trace, project,
		append(keysOf(parents), subTaskKeysOf(subTasks)...), sprint.Window())
	if err != nil {
		return domain.Retrospective{}, err
	}

	inScope := selectScope(parents, sprint, location, req.Scope)

	retrospective := r.assemble(sprint, location, project.Settings, inScope, subTasks, history, codeEvents)
	retrospective.Burndown = domain.BuildBurndown(
		burndownItems(inScope, history), sprint, project.Settings.Schedule(), location)
	return retrospective, nil
}

// burndownItems turns the selected work items into what the burndown needs.
//
// It runs on the scope selection rather than on the assembled groups, so the
// two views answer the same question about scope while disagreeing about rows:
// the timeline drops a work item whose rows all fell outside the window, and
// the burndown must not — its points were still committed.
func burndownItems(parents []domain.WorkItem, history map[domain.IssueKey][]domain.StatusChange) []domain.BurndownItem {
	items := make([]domain.BurndownItem, 0, len(parents))
	for _, parent := range parents {
		items = append(items, domain.BurndownItem{
			Key:     parent.Key,
			Points:  parent.Points,
			Status:  parent.Status,
			Changes: history[parent.Key],
		})
	}
	return items
}

// facts gathers the planning history and the delivery events together.
//
// The two depend on the same sub-task keys and on nothing else from each other,
// so running them one after the other simply added their durations. They are
// also the two most expensive phases in a build, which makes this the one place
// where overlapping is worth the concurrency.
//
// A plain WaitGroup rather than errgroup: the module depends on nothing beyond
// yaml.v3, and two goroutines do not justify breaking that.
func (r *Retrospective) facts(
	ctx context.Context,
	trace Trace,
	project domain.Project,
	subTaskKeys []domain.IssueKey,
	window domain.Window,
) (map[domain.IssueKey][]domain.StatusChange, map[domain.IssueKey][]domain.Event, error) {
	// Cancelling on the first failure stops the other branch paying for a
	// result that is already being discarded.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		history    map[domain.IssueKey][]domain.StatusChange
		codeEvents map[domain.IssueKey][]domain.Event
		historyErr error
		codeErr    error
		wg         sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		history, historyErr = r.statusHistory(ctx, trace, subTaskKeys)
		if historyErr != nil {
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		codeEvents, codeErr = r.linkedEvents(ctx, trace, project, subTaskKeys, window)
		if codeErr != nil {
			cancel()
		}
	}()
	wg.Wait()

	// Whichever branch failed first cancelled the other, so the survivor's
	// error is usually nothing but that cancellation. Reporting it would send
	// the reader looking at the wrong port entirely, so a real failure always
	// wins over a cancellation — in either direction.
	switch {
	case isReal(historyErr):
		return nil, nil, historyErr
	case isReal(codeErr):
		return nil, nil, codeErr
	case historyErr != nil:
		return nil, nil, historyErr
	case codeErr != nil:
		return nil, nil, codeErr
	}
	return history, codeEvents, nil
}

// isReal reports whether an error describes a genuine failure rather than this
// function having cancelled the work itself.
func isReal(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
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

	sprint, err := r.tracker.Sprint(ctx, tracker, id)
	if err != nil {
		return domain.Sprint{}, err
	}
	return sprint, nil
}

func (r *Retrospective) assemble(
	sprint domain.Sprint,
	location *time.Location,
	settings domain.ProjectSettings,
	parents []domain.WorkItem,
	subTasks []domain.SubTask,
	history map[domain.IssueKey][]domain.StatusChange,
	codeEvents map[domain.IssueKey][]domain.Event,
) domain.Retrospective {
	window := sprint.Window()
	byParent := map[domain.IssueKey][]domain.Row{}
	var warnings []string

	// A sub-task whose parent is not in the selected set has nowhere to be
	// charted. Skipping it here rather than letting it fall through keeps the
	// warnings describing only what the reader can actually see, and makes the
	// result independent of a tracker returning more than it was asked for.
	selected := make(map[domain.IssueKey]struct{}, len(parents))
	hasSubTasks := map[domain.IssueKey]bool{}
	for _, parent := range parents {
		selected[parent.Key] = struct{}{}
	}

	for _, subTask := range subTasks {
		if _, ok := selected[subTask.ParentKey]; !ok {
			continue
		}
		hasSubTasks[subTask.ParentKey] = true

		rows, rowWarnings := rowsFor(
			subTask.Key, subTask.Summary, domain.RowSubTask,
			history[subTask.Key], codeEvents[subTask.Key],
			initialStatus(subTask.Status, history[subTask.Key]), subTask.Created, window)

		warnings = append(warnings, rowWarnings...)
		byParent[subTask.ParentKey] = append(byParent[subTask.ParentKey], rows...)
	}

	// A work item nobody broke down is charted directly. Teams that do not use
	// sub-tasks are otherwise looking at a page of headers over empty space.
	for _, parent := range parents {
		if hasSubTasks[parent.Key] {
			continue
		}
		rows, rowWarnings := rowsFor(
			parent.Key, parent.Summary, domain.RowWorkItem,
			history[parent.Key], codeEvents[parent.Key],
			initialStatus(parent.Status, history[parent.Key]), parent.Created, window)

		warnings = append(warnings, rowWarnings...)
		byParent[parent.Key] = append(byParent[parent.Key], rows...)
	}

	groups := make([]domain.ParentGroup, 0, len(parents))
	for _, parent := range parents {
		rows := byParent[parent.Key]
		// A work item with nothing to chart is a header over empty space. Now
		// that every item yields at least one row, this fires only for one
		// whose rows were all skipped for falling outside the window — which is
		// what it was always really for.
		if len(rows) == 0 {
			continue
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].Key != rows[j].Key {
				return rows[i].Key < rows[j].Key
			}
			return rows[i].Label < rows[j].Label
		})
		groups = append(groups, domain.ParentGroup{
			Parent:  parent,
			InScope: domain.InScope(parent.DueDate, sprint, location),
			Rows:    rows,
		})
	}
	sortGroups(groups, settings.TypesLast)

	return domain.Retrospective{
		Sprint:   sprint,
		Groups:   groups,
		Warnings: warnings,
		Axis:     domain.AxisSegments(window, settings.Schedule(), location),
	}
}

// rowsFor turns one issue's facts into the rows that chart it — usually one,
// and one per branch when the issue was worked on across several.
func rowsFor(
	key domain.IssueKey,
	summary string,
	kind domain.RowKind,
	changes []domain.StatusChange,
	code []domain.Event,
	initial domain.IssueStatus,
	created time.Time,
	window domain.Window,
) ([]domain.Row, []string) {
	var warnings []string

	status := statusEvents(changes)
	strands := domain.SplitByBranch(code)

	// Strands are per branch, and an issue with no code at all has none. The
	// unnamed strand — activity that named no branch — is not a branch of its
	// own, so it does not on its own make an issue multi-branch.
	named := 0
	for _, strand := range strands {
		if strand.Branch != "" {
			named++
		}
	}

	if len(code) == 0 {
		warnings = append(warnings, fmt.Sprintf("%s: no linked branch or pull request found", key))
	}

	if named <= 1 {
		intervals := domain.BuildTimeline(append(status, code...), initial, created, window)
		if len(intervals) == 0 {
			// Created after the sprint closed: charting nothing is correct, but
			// say so rather than leaving a gap the reader must explain.
			return nil, append(warnings,
				fmt.Sprintf("%s: created after the sprint ended, so it has no timeline", key))
		}
		return []domain.Row{{Kind: kind, Key: key, Label: summary, Intervals: intervals}}, warnings
	}

	// Several branches for one issue. Charting them as one bar would mean a bar
	// holding two review states at once, so each gets a row — and the team gets
	// told, because the split makes the chart honest but the branches are still
	// a discipline problem.
	branches := make([]string, 0, named)
	rows := make([]domain.Row, 0, named)
	for _, strand := range strands {
		if strand.Branch == "" {
			continue
		}
		branches = append(branches, strand.Branch)

		// Status changes belong to the issue rather than to any branch, so they
		// are replayed into every strand: a Blocked or Done set on the tracker
		// applies to all of the item's rows.
		intervals := domain.BuildTimeline(append(status, strand.Events...), initial, created, window)
		if len(intervals) == 0 {
			continue
		}
		rows = append(rows, domain.Row{
			Kind: domain.RowBranch, Key: key, Label: strand.Branch, Intervals: intervals,
		})
	}

	if len(rows) == 0 {
		return nil, append(warnings,
			fmt.Sprintf("%s: created after the sprint ended, so it has no timeline", key))
	}
	return rows, append(warnings, fmt.Sprintf(
		"%s: %d branches for one work item, charted one row each (%s)",
		key, len(branches), strings.Join(branches, ", ")))
}

// sortGroups moves the configured issue types to the bottom, keeping everything
// else in the tracker's own order.
//
// Which types belong at the bottom is a fact about a team's process — support
// queues, spikes, chores — and not something this code could know, so it comes
// from the project's settings and the comparison is by name.
func sortGroups(groups []domain.ParentGroup, typesLast []string) {
	if len(typesLast) == 0 {
		return
	}
	sink := make(map[string]int, len(typesLast))
	for i, name := range typesLast {
		sink[strings.ToLower(strings.TrimSpace(name))] = i + 1
	}
	rank := func(group domain.ParentGroup) int {
		return sink[strings.ToLower(strings.TrimSpace(group.Parent.Type))]
	}
	sort.SliceStable(groups, func(i, j int) bool { return rank(groups[i]) < rank(groups[j]) })
}

// initialStatus recovers the status an issue held before its first recorded
// change. With no history at all, the current status is the only evidence
// available and has to stand for the whole window.
func initialStatus(current domain.IssueStatus, changes []domain.StatusChange) domain.IssueStatus {
	if len(changes) == 0 {
		return current
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
