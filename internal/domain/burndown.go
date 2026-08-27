package domain

import (
	"sort"
	"time"
)

// Points is a story point estimate. It is a float because trackers store it as
// one — half points and the occasional 0.5 are legal, however unfashionable.
type Points float64

// BurndownItem is one work item's contribution to a sprint's burndown.
//
// Sub-tasks are absent by design: estimates live on the parent, so a burndown
// built from sub-tasks would be built from nothing.
type BurndownItem struct {
	Key    IssueKey
	Points Points

	// Status is what the item holds now, and Changes is how it got there.
	// Both are needed: an item that never moved during the sprint has no
	// changes at all, and its current status is then the only evidence of
	// whether it was finished.
	Status  IssueStatus
	Changes []StatusChange
}

// BurndownPoint is one vertex of a plotted line.
type BurndownPoint struct {
	At        time.Time
	Remaining Points
}

// Burndown is the points view of a sprint.
type Burndown struct {
	// Total is the scope: every selected item's estimate, whenever it entered
	// the sprint. A line that steps as work is added would be more truthful
	// about what was committed when, and is deliberately not attempted here.
	Total Points

	// Remaining is a step series — points are not burned gradually, they drop
	// when an item is finished — so consecutive vertices at the same instant
	// are expected and are what makes the step visible.
	Remaining []BurndownPoint

	// Ideal descends only during working hours, so it is flat across nights
	// and weekends. A calendar-time ideal shows a team falling behind every
	// Saturday and catching up every Monday, which says nothing about them.
	Ideal []BurndownPoint

	// Unestimated names selected items carrying no estimate. They contribute
	// nothing to either line, so without this they would be invisible — and a
	// burndown quietly missing a third of the sprint is worse than no
	// burndown.
	Unestimated []IssueKey
}

// BuildBurndown assembles the points view of a sprint.
func BuildBurndown(items []BurndownItem, sprint Sprint, hours WorkingHours, loc *time.Location) Burndown {
	window := sprint.Window()

	burndown := Burndown{
		Ideal: idealLine(totalOf(items), window, hours, loc),
		Total: totalOf(items),
	}
	for _, item := range items {
		if item.Points <= 0 {
			burndown.Unestimated = append(burndown.Unestimated, item.Key)
		}
	}
	burndown.Remaining = remainingLine(items, window)
	return burndown
}

func totalOf(items []BurndownItem) Points {
	var total Points
	for _, item := range items {
		if item.Points > 0 {
			total += item.Points
		}
	}
	return total
}

// finishedFlip is one moment an item started or stopped counting as finished.
type finishedFlip struct {
	at       time.Time
	finished bool
	points   Points
}

// remainingLine walks every item's status history together, so the result is a
// single series rather than a per-item one.
//
// Reopening is handled by construction rather than as a special case: an item
// that goes Done, then back to In Progress, then Done again simply flips twice
// and the line steps back up in between. Treating "finished" as a state over
// time rather than as one timestamp is what makes that fall out.
func remainingLine(items []BurndownItem, window Window) []BurndownPoint {
	total := totalOf(items)

	var flips []finishedFlip
	burnedAtStart := Points(0)

	for _, item := range items {
		if item.Points <= 0 {
			continue
		}
		finished := finishedAtStart(item, window)
		if finished {
			burnedAtStart += item.Points
		}
		for _, change := range sortedChanges(item.Changes) {
			if !change.At.After(window.Start) || change.At.After(window.End) {
				continue
			}
			if change.To.IsDone() == finished {
				// No crossing: a move between two unfinished statuses, or
				// between two finished ones, changes nothing here.
				continue
			}
			finished = change.To.IsDone()
			flips = append(flips, finishedFlip{at: change.At, finished: finished, points: item.Points})
		}
	}

	sort.SliceStable(flips, func(i, j int) bool { return flips[i].at.Before(flips[j].at) })

	burned := burnedAtStart
	line := []BurndownPoint{{At: window.Start, Remaining: total - burned}}

	for _, flip := range flips {
		if flip.finished {
			burned += flip.points
		} else {
			burned -= flip.points
		}
		// Two vertices at the same instant: the step down is drawn as a step
		// rather than a diagonal, which is what actually happened.
		line = append(line,
			BurndownPoint{At: flip.at, Remaining: line[len(line)-1].Remaining},
			BurndownPoint{At: flip.at, Remaining: total - burned})
	}

	return append(line, BurndownPoint{At: window.End, Remaining: total - burned})
}

// finishedAtStart recovers whether an item was already finished when the sprint
// opened.
//
// The earliest change inside the window records the status the item held before
// it, which is the only evidence available. With no changes in the window at
// all, the item never moved and its current status stood for the whole sprint.
func finishedAtStart(item BurndownItem, window Window) bool {
	changes := sortedChanges(item.Changes)
	for _, change := range changes {
		if change.At.After(window.Start) {
			return change.From.IsDone()
		}
	}
	if len(changes) > 0 {
		// Every change predates the sprint; the last one left it where it
		// began the sprint.
		return changes[len(changes)-1].To.IsDone()
	}
	return item.Status.IsDone()
}

func sortedChanges(changes []StatusChange) []StatusChange {
	out := make([]StatusChange, len(changes))
	copy(out, changes)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// idealLine descends only while the team is at work.
//
// It reuses the same working-hours segments the timeline's axis is built from,
// so the two charts agree about when the sprint was actually running. A vertex
// falls at every segment boundary, which makes the line flat across nights and
// weekends and sloped across working hours.
func idealLine(total Points, window Window, hours WorkingHours, loc *time.Location) []BurndownPoint {
	segments := AxisSegments(window, hours, loc)

	var working time.Duration
	for _, segment := range segments {
		if segment.Kind == SegmentWorking {
			working += segment.To.Sub(segment.From)
		}
	}
	if working <= 0 || len(segments) == 0 {
		// No working time in the window — a sprint entirely over a holiday, or
		// a schedule with no days. A straight line would imply a pace nobody
		// could have kept.
		return []BurndownPoint{
			{At: window.Start, Remaining: total},
			{At: window.End, Remaining: total},
		}
	}

	line := []BurndownPoint{{At: window.Start, Remaining: total}}
	var elapsed time.Duration
	for _, segment := range segments {
		if segment.Kind == SegmentWorking {
			elapsed += segment.To.Sub(segment.From)
		}
		remaining := total * Points(1-float64(elapsed)/float64(working))
		if remaining < 0 {
			remaining = 0
		}
		line = append(line, BurndownPoint{At: segment.To, Remaining: remaining})
	}
	return line
}
