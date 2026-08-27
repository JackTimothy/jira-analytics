package domain

import "testing"

var (
	prA = PRKey{Repo: "org/repo", Number: 1}
	prB = PRKey{Repo: "org/repo", Number: 2}
)

func strandNames(strands []Strand) []string {
	names := make([]string, 0, len(strands))
	for _, strand := range strands {
		names = append(names, strand.Branch)
	}
	return names
}

func TestSplitByBranchGroupsPullRequestEventsWithTheirBranch(t *testing.T) {
	events := []Event{
		BranchFirstSeen{At: at(1), Name: "org/repo:one"},
		PROpened{At: at(2), PR: prA, Branch: "org/repo:one"},
		BranchFirstSeen{At: at(3), Name: "org/repo:two"},
		PROpened{At: at(4), PR: prB, Branch: "org/repo:two"},
		// Neither of these names a branch; both must follow their pull request.
		ReviewRequested{At: at(5), PR: prB, Actor: "alice"},
		PRMerged{At: at(6), PR: prA},
	}

	strands := SplitByBranch(events)

	if got := strandNames(strands); len(got) != 2 || got[0] != "org/repo:one" || got[1] != "org/repo:two" {
		t.Fatalf("strands = %v, want the two branches in name order", got)
	}
	if len(strands[0].Events) != 3 {
		t.Errorf("branch one has %+v, want its branch, its pull request and the merge", strands[0].Events)
	}
	if len(strands[1].Events) != 3 {
		t.Errorf("branch two has %+v, want its branch, its pull request and the review", strands[1].Events)
	}
}

// A pull request that opened before the scan window has no PROpened event, so
// nothing ties it to a branch. Dropping its review would be worse than filing it
// unattributed.
func TestSplitByBranchKeepsEventsWhoseBranchIsUnknown(t *testing.T) {
	events := []Event{
		BranchFirstSeen{At: at(1), Name: "org/repo:one"},
		ReviewSubmitted{At: at(2), PR: prB, Actor: "alice", State: ReviewerApproved},
	}

	strands := SplitByBranch(events)

	var found bool
	for _, strand := range strands {
		for range strand.Events {
			if strand.Branch == "" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("the unattributed review was dropped: %v", strandNames(strands))
	}
}

func TestSplitByBranchOrdersEachStrandInTime(t *testing.T) {
	events := []Event{
		PRMerged{At: at(9), PR: prA},
		BranchFirstSeen{At: at(1), Name: "org/repo:one"},
		PROpened{At: at(4), PR: prA, Branch: "org/repo:one"},
	}

	strands := SplitByBranch(events)

	if len(strands) != 1 {
		t.Fatalf("strands = %v, want one", strandNames(strands))
	}
	for i := 1; i < len(strands[0].Events); i++ {
		if strands[0].Events[i].When().Before(strands[0].Events[i-1].When()) {
			t.Fatalf("strand events out of order: %+v", strands[0].Events)
		}
	}
}

func TestSplitByBranchOnNoEventsIsEmpty(t *testing.T) {
	if strands := SplitByBranch(nil); len(strands) != 0 {
		t.Errorf("strands = %v, want none", strandNames(strands))
	}
}
