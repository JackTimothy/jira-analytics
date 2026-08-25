package github

import "time"

// GitHub's JSON, kept in this package so its shape never leaks inward.

type repoJSON struct {
	DefaultBranch string `json:"default_branch"`
}

type branchJSON struct {
	Name string `json:"name"`
}

type compareJSON struct {
	Commits []commitJSON `json:"commits"`
}

type commitJSON struct {
	Commit struct {
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

type userJSON struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type pullJSON struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Draft     bool       `json:"draft"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	MergedAt  *time.Time `json:"merged_at"`
	Head      struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type reviewJSON struct {
	User        userJSON   `json:"user"`
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submitted_at"`
}

type timelineEventJSON struct {
	Event             string    `json:"event"`
	CreatedAt         time.Time `json:"created_at"`
	Actor             userJSON  `json:"actor"`
	RequestedReviewer *userJSON `json:"requested_reviewer"`
}

// Timeline event names used to reconstruct a pull request's history.
const (
	eventReviewRequested      = "review_requested"
	eventReviewRequestRemoved = "review_request_removed"
	eventReadyForReview       = "ready_for_review"
	eventConvertToDraft       = "convert_to_draft"
	eventClosed               = "closed"
	eventReopened             = "reopened"
	eventMerged               = "merged"
)

// Review states as GitHub reports them.
const (
	reviewApproved         = "APPROVED"
	reviewChangesRequested = "CHANGES_REQUESTED"
	reviewCommented        = "COMMENTED"
	reviewDismissed        = "DISMISSED"
	reviewPending          = "PENDING"
)
