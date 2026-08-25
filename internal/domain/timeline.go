package domain

import "time"

// Window is a half-open time range [Start, End].
type Window struct {
	Start time.Time
	End   time.Time
}

func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Start) && !t.After(w.End)
}

// Interval is a contiguous run during which a sub-task held one state.
type Interval struct {
	State State
	From  time.Time
	To    time.Time
}

func (i Interval) Duration() time.Duration { return i.To.Sub(i.From) }

// BuildTimeline replays events to produce the states a sub-task occupied inside
// the window.
//
// Events before the window are folded in but emit no interval: they establish
// what was already true when the sprint opened, which is how carried-over work
// shows its real starting state rather than appearing to begin at To Do.
//
// A sub-task created mid-window starts at its creation instant, not at the
// window start, so a row that begins partway across the chart is the honest
// picture. A sub-task created after the window ends yields no intervals at all.
func BuildTimeline(events []Event, initial IssueStatus, createdAt time.Time, w Window) []Interval {
	if createdAt.After(w.End) {
		return nil
	}

	ordered := make([]Event, len(events))
	copy(ordered, events)
	SortEvents(ordered)

	facts := NewFacts(initial)

	start := w.Start
	if createdAt.After(start) {
		start = createdAt
	}

	// Everything at or before the opening instant establishes the baseline.
	i := 0
	for ; i < len(ordered) && !ordered[i].When().After(start); i++ {
		ordered[i].apply(&facts)
	}

	intervals := make([]Interval, 0, 8)
	current := Interval{State: Derive(&facts), From: start}

	for ; i < len(ordered); i++ {
		event := ordered[i]
		at := event.When()
		if at.After(w.End) {
			break
		}
		event.apply(&facts)

		next := Derive(&facts)
		if next == current.State {
			continue
		}
		current.To = at
		intervals = appendNonEmpty(intervals, current)
		current = Interval{State: next, From: at}
	}

	current.To = w.End
	return appendNonEmpty(intervals, current)
}

// appendNonEmpty drops zero-length intervals, which arise whenever several
// events share a timestamp and the state passes through an intermediate value
// without ever being observable.
func appendNonEmpty(intervals []Interval, candidate Interval) []Interval {
	if !candidate.To.After(candidate.From) {
		return intervals
	}
	return append(intervals, candidate)
}
