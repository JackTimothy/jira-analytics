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

// StatusChange is one transition from a tracker's change history.
type StatusChange struct {
	At time.Time
	To IssueStatus
}
