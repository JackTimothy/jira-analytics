package domain

// Derive maps a set of facts onto exactly one state. It is pure and total: the
// same facts always yield the same state, and every possible input yields one.
//
// The ordering below is load-bearing, not stylistic. Two orderings in
// particular are deliberate:
//
//   - Blocked outranks every code-host-derived state, so work someone flagged
//     as blocked stays visible even while its pull request is open.
//   - Approved is tested before Review Requested, and both before In Progress.
//     A code host drops a reviewer from the "requested" list once they submit,
//     so a "nobody is requested" test placed earlier would misread every fully
//     reviewed pull request as still in progress.
func Derive(f *Facts) State {
	if f.Status.IsDone() || f.AnyMerged() {
		return StateDone
	}
	if f.Status.IsBlocked() {
		return StateBlocked
	}

	if pr := f.ActivePR(); pr != nil && !pr.Draft && len(pr.Reviewers) > 0 {
		switch {
		case allApproved(pr.Reviewers):
			return StateApproved
		case anyFeedback(pr.Reviewers):
			return StateFeedbackGiven
		default:
			return StateReviewRequested
		}
	}

	// A branch exists but review has not meaningfully started: no open pull
	// request, or it is a draft, or nobody has been asked to look at it.
	if f.BranchCount() > 0 {
		return StateInProgress
	}

	// No branch. A status that implies work is underway is better evidence than
	// the absence of a branch, so trust it rather than reporting To Do.
	if !f.Status.IsUnstarted() {
		return StateInProgress
	}
	return StateToDo
}

func allApproved(reviewers map[ActorID]ReviewerState) bool {
	for _, state := range reviewers {
		if state != ReviewerApproved {
			return false
		}
	}
	return true
}

func anyFeedback(reviewers map[ActorID]ReviewerState) bool {
	for _, state := range reviewers {
		if state.IsFeedback() {
			return true
		}
	}
	return false
}
