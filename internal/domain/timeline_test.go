package domain

import (
	"testing"
	"time"
)

func day(d int, hour int) time.Time {
	return time.Date(2026, 8, d, hour, 0, 0, 0, time.UTC)
}

// sprint runs 3 Aug 09:00 to 17 Aug 09:00.
var testWindow = Window{Start: day(3, 9), End: day(17, 9)}

func assertIntervals(t *testing.T, got []Interval, want []Interval) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d intervals, want %d:\n got: %s\nwant: %s",
			len(got), len(want), formatIntervals(got), formatIntervals(want))
	}
	for i := range want {
		if got[i].State != want[i].State || !got[i].From.Equal(want[i].From) || !got[i].To.Equal(want[i].To) {
			t.Errorf("interval %d = %s, want %s", i, formatInterval(got[i]), formatInterval(want[i]))
		}
	}
}

func formatInterval(i Interval) string {
	return i.State.String() + " " + i.From.Format(time.RFC3339) + ".." + i.To.Format(time.RFC3339)
}

func formatIntervals(is []Interval) string {
	out := ""
	for _, i := range is {
		out += "\n  " + formatInterval(i)
	}
	if out == "" {
		return " (none)"
	}
	return out
}

func TestBuildTimelineFullLifecycle(t *testing.T) {
	events := []Event{
		StatusChanged{At: day(4, 9), To: statusInProgress},
		BranchFirstSeen{At: day(4, 9), Name: "PROJ-1-work"},
		PROpened{At: day(5, 9), PR: prOne, Draft: true},
		PRDraftChanged{At: day(6, 9), PR: prOne, Draft: false},
		ReviewRequested{At: day(6, 9), PR: prOne, Actor: "alice"},
		ReviewSubmitted{At: day(8, 9), PR: prOne, Actor: "alice", State: ReviewerChangesRequested},
		ReviewSubmitted{At: day(10, 9), PR: prOne, Actor: "alice", State: ReviewerApproved},
		PRMerged{At: day(11, 9), PR: prOne},
	}

	got := BuildTimeline(events, statusToDo, day(1, 9), testWindow)

	assertIntervals(t, got, []Interval{
		{State: StateToDo, From: day(3, 9), To: day(4, 9)},
		{State: StateInProgress, From: day(4, 9), To: day(6, 9)},
		{State: StateReviewRequested, From: day(6, 9), To: day(8, 9)},
		{State: StateFeedbackGiven, From: day(8, 9), To: day(10, 9)},
		{State: StateApproved, From: day(10, 9), To: day(11, 9)},
		{State: StateDone, From: day(11, 9), To: day(17, 9)},
	})
}

func TestBuildTimelineFoldsPriorEventsIntoOpeningState(t *testing.T) {
	// Carried-over work: everything happened before the sprint opened, so the
	// row must start mid-flight rather than at To Do.
	events := []Event{
		StatusChanged{At: day(1, 9), To: statusInProgress},
		BranchFirstSeen{At: day(1, 9), Name: "b"},
		PROpened{At: day(2, 9), PR: prOne},
		ReviewRequested{At: day(2, 9), PR: prOne, Actor: "alice"},
	}

	got := BuildTimeline(events, statusToDo, day(1, 9), testWindow)

	assertIntervals(t, got, []Interval{
		{State: StateReviewRequested, From: day(3, 9), To: day(17, 9)},
	})
}

func TestBuildTimelineStartsAtCreationWhenCreatedMidWindow(t *testing.T) {
	events := []Event{
		BranchFirstSeen{At: day(8, 9), Name: "b"},
	}

	got := BuildTimeline(events, statusToDo, day(6, 9), testWindow)

	assertIntervals(t, got, []Interval{
		{State: StateToDo, From: day(6, 9), To: day(8, 9)},
		{State: StateInProgress, From: day(8, 9), To: day(17, 9)},
	})
}

func TestBuildTimelineReturnsNothingWhenCreatedAfterWindow(t *testing.T) {
	got := BuildTimeline(nil, statusToDo, day(20, 9), testWindow)
	if got != nil {
		t.Fatalf("expected no intervals, got %s", formatIntervals(got))
	}
}

func TestBuildTimelineIgnoresEventsAfterWindow(t *testing.T) {
	events := []Event{
		BranchFirstSeen{At: day(4, 9), Name: "b"},
		PROpened{At: day(20, 9), PR: prOne},
		PRMerged{At: day(21, 9), PR: prOne},
	}

	got := BuildTimeline(events, statusToDo, day(1, 9), testWindow)

	assertIntervals(t, got, []Interval{
		{State: StateToDo, From: day(3, 9), To: day(4, 9)},
		{State: StateInProgress, From: day(4, 9), To: day(17, 9)},
	})
}

func TestBuildTimelineDropsZeroLengthIntervals(t *testing.T) {
	// Branch, pull request, reviewer and approval all land in the same instant:
	// the intermediate states were never observable and must not be charted.
	events := []Event{
		BranchFirstSeen{At: day(5, 9), Name: "b"},
		PROpened{At: day(5, 9), PR: prOne},
		ReviewRequested{At: day(5, 9), PR: prOne, Actor: "alice"},
		ReviewSubmitted{At: day(5, 9), PR: prOne, Actor: "alice", State: ReviewerApproved},
	}

	got := BuildTimeline(events, statusToDo, day(1, 9), testWindow)

	assertIntervals(t, got, []Interval{
		{State: StateToDo, From: day(3, 9), To: day(5, 9)},
		{State: StateApproved, From: day(5, 9), To: day(17, 9)},
	})
}

func TestBuildTimelineCollapsesRunsOfTheSameState(t *testing.T) {
	// Two reviewers requested at different times both mean Review Requested;
	// the row should not be chopped into two identical segments.
	events := []Event{
		BranchFirstSeen{At: day(4, 9), Name: "b"},
		PROpened{At: day(5, 9), PR: prOne},
		ReviewRequested{At: day(5, 9), PR: prOne, Actor: "alice"},
		ReviewRequested{At: day(7, 9), PR: prOne, Actor: "bob"},
	}

	got := BuildTimeline(events, statusToDo, day(1, 9), testWindow)

	assertIntervals(t, got, []Interval{
		{State: StateToDo, From: day(3, 9), To: day(4, 9)},
		{State: StateInProgress, From: day(4, 9), To: day(5, 9)},
		{State: StateReviewRequested, From: day(5, 9), To: day(17, 9)},
	})
}

func TestBuildTimelineIsIndependentOfInputOrder(t *testing.T) {
	ordered := []Event{
		BranchFirstSeen{At: day(4, 9), Name: "b"},
		PROpened{At: day(5, 9), PR: prOne},
		ReviewRequested{At: day(6, 9), PR: prOne, Actor: "alice"},
		ReviewSubmitted{At: day(7, 9), PR: prOne, Actor: "alice", State: ReviewerApproved},
		PRMerged{At: day(8, 9), PR: prOne},
	}
	shuffled := []Event{ordered[3], ordered[0], ordered[4], ordered[2], ordered[1]}

	want := BuildTimeline(ordered, statusToDo, day(1, 9), testWindow)
	got := BuildTimeline(shuffled, statusToDo, day(1, 9), testWindow)

	assertIntervals(t, got, want)
}

func TestBuildTimelineDoesNotMutateCallerSlice(t *testing.T) {
	events := []Event{
		PRMerged{At: day(8, 9), PR: prOne},
		BranchFirstSeen{At: day(4, 9), Name: "b"},
	}
	first := events[0]

	BuildTimeline(events, statusToDo, day(1, 9), testWindow)

	if events[0] != first {
		t.Error("BuildTimeline reordered the caller's slice")
	}
}
