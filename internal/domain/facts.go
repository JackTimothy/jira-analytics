package domain

import (
	"strings"
	"time"
)

// StatusCategory is the tracker-agnostic bucket a status falls into. Mapping
// concrete status names onto these buckets is an adapter's job, which is what
// lets the app work against any workflow without per-status configuration.
type StatusCategory uint8

const (
	CategoryToDo StatusCategory = iota
	CategoryInProgress
	CategoryDone
)

// BlockedStatusName is the one status name the process itself defines. Every
// other status reaches the domain as a category.
const BlockedStatusName = "Blocked"

type IssueStatus struct {
	Name     string
	Category StatusCategory
}

func (s IssueStatus) IsDone() bool { return s.Category == CategoryDone }

func (s IssueStatus) IsBlocked() bool {
	return strings.EqualFold(strings.TrimSpace(s.Name), BlockedStatusName)
}

// IsTerminalOrUnstarted reports whether a status carries no implication that
// work is underway. Used to decide between To Do and In Progress for a sub-task
// that has no branch yet.
func (s IssueStatus) IsUnstarted() bool { return s.Category == CategoryToDo }

// ActorID identifies a reviewer. Only human reviewers ever reach the domain;
// bots are filtered in the code-host adapter.
type ActorID string

// ReviewerState is one reviewer's position on a pull request.
type ReviewerState uint8

const (
	ReviewerRequested ReviewerState = iota
	ReviewerCommented
	ReviewerChangesRequested
	ReviewerApproved
)

// IsFeedback reports whether the reviewer has actually said something that the
// author must respond to.
func (r ReviewerState) IsFeedback() bool {
	return r == ReviewerCommented || r == ReviewerChangesRequested
}

// PRKey identifies a pull request across repositories, since one project may
// source from several.
type PRKey struct {
	Repo   string
	Number int
}

type PullRequest struct {
	Key       PRKey
	OpenedAt  time.Time
	Draft     bool
	Closed    bool
	Merged    bool
	Reviewers map[ActorID]ReviewerState
}

func (p *PullRequest) IsOpen() bool { return !p.Closed && !p.Merged }

// Facts is everything the state predicates need, as of an instant. It is
// mutated forward by events; Derive reads it and never writes.
type Facts struct {
	Status   IssueStatus
	branches map[string]struct{}
	prs      map[PRKey]*PullRequest
}

func NewFacts(initial IssueStatus) Facts {
	return Facts{
		Status:   initial,
		branches: map[string]struct{}{},
		prs:      map[PRKey]*PullRequest{},
	}
}

func (f *Facts) BranchCount() int { return len(f.branches) }

func (f *Facts) AnyMerged() bool {
	for _, pr := range f.prs {
		if pr.Merged {
			return true
		}
	}
	return false
}

// ActivePR returns the most recently opened pull request that is still open.
// Sub-tasks routinely accumulate superseded and reopened PRs, so "the" PR has
// to be chosen rather than assumed. Ties break on number to keep the result
// deterministic despite map iteration order.
func (f *Facts) ActivePR() *PullRequest {
	var best *PullRequest
	for _, pr := range f.prs {
		if !pr.IsOpen() {
			continue
		}
		if best == nil || pr.OpenedAt.After(best.OpenedAt) ||
			(pr.OpenedAt.Equal(best.OpenedAt) && pr.Key.Number > best.Key.Number) {
			best = pr
		}
	}
	return best
}

func (f *Facts) pr(key PRKey) *PullRequest {
	if existing, ok := f.prs[key]; ok {
		return existing
	}
	created := &PullRequest{Key: key, Reviewers: map[ActorID]ReviewerState{}}
	f.prs[key] = created
	return created
}
