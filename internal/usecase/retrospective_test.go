package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

func buildFixture() (fakeProjects, fakeTracker, fakeCodeHost) {
	projects := fakeProjects{project: testProject()}

	tracker := fakeTracker{
		sprints: []domain.Sprint{testSprint},
		parents: []domain.WorkItem{
			// Committed: due date equals the sprint end.
			{Key: "PROJ-1", Summary: "Committed story", DueDate: dueOn(2026, 8, 17), Created: ts(1, 9)},
			// Pulled in opportunistically: no due date at all.
			{Key: "PROJ-2", Summary: "Pulled-in story", Created: ts(4, 9)},
		},
		subTasks: []domain.SubTask{
			{Key: "PROJ-11", ParentKey: "PROJ-1", Summary: "API", Created: ts(1, 9), Status: statusDone},
			{Key: "PROJ-10", ParentKey: "PROJ-1", Summary: "UI", Created: ts(1, 9), Status: statusInProgress},
			{Key: "PROJ-20", ParentKey: "PROJ-2", Summary: "Spike", Created: ts(4, 9), Status: statusToDo},
		},
		history: map[domain.IssueKey][]domain.StatusChange{
			"PROJ-11": {{At: ts(5, 9), From: statusToDo, To: statusInProgress}},
			"PROJ-10": {{At: ts(6, 9), From: statusToDo, To: statusInProgress}},
		},
	}

	code := fakeCodeHost{events: map[domain.IssueKey][]domain.Event{
		"PROJ-11": {
			domain.BranchFirstSeen{At: ts(5, 9), Name: "PROJ-11-api"},
			domain.PROpened{At: ts(7, 9), PR: domain.PRKey{Repo: "org/repo", Number: 1}},
			domain.PRMerged{At: ts(9, 9), PR: domain.PRKey{Repo: "org/repo", Number: 1}},
		},
		"PROJ-10": {
			domain.BranchFirstSeen{At: ts(6, 9), Name: "PROJ-10-ui"},
		},
	}}

	return projects, tracker, code
}

func TestBuildGroupsSubTasksByParent(t *testing.T) {
	projects, tracker, code := buildFixture()
	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(result.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(result.Groups))
	}
	if result.Groups[0].Parent.Key != "PROJ-1" || len(result.Groups[0].SubTasks) != 2 {
		t.Errorf("first group = %s with %d sub-tasks", result.Groups[0].Parent.Key, len(result.Groups[0].SubTasks))
	}
	// Sub-tasks are ordered by key, not by the tracker's arbitrary order.
	if result.Groups[0].SubTasks[0].SubTask.Key != "PROJ-10" {
		t.Errorf("sub-tasks not ordered by key: %s first", result.Groups[0].SubTasks[0].SubTask.Key)
	}
	if !result.Groups[0].InScope {
		t.Error("PROJ-1 due on the sprint end should be in scope")
	}
	if result.Groups[1].InScope {
		t.Error("PROJ-2 with no due date should be out of scope")
	}
}

func TestBuildCommittedScopeExcludesUncommittedParents(t *testing.T) {
	projects, tracker, code := buildFixture()
	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeCommitted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(result.Groups) != 1 {
		t.Fatalf("got %d groups, want only the committed one", len(result.Groups))
	}
	if result.Groups[0].Parent.Key != "PROJ-1" {
		t.Errorf("kept %s, want PROJ-1", result.Groups[0].Parent.Key)
	}
}

func TestBuildIncludesCarryoverAndEmergencyPullForwardInCommittedScope(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.parents = []domain.WorkItem{
		{Key: "PROJ-1", Summary: "Carryover", DueDate: dueOn(2026, 8, 3), Created: ts(1, 9)},
		{Key: "PROJ-2", Summary: "Emergency", DueDate: dueOn(2026, 8, 10), Created: ts(1, 9)},
		{Key: "PROJ-3", Summary: "Next sprint's work", DueDate: dueOn(2026, 8, 31), Created: ts(1, 9)},
	}

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeCommitted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var kept []string
	for _, group := range result.Groups {
		kept = append(kept, string(group.Parent.Key))
	}
	if len(kept) != 2 || kept[0] != "PROJ-1" || kept[1] != "PROJ-2" {
		t.Errorf("committed scope = %v, want carryover and emergency only", kept)
	}
}

func TestBuildDerivesTimelineFromCombinedTrackerAndCodeEvents(t *testing.T) {
	projects, tracker, code := buildFixture()
	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// PROJ-11: To Do until its branch appears, In Progress until merge, Done after.
	var api domain.SubTaskTimeline
	for _, timeline := range result.Groups[0].SubTasks {
		if timeline.SubTask.Key == "PROJ-11" {
			api = timeline
		}
	}
	if len(api.Intervals) != 3 {
		t.Fatalf("got %d intervals for PROJ-11, want 3: %+v", len(api.Intervals), api.Intervals)
	}
	want := []domain.State{domain.StateToDo, domain.StateInProgress, domain.StateDone}
	for i, state := range want {
		if api.Intervals[i].State != state {
			t.Errorf("interval %d = %s, want %s", i, api.Intervals[i].State, state)
		}
	}
	if !api.Intervals[2].From.Equal(ts(9, 9)) {
		t.Errorf("Done should start at the merge instant, got %s", api.Intervals[2].From)
	}
}

func TestBuildWarnsAboutSubTasksWithNoLinkedCode(t *testing.T) {
	projects, tracker, code := buildFixture()
	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// PROJ-20 has no entry in the code host's result at all.
	var found bool
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "PROJ-20") && strings.Contains(warning, "no linked branch") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning about PROJ-20, got %v", result.Warnings)
	}
}

func TestBuildWarnsAboutSubTasksCreatedAfterTheSprint(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.subTasks = append(tracker.subTasks, domain.SubTask{
		Key: "PROJ-99", ParentKey: "PROJ-1", Summary: "Late", Created: ts(25, 9), Status: statusToDo,
	})

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, timeline := range result.Groups[0].SubTasks {
		if timeline.SubTask.Key == "PROJ-99" {
			t.Fatal("a sub-task created after the sprint should not be charted")
		}
	}
	var found bool
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "PROJ-99") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning about PROJ-99, got %v", result.Warnings)
	}
}

func TestBuildUsesTheEarliestChangeToRecoverTheOpeningStatus(t *testing.T) {
	projects, tracker, code := buildFixture()
	// History arrives out of order and the sub-task's current status is Done;
	// the timeline must still open from the pre-change status, not the current
	// one, or every row would begin as already finished.
	tracker.history = map[domain.IssueKey][]domain.StatusChange{
		"PROJ-11": {
			{At: ts(9, 10), From: statusInProgress, To: statusDone},
			{At: ts(5, 9), From: statusToDo, To: statusInProgress},
		},
	}
	code.events = map[domain.IssueKey][]domain.Event{"PROJ-11": {}}
	tracker.subTasks = []domain.SubTask{
		{Key: "PROJ-11", ParentKey: "PROJ-1", Summary: "API", Created: ts(1, 9), Status: statusDone},
	}

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	intervals := result.Groups[0].SubTasks[0].Intervals
	if intervals[0].State != domain.StateToDo {
		t.Errorf("first interval = %s, want TO_DO", intervals[0].State)
	}
}

func TestBuildRejectsUnknownSprint(t *testing.T) {
	projects, tracker, code := buildFixture()
	_, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "999", Scope: domain.ScopeAll})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestBuildRejectsUnknownProject(t *testing.T) {
	projects, tracker, code := buildFixture()
	_, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "nope", SprintID: "100", Scope: domain.ScopeAll})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestBuildRejectsAnInvalidProjectTimezone(t *testing.T) {
	projects, tracker, code := buildFixture()
	project := testProject()
	project.Settings.Timezone = "Nowhere/Land"
	projects.project = project

	_, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if !errors.Is(err, domain.ErrInvalidSettings) {
		t.Errorf("got %v, want ErrInvalidSettings", err)
	}
}

func TestBuildExcludesParentsWithNothingToChart(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.subTasks = nil

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Groups) != 0 {
		t.Fatalf("got %d groups, want none — a parent with no rows is a header over empty space", len(result.Groups))
	}
	// The frame still comes back so the chart can render its axis.
	if len(result.Axis) == 0 {
		t.Error("expected the axis even with nothing to chart")
	}
}

func TestBuildKeepsParentsThatDoHaveRows(t *testing.T) {
	projects, tracker, code := buildFixture()
	// PROJ-2 keeps its sub-task; PROJ-1 loses both of its.
	tracker.subTasks = []domain.SubTask{
		{Key: "PROJ-20", ParentKey: "PROJ-2", Summary: "Spike", Created: ts(4, 9), Status: statusToDo},
	}

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("got %d groups, want only the parent with rows", len(result.Groups))
	}
	if result.Groups[0].Parent.Key != "PROJ-2" {
		t.Errorf("kept %s, want PROJ-2", result.Groups[0].Parent.Key)
	}
}

func TestBuildExcludesAParentWhoseOnlySubTaskWasSkipped(t *testing.T) {
	// A sub-task created after the sprint closed produces no timeline, so its
	// parent has nothing to chart even though the tracker reported a sub-task.
	projects, tracker, code := buildFixture()
	tracker.subTasks = []domain.SubTask{
		{Key: "PROJ-99", ParentKey: "PROJ-1", Summary: "Late", Created: ts(25, 9), Status: statusToDo},
	}

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Groups) != 0 {
		t.Fatalf("got %d groups, want none", len(result.Groups))
	}
	// The warning still stands: the reader should know why it is absent.
	var warned bool
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "PROJ-99") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected a warning explaining the omission, got %v", result.Warnings)
	}
}

func TestBuildIgnoresSubTasksWhoseParentIsNotInTheSelectedScope(t *testing.T) {
	projects, tracker, code := buildFixture()
	// A tracker that returns more than it was asked for must not produce rows
	// with nowhere to go, nor warnings about work the reader cannot see.
	tracker.subTasks = append(tracker.subTasks, domain.SubTask{
		Key: "PROJ-90", ParentKey: "PROJ-UNRELATED", Summary: "Someone else's work",
		Created: ts(4, 9), Status: statusToDo,
	})

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, group := range result.Groups {
		for _, timeline := range group.SubTasks {
			if timeline.SubTask.Key == "PROJ-90" {
				t.Fatal("a sub-task from an unselected parent was charted")
			}
		}
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "PROJ-90") {
			t.Errorf("warned about a sub-task the reader cannot see: %q", warning)
		}
	}
}

func TestBuildCommittedScopeDropsWarningsForFilteredOutWork(t *testing.T) {
	projects, tracker, code := buildFixture()
	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeCommitted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// PROJ-20 belongs to the uncommitted parent, so neither its row nor its
	// warning belongs in the committed view.
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "PROJ-20") {
			t.Errorf("committed scope warned about out-of-scope work: %q", warning)
		}
	}
}

func TestBuildIncludesTheCompressedAxis(t *testing.T) {
	projects, tracker, code := buildFixture()
	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(result.Axis) == 0 {
		t.Fatal("the retrospective carries no axis segments")
	}
	if !result.Axis[0].From.Equal(testSprint.Start) {
		t.Errorf("axis starts at %s, want the sprint start", result.Axis[0].From)
	}
	if !result.Axis[len(result.Axis)-1].To.Equal(testSprint.End) {
		t.Errorf("axis ends at %s, want the sprint end", result.Axis[len(result.Axis)-1].To)
	}
	// The sprint spans a weekend, so both kinds must appear.
	kinds := map[domain.AxisSegmentKind]bool{}
	for _, s := range result.Axis {
		kinds[s.Kind] = true
	}
	if !kinds[domain.SegmentWorking] || !kinds[domain.SegmentOffHours] {
		t.Errorf("axis has kinds %v, want both working and off-hours", kinds)
	}
}
