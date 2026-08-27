package domain

import "sort"

// A strand is one branch's worth of an issue's code activity: the branch
// itself, the pull requests opened from it, and everything that happened to
// them.
//
// Splitting matters because an issue with several live branches cannot be
// charted as one bar without contradicting itself — the bar would have to hold
// two review states at once, and the first merge would end it while the rest
// were still open. One row per strand says what actually happened.
type Strand struct {
	// Branch is the "repo:branch" name, or "" for activity that named no
	// branch at all.
	Branch string
	Events []Event
}

// SplitByBranch partitions one issue's code events into strands.
//
// Only BranchFirstSeen and PROpened name a branch; every other pull-request
// event names a PRKey, so the mapping from key to branch is learned from the
// PROpened events and applied to the rest. An event whose pull request was
// never seen opening — possible if it opened before the scan window — files
// under the unnamed strand rather than being dropped, because a missing review
// is worse than an unattributed one.
//
// The result is ordered by branch name so two runs over the same events chart
// the same way.
func SplitByBranch(events []Event) []Strand {
	branchOf := map[PRKey]string{}
	for _, event := range events {
		if opened, ok := event.(PROpened); ok {
			branchOf[opened.PR] = opened.Branch
		}
	}

	byBranch := map[string][]Event{}
	for _, event := range events {
		byBranch[strandOf(event, branchOf)] = append(byBranch[strandOf(event, branchOf)], event)
	}

	strands := make([]Strand, 0, len(byBranch))
	for branch, grouped := range byBranch {
		SortEvents(grouped)
		strands = append(strands, Strand{Branch: branch, Events: grouped})
	}
	sort.SliceStable(strands, func(i, j int) bool { return strands[i].Branch < strands[j].Branch })
	return strands
}

func strandOf(event Event, branchOf map[PRKey]string) string {
	switch typed := event.(type) {
	case BranchFirstSeen:
		return typed.Name
	case PROpened:
		return typed.Branch
	case PRMerged:
		return branchOf[typed.PR]
	case PRClosed:
		return branchOf[typed.PR]
	case PRReopened:
		return branchOf[typed.PR]
	case PRDraftChanged:
		return branchOf[typed.PR]
	case ReviewRequested:
		return branchOf[typed.PR]
	case ReviewRequestRemoved:
		return branchOf[typed.PR]
	case ReviewSubmitted:
		return branchOf[typed.PR]
	default:
		// Status changes belong to the issue rather than to any branch, and
		// are replayed into every strand by the caller.
		return ""
	}
}
