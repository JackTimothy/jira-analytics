package domain

import (
	"testing"
	"time"
)

var (
	statusToDo       = IssueStatus{Name: "To Do", Category: CategoryToDo}
	statusInProgress = IssueStatus{Name: "In Progress", Category: CategoryInProgress}
	statusReview     = IssueStatus{Name: "Review", Category: CategoryInProgress}
	statusBlocked    = IssueStatus{Name: "Blocked", Category: CategoryToDo}
	statusDone       = IssueStatus{Name: "Done", Category: CategoryDone}
	statusCancelled  = IssueStatus{Name: "Cancelled", Category: CategoryDone}
)

func at(minute int) time.Time {
	return time.Date(2026, 8, 3, 9, minute, 0, 0, time.UTC)
}

var prOne = PRKey{Repo: "org/repo", Number: 1}
var prTwo = PRKey{Repo: "org/repo", Number: 2}

// factsFrom folds events onto a starting status, exercising the same path the
// timeline uses rather than hand-building internal state.
func factsFrom(initial IssueStatus, events ...Event) *Facts {
	facts := NewFacts(initial)
	SortEvents(events)
	for _, e := range events {
		e.apply(&facts)
	}
	return &facts
}

func TestDerive(t *testing.T) {
	tests := []struct {
		name   string
		facts  *Facts
		expect State
	}{
		{
			name:   "unstarted status with no branch is To Do",
			facts:  factsFrom(statusToDo),
			expect: StateToDo,
		},
		{
			name:   "in-flight status with no branch is In Progress, not To Do",
			facts:  factsFrom(statusInProgress),
			expect: StateInProgress,
		},
		{
			name:   "Review status with no branch falls through to In Progress",
			facts:  factsFrom(statusReview),
			expect: StateInProgress,
		},
		{
			name:   "branch with no pull request is In Progress",
			facts:  factsFrom(statusToDo, BranchFirstSeen{At: at(1), Name: "PROJ-1-thing"}),
			expect: StateInProgress,
		},
		{
			name: "draft pull request stays In Progress even with reviewers",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne, Draft: true},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
			),
			expect: StateInProgress,
		},
		{
			name: "open pull request with no reviewers is In Progress",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
			),
			expect: StateInProgress,
		},
		{
			name: "all reviewers outstanding is Review Requested",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewRequested{At: at(3), PR: prOne, Actor: "bob"},
			),
			expect: StateReviewRequested,
		},
		{
			name: "partial approval with one outstanding is Review Requested",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewRequested{At: at(3), PR: prOne, Actor: "bob"},
				ReviewSubmitted{At: at(4), PR: prOne, Actor: "alice", State: ReviewerApproved},
			),
			expect: StateReviewRequested,
		},
		{
			name: "every reviewer approved is Approved, taking precedence over Review Requested",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewSubmitted{At: at(4), PR: prOne, Actor: "alice", State: ReviewerApproved},
			),
			expect: StateApproved,
		},
		{
			name: "a comment counts as Feedback Given",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewSubmitted{At: at(4), PR: prOne, Actor: "alice", State: ReviewerCommented},
			),
			expect: StateFeedbackGiven,
		},
		{
			name: "changes requested alongside an approval is Feedback Given",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewRequested{At: at(3), PR: prOne, Actor: "bob"},
				ReviewSubmitted{At: at(4), PR: prOne, Actor: "alice", State: ReviewerApproved},
				ReviewSubmitted{At: at(5), PR: prOne, Actor: "bob", State: ReviewerChangesRequested},
			),
			expect: StateFeedbackGiven,
		},
		{
			name: "merged pull request is Done even while the tracker lags behind",
			facts: factsFrom(statusInProgress,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				PRMerged{At: at(5), PR: prOne},
			),
			expect: StateDone,
		},
		{
			name:   "a terminal status is Done even with no code at all",
			facts:  factsFrom(statusDone),
			expect: StateDone,
		},
		{
			name:   "Cancelled is in the terminal category and reads as Done",
			facts:  factsFrom(statusCancelled),
			expect: StateDone,
		},
		{
			name: "Blocked outranks an approved pull request",
			facts: factsFrom(statusBlocked,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewSubmitted{At: at(4), PR: prOne, Actor: "alice", State: ReviewerApproved},
			),
			expect: StateBlocked,
		},
		{
			name:   "Blocked outranks a bare branch",
			facts:  factsFrom(statusBlocked, BranchFirstSeen{At: at(1), Name: "b"}),
			expect: StateBlocked,
		},
		{
			name: "Done outranks Blocked",
			facts: factsFrom(statusBlocked,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				PRMerged{At: at(3), PR: prOne},
			),
			expect: StateDone,
		},
		{
			name: "a pull request closed without merging falls back to In Progress",
			facts: factsFrom(statusInProgress,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				PRClosed{At: at(4), PR: prOne},
			),
			expect: StateInProgress,
		},
		{
			name: "a superseded closed pull request does not mask the open one",
			facts: factsFrom(statusInProgress,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				PRClosed{At: at(3), PR: prOne},
				PROpened{At: at(4), PR: prTwo},
				ReviewRequested{At: at(5), PR: prTwo, Actor: "alice"},
				ReviewSubmitted{At: at(6), PR: prTwo, Actor: "alice", State: ReviewerApproved},
			),
			expect: StateApproved,
		},
		{
			// A merge is not the end while something is still open. The work
			// item is either spread across branches or has a follow-up fix in
			// review; in both cases somebody is still waiting on a reviewer.
			name: "a merge does not finish the work while another pull request is open",
			facts: factsFrom(statusInProgress,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				PRMerged{At: at(3), PR: prOne},
				PROpened{At: at(4), PR: prTwo},
			),
			expect: StateInProgress,
		},
		{
			// The other half of the same rule: a pull request closed without
			// merging is abandoned, not outstanding, so it must never keep
			// finished work looking unfinished.
			name: "a merge finishes the work when the other pull request was abandoned",
			facts: factsFrom(statusInProgress,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				PROpened{At: at(3), PR: prTwo},
				PRClosed{At: at(4), PR: prTwo},
				PRMerged{At: at(5), PR: prOne},
			),
			expect: StateDone,
		},
		{
			name: "a merge finishes the work when it is the only pull request",
			facts: factsFrom(statusInProgress,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				PRMerged{At: at(3), PR: prOne},
			),
			expect: StateDone,
		},
		{
			name: "un-requesting a reviewer who already approved keeps the approval",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewSubmitted{At: at(4), PR: prOne, Actor: "alice", State: ReviewerApproved},
				ReviewRequestRemoved{At: at(5), PR: prOne, Actor: "alice"},
			),
			expect: StateApproved,
		},
		{
			name: "withdrawing the only outstanding request returns to In Progress",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				ReviewRequestRemoved{At: at(4), PR: prOne, Actor: "alice"},
			),
			expect: StateInProgress,
		},
		{
			name: "a reopened pull request becomes active again",
			facts: factsFrom(statusInProgress,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				PRClosed{At: at(4), PR: prOne},
				PRReopened{At: at(5), PR: prOne},
			),
			expect: StateReviewRequested,
		},
		{
			name: "converting back to draft returns to In Progress",
			facts: factsFrom(statusToDo,
				BranchFirstSeen{At: at(1), Name: "b"},
				PROpened{At: at(2), PR: prOne},
				ReviewRequested{At: at(3), PR: prOne, Actor: "alice"},
				PRDraftChanged{At: at(4), PR: prOne, Draft: true},
			),
			expect: StateInProgress,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Derive(tc.facts); got != tc.expect {
				t.Errorf("Derive() = %s, want %s", got, tc.expect)
			}
		})
	}
}

func TestDeriveIsDeterministicAcrossMapOrdering(t *testing.T) {
	// ActivePR and the reviewer predicates iterate maps, so run repeatedly to
	// catch a result that depends on Go's randomised map order.
	facts := factsFrom(statusInProgress,
		BranchFirstSeen{At: at(1), Name: "b"},
		PROpened{At: at(2), PR: prOne},
		PROpened{At: at(3), PR: prTwo},
		ReviewRequested{At: at(4), PR: prTwo, Actor: "alice"},
		ReviewSubmitted{At: at(5), PR: prTwo, Actor: "alice", State: ReviewerApproved},
	)
	for i := 0; i < 200; i++ {
		if got := Derive(facts); got != StateApproved {
			t.Fatalf("iteration %d: Derive() = %s, want APPROVED", i, got)
		}
	}
}
