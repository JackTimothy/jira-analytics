package domain

import "time"

type (
	IssueKey  string
	ProjectID string
)

// WorkItem is a parent-level item: a story, task, or bug. Sub-tasks hang off it
// and inherit their sprint membership from it.
type WorkItem struct {
	Key     IssueKey
	Summary string
	DueDate *CalendarDate
	Created time.Time

	// Points is the estimate the burndown is built from. Estimates live at
	// this level and not on sub-tasks, which is why the burndown is the one
	// view built from parents rather than from the rows beneath them.
	// Zero means unestimated; nothing is estimated at zero.
	Points Points

	// Status is what the item holds now. With no recorded transitions it is
	// the only evidence of whether the item was finished during the sprint.
	Status IssueStatus
}

// SubTask is the unit the retrospective charts. It is the only level at which
// branches exist, which is why it is also the only level with a timeline.
type SubTask struct {
	Key       IssueKey
	ParentKey IssueKey
	Summary   string
	Created   time.Time
	Status    IssueStatus
}

// StatusChange is one transition from a tracker's change history. Both sides
// are recorded because the earliest entry's From is the only evidence of what a
// sub-task's status was before anyone changed it.
type StatusChange struct {
	At   time.Time
	From IssueStatus
	To   IssueStatus
}
