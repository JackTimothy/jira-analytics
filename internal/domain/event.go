package domain

import (
	"sort"
	"time"
)

// Event is one timestamped observation about a sub-task. Adapters translate
// Jira changelog entries and GitHub pull-request activity into these; the
// domain replays them to reconstruct what was true at any past instant.
//
// apply is unexported so that only the domain can define how an observation
// changes the facts, while adapters remain free to construct the event types.
type Event interface {
	When() time.Time
	apply(*Facts)
}

// SortEvents orders events chronologically. It is stable, so events sharing a
// timestamp keep the order the adapter emitted them in — which matters when a
// pull request is opened and reviewers are requested in the same instant.
func SortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].When().Before(events[j].When())
	})
}

type StatusChanged struct {
	At time.Time
	To IssueStatus
}

func (e StatusChanged) When() time.Time { return e.At }
func (e StatusChanged) apply(f *Facts)  { f.Status = e.To }

// BranchFirstSeen marks the earliest moment a branch is known to have existed.
// Code hosts do not record branch creation, so this timestamp is inferred; see
// the code-host adapter for how.
type BranchFirstSeen struct {
	At   time.Time
	Name string
}

func (e BranchFirstSeen) When() time.Time { return e.At }
func (e BranchFirstSeen) apply(f *Facts)  { f.branches[e.Name] = struct{}{} }

type PROpened struct {
	At    time.Time
	PR    PRKey
	Draft bool
}

func (e PROpened) When() time.Time { return e.At }
func (e PROpened) apply(f *Facts) {
	pr := f.pr(e.PR)
	pr.OpenedAt = e.At
	pr.Draft = e.Draft
	pr.Closed = false
	pr.Merged = false
}

type PRDraftChanged struct {
	At    time.Time
	PR    PRKey
	Draft bool
}

func (e PRDraftChanged) When() time.Time { return e.At }
func (e PRDraftChanged) apply(f *Facts)  { f.pr(e.PR).Draft = e.Draft }

type ReviewRequested struct {
	At    time.Time
	PR    PRKey
	Actor ActorID
}

func (e ReviewRequested) When() time.Time { return e.At }
func (e ReviewRequested) apply(f *Facts) {
	f.pr(e.PR).Reviewers[e.Actor] = ReviewerRequested
}

type ReviewRequestRemoved struct {
	At    time.Time
	PR    PRKey
	Actor ActorID
}

func (e ReviewRequestRemoved) When() time.Time { return e.At }
func (e ReviewRequestRemoved) apply(f *Facts) {
	pr := f.pr(e.PR)
	// Only an outstanding request is withdrawn. A reviewer who has already
	// submitted keeps their verdict, so explicitly un-requesting someone after
	// they approved cannot silently undo the approval Derive depends on.
	if state, ok := pr.Reviewers[e.Actor]; ok && state == ReviewerRequested {
		delete(pr.Reviewers, e.Actor)
	}
}

type ReviewSubmitted struct {
	At    time.Time
	PR    PRKey
	Actor ActorID
	State ReviewerState
}

func (e ReviewSubmitted) When() time.Time { return e.At }
func (e ReviewSubmitted) apply(f *Facts) {
	f.pr(e.PR).Reviewers[e.Actor] = e.State
}

type PRClosed struct {
	At time.Time
	PR PRKey
}

func (e PRClosed) When() time.Time { return e.At }
func (e PRClosed) apply(f *Facts)  { f.pr(e.PR).Closed = true }

type PRReopened struct {
	At time.Time
	PR PRKey
}

func (e PRReopened) When() time.Time { return e.At }
func (e PRReopened) apply(f *Facts)  { f.pr(e.PR).Closed = false }

type PRMerged struct {
	At time.Time
	PR PRKey
}

func (e PRMerged) When() time.Time { return e.At }
func (e PRMerged) apply(f *Facts) {
	pr := f.pr(e.PR)
	pr.Merged = true
	pr.Closed = true
}
