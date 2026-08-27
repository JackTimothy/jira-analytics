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
	if result.Groups[0].Parent.Key != "PROJ-1" || len(result.Groups[0].Rows) != 2 {
		t.Errorf("first group = %s with %d sub-tasks", result.Groups[0].Parent.Key, len(result.Groups[0].Rows))
	}
	// Sub-tasks are ordered by key, not by the tracker's arbitrary order.
	if result.Groups[0].Rows[0].Key != "PROJ-10" {
		t.Errorf("sub-tasks not ordered by key: %s first", result.Groups[0].Rows[0].Key)
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
	var api domain.Row
	for _, timeline := range result.Groups[0].Rows {
		if timeline.Key == "PROJ-11" {
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

	for _, timeline := range result.Groups[0].Rows {
		if timeline.Key == "PROJ-99" {
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

	intervals := result.Groups[0].Rows[0].Intervals
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

// A sprint with no sub-tasks anywhere is not an empty sprint. It is the whole
// shape of a team that does not break work down, and every work item in it is
// charted directly — the case this used to return nothing at all for.
func TestBuildChartsASprintThatHasNoSubTasksAtAll(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.subTasks = nil

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Groups) != len(tracker.parents) {
		t.Fatalf("got %d groups, want one per work item", len(result.Groups))
	}
	for _, group := range result.Groups {
		if len(group.Rows) != 1 || group.Rows[0].Kind != domain.RowWorkItem {
			t.Errorf("%s has %+v, want one work-item row", group.Parent.Key, group.Rows)
		}
	}
	// The frame still comes back so the chart can render its axis.
	if len(result.Axis) == 0 {
		t.Error("expected the axis even with nothing to chart")
	}
}

// A work item nobody broke down is charted in its own right, which is the whole
// sprint for a team that does not use sub-tasks. One that does have sub-tasks is
// still a header over them, and the two can sit in the same chart.
func TestBuildChartsWorkItemsWithNoSubTasksAsTheirOwnRows(t *testing.T) {
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
	if len(result.Groups) != 2 {
		t.Fatalf("got %d groups, want both work items charted", len(result.Groups))
	}

	byKey := map[domain.IssueKey]domain.ParentGroup{}
	for _, group := range result.Groups {
		byKey[group.Parent.Key] = group
	}

	leaf := byKey["PROJ-1"].Rows
	if len(leaf) != 1 || leaf[0].Kind != domain.RowWorkItem {
		t.Errorf("PROJ-1 has %d rows %+v, want one charting the work item itself", len(leaf), leaf)
	}
	if leaf[0].Key != "PROJ-1" || leaf[0].Label != "Committed story" {
		t.Errorf("the work item row is labelled %s/%q", leaf[0].Key, leaf[0].Label)
	}

	nested := byKey["PROJ-2"].Rows
	if len(nested) != 1 || nested[0].Kind != domain.RowSubTask || nested[0].Key != "PROJ-20" {
		t.Errorf("PROJ-2 has %+v, want its sub-task row", nested)
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
	// PROJ-1's only sub-task was skipped, so it has nothing to chart. PROJ-2
	// has no sub-tasks at all and is charted in its own right, so the guard has
	// to be about surviving rows rather than about reported sub-tasks.
	for _, group := range result.Groups {
		if group.Parent.Key == "PROJ-1" {
			t.Errorf("PROJ-1 was charted with %+v, want it excluded", group.Rows)
		}
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
		for _, timeline := range group.Rows {
			if timeline.Key == "PROJ-90" {
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

// The undisciplined case: several branches on one work item. Charting them as
// one bar would mean a bar holding two review states at once, so each gets a
// row — and the team gets told, because the split makes the chart honest but
// the branches are still a problem.
func TestBuildSplitsAWorkItemAcrossItsBranches(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.subTasks = nil
	tracker.parents = tracker.parents[:1] // PROJ-1 only
	code.events = map[domain.IssueKey][]domain.Event{
		"PROJ-1": {
			domain.BranchFirstSeen{At: ts(5, 9), Name: "org/repo:PROJ-1-api"},
			domain.PROpened{At: ts(6, 9), PR: domain.PRKey{Repo: "org/repo", Number: 1}, Branch: "org/repo:PROJ-1-api"},
			domain.BranchFirstSeen{At: ts(7, 9), Name: "org/repo:PROJ-1-ui"},
			domain.PROpened{At: ts(8, 9), PR: domain.PRKey{Repo: "org/repo", Number: 2}, Branch: "org/repo:PROJ-1-ui"},
			domain.PRMerged{At: ts(9, 9), PR: domain.PRKey{Repo: "org/repo", Number: 1}},
		},
	}

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(result.Groups))
	}

	rows := result.Groups[0].Rows
	if len(rows) != 2 {
		t.Fatalf("got %d rows %+v, want one per branch", len(rows), rows)
	}
	for _, row := range rows {
		if row.Kind != domain.RowBranch {
			t.Errorf("row %s is %s, want a branch row", row.Label, row.Kind)
		}
		if row.Key != "PROJ-1" {
			t.Errorf("row %s belongs to %s, want the work item", row.Label, row.Key)
		}
	}
	if rows[0].Label != "org/repo:PROJ-1-api" || rows[1].Label != "org/repo:PROJ-1-ui" {
		t.Errorf("rows are labelled %q and %q, want the branch names in order", rows[0].Label, rows[1].Label)
	}

	var warned bool
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "PROJ-1") && strings.Contains(warning, "2 branches") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("nothing warned about the split: %v", result.Warnings)
	}
}

// The ordinary case must not change: one branch is one row, whatever kind.
func TestBuildLeavesASingleBranchAsOneRow(t *testing.T) {
	projects, tracker, code := buildFixture()

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, group := range result.Groups {
		for _, row := range group.Rows {
			if row.Kind == domain.RowBranch {
				t.Errorf("%s was split across branches when it has one: %s", group.Parent.Key, row.Label)
			}
		}
	}
}

// Which issue types belong at the bottom is a fact about a team's process, so
// it comes from the project rather than from a rule in the code.
func TestBuildSortsConfiguredIssueTypesLast(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.subTasks = nil
	tracker.parents = []domain.WorkItem{
		{Key: "PROJ-1", Summary: "Support one", Type: "Support", Created: ts(1, 9)},
		{Key: "PROJ-2", Summary: "A story", Type: "Story", Created: ts(1, 9)},
		{Key: "PROJ-3", Summary: "Support two", Type: "Support", Created: ts(1, 9)},
		{Key: "PROJ-4", Summary: "A bug", Type: "Bug", Created: ts(1, 9)},
	}
	projects.project.Settings.TypesLast = []string{"support"} // matched case-insensitively

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var order []domain.IssueKey
	for _, group := range result.Groups {
		order = append(order, group.Parent.Key)
	}
	want := []domain.IssueKey{"PROJ-2", "PROJ-4", "PROJ-1", "PROJ-3"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("order = %v, want the Support items last and everything else in tracker order: %v", order, want)
		}
	}
}

func TestBuildLeavesOrderAloneWithNoConfiguredTypes(t *testing.T) {
	projects, tracker, code := buildFixture()
	tracker.subTasks = nil
	tracker.parents = []domain.WorkItem{
		{Key: "PROJ-1", Summary: "Support", Type: "Support", Created: ts(1, 9)},
		{Key: "PROJ-2", Summary: "A story", Type: "Story", Created: ts(1, 9)},
	}

	result, err := NewRetrospective(projects, tracker, code).
		Build(context.Background(), RetrospectiveRequest{ProjectID: "activation", SprintID: "100", Scope: domain.ScopeAll})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Groups[0].Parent.Key != "PROJ-1" {
		t.Errorf("order changed with no typesLast configured: %s first", result.Groups[0].Parent.Key)
	}
}
