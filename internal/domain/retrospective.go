package domain

// SubTaskTimeline is one charted row: a sub-task and the states it held.
type SubTaskTimeline struct {
	SubTask   SubTask
	Intervals []Interval
}

// ParentGroup gathers the sub-task rows belonging to one parent work item,
// which is how the retrospective is organised.
type ParentGroup struct {
	Parent   WorkItem
	InScope  bool
	SubTasks []SubTaskTimeline
}

// Retrospective is the completed analysis of one sprint.
type Retrospective struct {
	Sprint   Sprint
	Groups   []ParentGroup
	Warnings []string
}

// Scope selects which parents appear in a retrospective.
type Scope string

const (
	ScopeAll       Scope = "all"
	ScopeCommitted Scope = "committed"
)

func (s Scope) Valid() bool { return s == ScopeAll || s == ScopeCommitted }
