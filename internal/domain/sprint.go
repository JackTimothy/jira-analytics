package domain

import "time"

type SprintID string

type Sprint struct {
	ID    SprintID
	Name  string
	Start time.Time
	End   time.Time
}

func (s Sprint) Window() Window { return Window{Start: s.Start, End: s.End} }

// InScope reports whether a parent work item was part of the sprint's committed
// scope.
//
// The rule is that the parent carries a due date on or before the sprint's end.
// That single comparison covers the three ways work is legitimately committed:
//
//   - committed for this sprint      due date == sprint end
//   - carried over from an earlier   due date <  sprint end (the due date is
//     sprint                         preserved to mark the original commitment)
//   - pulled forward for an urgent    due date <  sprint end
//     business need
//
// and excludes the two ways it is not: work pulled in opportunistically carries
// no due date at all, and work belonging to a later sprint carries a due date
// beyond this sprint's end.
//
// The comparison is between calendar days. A tracker records a due date as a
// calendar day while a sprint ends at an instant, so the sprint end is resolved
// to the day it falls on in the project's timezone. Without that, every sprint
// ending near midnight UTC is misclassified.
func InScope(dueDate *CalendarDate, sprint Sprint, loc *time.Location) bool {
	if dueDate == nil {
		return false
	}
	return !dueDate.After(CalendarDateIn(sprint.End, loc))
}
