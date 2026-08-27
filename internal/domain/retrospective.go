package domain

// RowKind says what one charted row represents.
//
// A row was once always a sub-task. It stopped being one when teams that do not
// break work down started using this: their sprints are Stories, Bugs and Tasks
// with no children, and a work item that is a header over nothing is not worth
// drawing.
type RowKind uint8

const (
	// RowSubTask is a sub-task of the work item above it.
	RowSubTask RowKind = iota
	// RowWorkItem is the work item itself, charted directly because it has no
	// sub-tasks.
	RowWorkItem
	// RowBranch is one branch of a work item that has several. Undisciplined
	// teams put more than one branch on an item, and one bar cannot hold two
	// review states at once.
	RowBranch
)

func (k RowKind) String() string {
	switch k {
	case RowWorkItem:
		return "WORK_ITEM"
	case RowBranch:
		return "BRANCH"
	default:
		return "SUB_TASK"
	}
}

// Row is one charted line: something that held states over time.
type Row struct {
	Kind RowKind

	// Key is the sub-task's key, or the work item's own for the other kinds.
	Key IssueKey

	// Label is what identifies the row to a reader: a summary, or the branch
	// name where several branches share one work item.
	Label string

	Intervals []Interval
}

// ParentGroup gathers the rows belonging to one work item, which is how the
// retrospective is organised.
type ParentGroup struct {
	Parent  WorkItem
	InScope bool
	Rows    []Row
}

// Retrospective is the completed analysis of one sprint.
type Retrospective struct {
	Sprint   Sprint
	Groups   []ParentGroup
	Warnings []string

	// Axis is the sprint window split into working and off-hours spans, in the
	// project's timezone — what lets the client compress dead time while
	// keeping off-hours transitions visible.
	Axis []AxisSegment

	// Burndown is the same sprint measured in points rather than in states. It
	// follows the same scope selection as Groups, so the toggle moves both.
	Burndown Burndown
}

// Scope selects which parents appear in a retrospective.
type Scope string

const (
	ScopeAll       Scope = "all"
	ScopeCommitted Scope = "committed"
)

func (s Scope) Valid() bool { return s == ScopeAll || s == ScopeCommitted }
