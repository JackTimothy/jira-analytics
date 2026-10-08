package domain

import (
	"testing"
	"time"
)

// The cycle time tests reuse the burndown's sprint: Monday 3 August 08:00 to
// Friday 14 August 18:00 Eastern. august() is in UTC, four hours ahead, so
// the working day runs from august(d, 12) to august(d, 22).

func finished(key IssueKey, kind string, points Points) WorkItem {
	return WorkItem{Key: key, Type: kind, Points: points, Status: statusDone}
}

func move(at time.Time, from, to IssueStatus) StatusChange {
	return StatusChange{At: at, From: from, To: to}
}

func cycleTimesOf(t *testing.T, items []WorkItem, history map[IssueKey][]StatusChange) SprintCycleTime {
	t.Helper()
	return BuildCycleTimes(items, history, burndownSprint, DefaultWorkingHours(), eastern(t))
}

func keysOfCycles(cycles []CycleTime) []IssueKey {
	keys := make([]IssueKey, 0, len(cycles))
	for _, cycle := range cycles {
		keys = append(keys, cycle.Item.Key)
	}
	return keys
}

func assertKeys(t *testing.T, what string, got, want []IssueKey) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

// The clock runs from the first move into progress to Done, and only while
// the team is at work: Tuesday 16:00 to 18:00, then Wednesday 08:00 to 11:00.
// Review sits in the in-progress category and must not restart the clock.
func TestCycleTimeCountsWorkingHoursFromFirstInProgressToDone(t *testing.T) {
	item := finished("PROJ-1", "Story", 2)
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(4, 20), statusToDo, statusInProgress),
		move(august(5, 13), statusInProgress, statusReview),
		move(august(5, 15), statusReview, statusDone),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	assertKeys(t, "counted", keysOfCycles(got.Counted), []IssueKey{"PROJ-1"})
	cycle := got.Counted[0]
	if !cycle.Started.Equal(august(4, 20)) || !cycle.Finished.Equal(august(5, 15)) {
		t.Errorf("cycle = %s..%s, want %s..%s", cycle.Started, cycle.Finished, august(4, 20), august(5, 15))
	}
	if cycle.Working != 5*time.Hour {
		t.Errorf("working = %s, want 5h", cycle.Working)
	}
	if got.PerPoint != 150*time.Minute {
		t.Errorf("per point = %s, want 2h30m", got.PerPoint)
	}
}

// The changelog arrives unsorted from the inline search. Reading it in the
// order given would take the last-listed move into progress as the first.
func TestCycleTimeIsIndependentOfHistoryOrder(t *testing.T) {
	item := finished("PROJ-1", "Story", 2)
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(5, 15), statusReview, statusDone),
		move(august(5, 13), statusInProgress, statusReview),
		move(august(4, 20), statusToDo, statusInProgress),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	if len(got.Counted) != 1 || got.Counted[0].Working != 5*time.Hour {
		t.Fatalf("counted = %+v, want PROJ-1 at 5h", got.Counted)
	}
}

// Carried-over work began before the sprint did. Measuring from the sprint's
// start would make every carried item look quicker than it was.
func TestCycleTimeIncludesWorkDoneBeforeTheSprintOpened(t *testing.T) {
	item := finished("PROJ-1", "Task", 1)
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		// Friday 31 July 16:00 Eastern, then Monday 3 August 10:00.
		move(time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC), statusToDo, statusInProgress),
		move(august(3, 14), statusInProgress, statusDone),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	if len(got.Counted) != 1 || got.Counted[0].Working != 4*time.Hour {
		t.Fatalf("counted = %+v, want PROJ-1 at 4h", got.Counted)
	}
}

// Work that was reopened was not finished the first time. Its rework is part
// of how long it took, so the clock runs to the last Done.
func TestCycleTimeRunsToTheLastDoneWhenWorkIsReopened(t *testing.T) {
	item := finished("PROJ-1", "Bug", 1)
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(4, 12), statusToDo, statusInProgress),
		move(august(4, 14), statusInProgress, statusDone),
		move(august(4, 16), statusDone, statusInProgress),
		move(august(4, 18), statusInProgress, statusDone),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	if len(got.Counted) != 1 {
		t.Fatalf("counted = %+v, want PROJ-1", got.Counted)
	}
	if cycle := got.Counted[0]; !cycle.Finished.Equal(august(4, 18)) || cycle.Working != 6*time.Hour {
		t.Errorf("cycle finished %s after %s, want %s after 6h", cycle.Finished, cycle.Working, august(4, 18))
	}
}

// Cancelled shares the done category with Done, which is right for the
// burndown and wrong here: abandoned work has no delivery time to report.
func TestCycleTimeIgnoresCancelledWork(t *testing.T) {
	item := WorkItem{Key: "PROJ-1", Type: "Story", Points: 3, Status: statusCancelled}
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(4, 12), statusToDo, statusInProgress),
		move(august(4, 14), statusInProgress, statusCancelled),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	if len(got.Items) != 0 || len(got.Counted) != 0 || len(got.Unstarted) != 0 {
		t.Errorf("cancelled work was measured: %+v", got)
	}
}

// Only Stories, Tasks and Bugs are measured, whatever case the tracker uses.
// A support ticket's points are not comparable with a story's.
func TestCycleTimeMeasuresOnlyStoriesTasksAndBugs(t *testing.T) {
	items := []WorkItem{
		finished("PROJ-1", "Support", 1),
		finished("PROJ-2", " story ", 1),
		finished("PROJ-3", "Epic", 1),
	}
	history := map[IssueKey][]StatusChange{}
	for _, item := range items {
		history[item.Key] = []StatusChange{
			move(august(4, 12), statusToDo, statusInProgress),
			move(august(4, 14), statusInProgress, statusDone),
		}
	}

	got := cycleTimesOf(t, items, history)

	assertKeys(t, "items", keysOfCycles(got.Items), []IssueKey{"PROJ-2"})
}

// An item finished outside the sprint still has a cycle time of its own, which
// the timeline shows, but this sprint's average is not its to move. The
// boundaries follow the burndown: after the sprint opened, up to and including
// its close.
func TestCycleTimeCountsOnlyWorkFinishedDuringTheSprint(t *testing.T) {
	items := []WorkItem{
		finished("BEFORE", "Story", 1),
		finished("AT-START", "Story", 1),
		finished("AT-END", "Story", 1),
		finished("AFTER", "Story", 1),
	}
	history := map[IssueKey][]StatusChange{
		"BEFORE": {
			move(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC), statusToDo, statusInProgress),
			move(time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC), statusInProgress, statusDone),
		},
		"AT-START": {
			move(time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC), statusToDo, statusInProgress),
			move(burndownSprint.Start, statusInProgress, statusDone),
		},
		"AT-END": {
			move(august(14, 12), statusToDo, statusInProgress),
			move(burndownSprint.End, statusInProgress, statusDone),
		},
		"AFTER": {
			move(august(14, 12), statusToDo, statusInProgress),
			move(august(17, 14), statusInProgress, statusDone),
		},
	}

	got := cycleTimesOf(t, items, history)

	assertKeys(t, "items", keysOfCycles(got.Items), []IssueKey{"BEFORE", "AT-START", "AT-END", "AFTER"})
	assertKeys(t, "counted", keysOfCycles(got.Counted), []IssueKey{"AT-END"})
}

// An item with no estimate has nothing to divide by. It is measured, so its
// own cycle time still shows, and named rather than quietly left out.
func TestCycleTimeNamesFinishedWorkCarryingNoEstimate(t *testing.T) {
	item := finished("PROJ-1", "Story", 0)
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(4, 12), statusToDo, statusInProgress),
		move(august(4, 14), statusInProgress, statusDone),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	assertKeys(t, "items", keysOfCycles(got.Items), []IssueKey{"PROJ-1"})
	assertKeys(t, "counted", keysOfCycles(got.Counted), nil)
	assertKeys(t, "unestimated", got.Unestimated, []IssueKey{"PROJ-1"})
}

// Work moved straight from To Do to Done has no start to measure from.
// Counting it as zero would flatter the average, so it is named instead.
func TestCycleTimeNamesWorkNeverMarkedInProgress(t *testing.T) {
	item := finished("PROJ-1", "Story", 2)
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(4, 14), statusToDo, statusDone),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	assertKeys(t, "items", keysOfCycles(got.Items), nil)
	assertKeys(t, "unstarted", got.Unstarted, []IssueKey{"PROJ-1"})
}

// An item created straight into progress never moves into it. Its first
// change leaving progress is the evidence, and creation is the start.
func TestCycleTimeStartsAtCreationForWorkCreatedInProgress(t *testing.T) {
	item := finished("PROJ-1", "Task", 1)
	item.Created = august(4, 12)
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(4, 15), statusInProgress, statusDone),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	if len(got.Counted) != 1 || got.Counted[0].Working != 3*time.Hour {
		t.Fatalf("counted = %+v, want PROJ-1 at 3h", got.Counted)
	}
}

// Done now, but no recorded move into Done: there is no telling when it
// finished, so it is neither measured nor named.
func TestCycleTimeSkipsDoneWorkWithNoRecordedFinish(t *testing.T) {
	got := cycleTimesOf(t, []WorkItem{finished("PROJ-1", "Story", 2)}, nil)

	if len(got.Items) != 0 || len(got.Unstarted) != 0 || len(got.Unestimated) != 0 {
		t.Errorf("unmeasurable work was reported: %+v", got)
	}
}

// Work that is not Done now has no cycle time, even though it reached Done
// during the sprint before it was reopened.
func TestCycleTimeIgnoresWorkReopenedAndStillOpen(t *testing.T) {
	item := WorkItem{Key: "PROJ-1", Type: "Story", Points: 2, Status: statusInProgress}
	history := map[IssueKey][]StatusChange{"PROJ-1": {
		move(august(4, 12), statusToDo, statusInProgress),
		move(august(4, 14), statusInProgress, statusDone),
		move(august(4, 16), statusDone, statusInProgress),
	}}

	got := cycleTimesOf(t, []WorkItem{item}, history)

	if len(got.Items) != 0 {
		t.Errorf("open work was measured: %+v", got.Items)
	}
}

// The average is the mean of each item's hours per point, not total hours
// over total points. 10h over 5 points and 6h over 1 point average 4h per
// point; pooled they would be 16h over 6 points, about 2.67h. The second
// figure lets the largest item decide.
func TestCycleTimeAveragesEachItemsHoursPerPoint(t *testing.T) {
	items := []WorkItem{finished("BIG", "Story", 5), finished("SMALL", "Bug", 1)}
	history := map[IssueKey][]StatusChange{
		"BIG": {
			move(august(3, 12), statusToDo, statusInProgress),
			move(august(3, 22), statusInProgress, statusDone),
		},
		"SMALL": {
			move(august(4, 12), statusToDo, statusInProgress),
			move(august(4, 18), statusInProgress, statusDone),
		},
	}

	got := cycleTimesOf(t, items, history)

	if got.PerPoint != 4*time.Hour {
		t.Errorf("per point = %s, want 4h", got.PerPoint)
	}
}

func TestCycleTimeWithNothingFinishedIsEmptyRatherThanBroken(t *testing.T) {
	item := WorkItem{Key: "PROJ-1", Type: "Story", Points: 2, Status: statusInProgress}

	got := cycleTimesOf(t, []WorkItem{item}, nil)

	if len(got.Counted) != 0 || got.PerPoint != 0 {
		t.Errorf("got %+v, want nothing counted", got)
	}
}
