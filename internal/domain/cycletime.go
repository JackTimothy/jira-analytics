package domain

import (
	"strings"
	"time"
)

// CycleTimeTypes are the work item types whose cycle time is measured.
//
// They are the types that carry delivery work and an estimate. A support
// ticket or a spike averaged in with them would be divided by points nobody
// gave it and pull the figure towards something nobody committed to. The list
// is fixed rather than configured for now; a team whose types are named
// differently is the reason to move it into the project's settings, as
// typesLast was.
var CycleTimeTypes = []string{"Story", "Task", "Bug"}

// CycleTime is how long one finished work item took, counted in working hours.
type CycleTime struct {
	// Item travels whole, so whoever presents this reads the key, summary,
	// type and estimate from the same place the figure came from.
	Item WorkItem

	// Started is the first time the item was in progress, and Finished the
	// last time it reached Done.
	Started  time.Time
	Finished time.Time

	// Working is the part of the time between the two that fell inside
	// working hours. Nights and weekends are nobody's cycle time.
	Working time.Duration
}

// PerPoint is the working time spent per point of estimate. An unestimated
// item has nothing to divide by and reports zero.
func (c CycleTime) PerPoint() time.Duration {
	if c.Item.Points <= 0 {
		return 0
	}
	return time.Duration(float64(c.Working) / float64(c.Item.Points))
}

// SprintCycleTime is cycle time across a sprint's selected work items.
type SprintCycleTime struct {
	// Items is every selected Story, Task and Bug that is Done now and whose
	// cycle could be measured, whenever it finished. An item finished outside
	// the sprint plays no part in the average, but its own cycle time is still
	// worth showing beside it.
	Items []CycleTime

	// Counted holds the average's inputs: the items that finished during the
	// sprint and carry an estimate.
	Counted []CycleTime

	// PerPoint is the mean of each counted item's working time per point.
	// It means nothing when Counted is empty.
	PerPoint time.Duration

	// Unestimated names items that finished during the sprint without an
	// estimate, and Unstarted those that finished during it without ever
	// being marked in progress. Neither can contribute, and leaving them out
	// silently would make the average look better founded than it is.
	Unestimated []IssueKey
	Unstarted   []IssueKey
}

// BuildCycleTimes measures each selected work item and averages those that
// finished during the sprint.
//
// The clock starts the first time the item's own status entered the
// in-progress category, however long before the sprint that was, and stops the
// last time it reached Done. The last rather than the first because work that
// was reopened was not finished the first time, and its rework is part of how
// long it took.
//
// Finishing during the sprint uses the burndown's rule — after the sprint
// opened, up to and including its close — so "finished this sprint" means the
// same thing on both views.
//
// The average is the mean of each item's hours per point, not the sprint's
// total hours over its total points. The two differ whenever estimates differ,
// and the mean of ratios is what was asked for: every finished item gets one
// vote, rather than the largest item deciding the figure.
func BuildCycleTimes(
	items []WorkItem,
	history map[IssueKey][]StatusChange,
	sprint Sprint,
	hours WorkingHours,
	loc *time.Location,
) SprintCycleTime {
	window := sprint.Window()

	var (
		result SprintCycleTime
		sum    float64
	)
	for _, item := range items {
		if !isCycleTimeType(item.Type) || !item.Status.IsCompleted() {
			continue
		}
		started, finished := cycleBounds(item, history[item.Key])
		if finished.IsZero() {
			// Done now with no recorded move into Done: there is no telling
			// when it finished, and so no telling whether it was this sprint.
			continue
		}
		finishedInSprint := finished.After(window.Start) && !finished.After(window.End)

		if started.IsZero() {
			if finishedInSprint {
				result.Unstarted = append(result.Unstarted, item.Key)
			}
			continue
		}

		cycle := CycleTime{
			Item:     item,
			Started:  started,
			Finished: finished,
			Working:  WorkingDuration(started, finished, hours, loc),
		}
		result.Items = append(result.Items, cycle)

		if !finishedInSprint {
			continue
		}
		if item.Points <= 0 {
			result.Unestimated = append(result.Unestimated, item.Key)
			continue
		}
		result.Counted = append(result.Counted, cycle)
		sum += float64(cycle.PerPoint())
	}

	if len(result.Counted) > 0 {
		result.PerPoint = time.Duration(sum / float64(len(result.Counted)))
	}
	return result
}

// cycleBounds finds when an item's cycle started and finished, either being
// zero where the history does not say.
//
// An item created straight into an in-progress status never moves into one, so
// its first recorded change leaving such a status is the evidence that it was
// in progress from the moment it was created.
func cycleBounds(item WorkItem, changes []StatusChange) (started, finished time.Time) {
	changes = sortedChanges(changes)
	if len(changes) > 0 && changes[0].From.Category == CategoryInProgress {
		started = item.Created
	}
	for _, change := range changes {
		if started.IsZero() && change.To.Category == CategoryInProgress {
			started = change.At
		}
		if change.To.IsCompleted() {
			finished = change.At
		}
	}
	if !started.IsZero() && started.After(finished) {
		// Only reachable on a history that contradicts itself; a cycle that
		// ends before it starts is not one worth reporting.
		started = time.Time{}
	}
	return started, finished
}

func isCycleTimeType(name string) bool {
	name = strings.TrimSpace(name)
	for _, measured := range CycleTimeTypes {
		if strings.EqualFold(name, measured) {
			return true
		}
	}
	return false
}
