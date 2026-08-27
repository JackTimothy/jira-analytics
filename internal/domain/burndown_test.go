package domain

import (
	"testing"
	"time"
)

var burndownSprint = Sprint{
	ID:    "100",
	Name:  "Sprint 26-33",
	Start: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC), // Monday 08:00 Eastern
	End:   time.Date(2026, 8, 14, 22, 0, 0, 0, time.UTC),
}

func burndownHours() WorkingHours { return DefaultWorkingHours() }

// august is a UTC instant inside the burndown fixture's sprint. The package
// already has an at() measured in minutes, for a different fixture.
func august(day, hour int) time.Time {
	return time.Date(2026, 8, day, hour, 0, 0, 0, time.UTC)
}

var (
	statusOpen = IssueStatus{Name: "In Progress", Category: CategoryInProgress}
	statusShut = IssueStatus{Name: "Done", Category: CategoryDone}
	statusNew  = IssueStatus{Name: "To Do", Category: CategoryToDo}
)

func remainingAt(line []BurndownPoint, when time.Time) Points {
	remaining := line[0].Remaining
	for _, point := range line {
		if point.At.After(when) {
			break
		}
		remaining = point.Remaining
	}
	return remaining
}

func TestBurndownStartsAtTheTotalAndEndsAtWhatIsLeft(t *testing.T) {
	items := []BurndownItem{
		{Key: "PROJ-1", Points: 3, Status: statusShut, Changes: []StatusChange{
			{At: august(5, 14), From: statusOpen, To: statusShut}}},
		{Key: "PROJ-2", Points: 5, Status: statusOpen},
	}

	burndown := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t))

	if burndown.Total != 8 {
		t.Errorf("total = %v, want 8", burndown.Total)
	}
	if got := burndown.Remaining[0].Remaining; got != 8 {
		t.Errorf("remaining at the start = %v, want the full 8", got)
	}
	if got := burndown.Remaining[len(burndown.Remaining)-1].Remaining; got != 5 {
		t.Errorf("remaining at the end = %v, want the 5 that never finished", got)
	}
}

// Points do not burn gradually; they drop when an item is finished. Two
// vertices at the same instant are what draws that as a step.
func TestBurndownDropsAsAStepNotADiagonal(t *testing.T) {
	finishedAt := august(5, 14)
	items := []BurndownItem{
		{Key: "PROJ-1", Points: 3, Status: statusShut, Changes: []StatusChange{
			{At: finishedAt, From: statusOpen, To: statusShut}}},
	}

	line := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t)).Remaining

	var atInstant []Points
	for _, point := range line {
		if point.At.Equal(finishedAt) {
			atInstant = append(atInstant, point.Remaining)
		}
	}
	if len(atInstant) != 2 {
		t.Fatalf("got %d vertices at the finishing instant, want 2", len(atInstant))
	}
	if atInstant[0] != 3 || atInstant[1] != 0 {
		t.Errorf("step went %v -> %v, want 3 -> 0", atInstant[0], atInstant[1])
	}
}

// An item finished, reopened and finished again should put its points back on
// the chart in between. Treating "finished" as a state over time rather than as
// one timestamp is what makes this work without a special case.
func TestBurndownPutsPointsBackWhenWorkIsReopened(t *testing.T) {
	items := []BurndownItem{
		{Key: "PROJ-1", Points: 5, Status: statusShut, Changes: []StatusChange{
			{At: august(5, 14), From: statusOpen, To: statusShut},
			{At: august(7, 14), From: statusShut, To: statusOpen},
			{At: august(11, 14), From: statusOpen, To: statusShut},
		}},
	}

	line := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t)).Remaining

	for _, tc := range []struct {
		when time.Time
		want Points
	}{
		{august(4, 14), 5},  // before it was ever finished
		{august(6, 14), 0},  // finished
		{august(8, 14), 5},  // reopened
		{august(12, 14), 0}, // finished again
	} {
		if got := remainingAt(line, tc.when); got != tc.want {
			t.Errorf("remaining at %s = %v, want %v", tc.when.Format(time.RFC3339), got, tc.want)
		}
	}
}

// Work carried over already finished before the sprint opened was never part of
// what this sprint had left to do.
func TestBurndownExcludesWorkAlreadyFinishedWhenTheSprintOpened(t *testing.T) {
	items := []BurndownItem{
		{Key: "PROJ-1", Points: 3, Status: statusShut, Changes: []StatusChange{
			{At: august(1, 14), From: statusOpen, To: statusShut}}}, // before the sprint
		{Key: "PROJ-2", Points: 5, Status: statusOpen},
	}

	burndown := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t))

	if burndown.Total != 8 {
		t.Errorf("total = %v, want 8 — the item is still in the sprint's scope", burndown.Total)
	}
	if got := burndown.Remaining[0].Remaining; got != 5 {
		t.Errorf("remaining at the start = %v, want 5 — 3 points were already done", got)
	}
}

// An item that never moved has no changes at all, so its current status is the
// only evidence of whether it was finished during the sprint.
func TestBurndownReadsAnItemThatNeverMovedFromItsCurrentStatus(t *testing.T) {
	items := []BurndownItem{{Key: "PROJ-1", Points: 3, Status: statusNew}}

	line := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t)).Remaining

	if got := line[len(line)-1].Remaining; got != 3 {
		t.Errorf("remaining = %v, want 3 — nothing ever happened to it", got)
	}
}

// An unestimated item contributes nothing to either line, so it has to be named
// somewhere or a burndown missing a third of the sprint looks complete.
func TestBurndownNamesTheItemsCarryingNoEstimate(t *testing.T) {
	items := []BurndownItem{
		{Key: "PROJ-1", Points: 3, Status: statusOpen},
		{Key: "PROJ-2", Status: statusOpen},
		{Key: "PROJ-3", Points: 0, Status: statusShut},
	}

	burndown := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t))

	if burndown.Total != 3 {
		t.Errorf("total = %v, want 3", burndown.Total)
	}
	if len(burndown.Unestimated) != 2 {
		t.Fatalf("named %v as unestimated, want PROJ-2 and PROJ-3", burndown.Unestimated)
	}
}

// The ideal line is flat across nights and weekends. A calendar-time ideal
// shows a team falling behind every Saturday and catching up every Monday,
// which says nothing about them.
func TestIdealLineIsFlatOutsideWorkingHours(t *testing.T) {
	items := []BurndownItem{{Key: "PROJ-1", Points: 10, Status: statusOpen}}

	ideal := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t)).Ideal

	if ideal[0].Remaining != 10 {
		t.Errorf("ideal starts at %v, want the full 10", ideal[0].Remaining)
	}
	if last := ideal[len(ideal)-1].Remaining; last > 0.0001 {
		t.Errorf("ideal ends at %v, want 0", last)
	}

	// Saturday: the whole day sits inside one off-hours band, so the ideal
	// must not move across it.
	saturday, sunday := august(8, 16), august(9, 16)
	if remainingAt(ideal, saturday) != remainingAt(ideal, sunday) {
		t.Errorf("the ideal moved over the weekend: %v -> %v",
			remainingAt(ideal, saturday), remainingAt(ideal, sunday))
	}
}

func TestIdealLineDescendsMonotonically(t *testing.T) {
	items := []BurndownItem{{Key: "PROJ-1", Points: 21, Status: statusOpen}}

	ideal := BuildBurndown(items, burndownSprint, burndownHours(), eastern(t)).Ideal

	for i := 1; i < len(ideal); i++ {
		if ideal[i].Remaining > ideal[i-1].Remaining+0.0001 {
			t.Fatalf("ideal rose at vertex %d: %v -> %v", i, ideal[i-1].Remaining, ideal[i].Remaining)
		}
		if ideal[i].At.Before(ideal[i-1].At) {
			t.Fatalf("ideal vertices out of order at %d", i)
		}
	}
}

// A sprint with no working time at all cannot have an ideal pace, and drawing a
// straight line would imply one nobody could have kept.
func TestIdealLineStaysFlatWithNoWorkingTime(t *testing.T) {
	weekend := Sprint{
		Start: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC), // Saturday
		End:   time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC), // Sunday
	}
	items := []BurndownItem{{Key: "PROJ-1", Points: 5, Status: statusOpen}}

	ideal := BuildBurndown(items, weekend, burndownHours(), eastern(t)).Ideal

	for _, point := range ideal {
		if point.Remaining != 5 {
			t.Fatalf("ideal moved during a sprint with no working hours: %v", ideal)
		}
	}
}

func TestBurndownWithNoItemsIsEmptyRatherThanBroken(t *testing.T) {
	burndown := BuildBurndown(nil, burndownSprint, burndownHours(), eastern(t))

	if burndown.Total != 0 {
		t.Errorf("total = %v, want 0", burndown.Total)
	}
	if len(burndown.Remaining) < 2 {
		t.Errorf("remaining = %v, want a flat line rather than nothing", burndown.Remaining)
	}
}
