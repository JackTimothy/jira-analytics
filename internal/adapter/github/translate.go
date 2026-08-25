package github

import (
	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// draftAtCreation reconstructs whether a pull request was opened as a draft.
//
// The API reports only the current draft flag, so the earliest draft transition
// tells us what it was before: if the first such event marks it ready, it was
// created as a draft; if the first converts it to draft, it was created ready.
// With no transitions at all, it has always been what it is now.
func draftAtCreation(pull pullJSON, timeline []timelineEventJSON) bool {
	for _, event := range timeline {
		switch event.Event {
		case eventReadyForReview:
			return true
		case eventConvertToDraft:
			return false
		}
	}
	return pull.Draft
}

func timelineEvents(key domain.PRKey, timeline []timelineEventJSON, policy domain.ReviewerPolicy) []domain.Event {
	var events []domain.Event

	for _, event := range timeline {
		switch event.Event {
		case eventReviewRequested:
			if actor, ok := reviewerFrom(event.RequestedReviewer, policy); ok {
				events = append(events, domain.ReviewRequested{At: event.CreatedAt, PR: key, Actor: actor})
			}
		case eventReviewRequestRemoved:
			if actor, ok := reviewerFrom(event.RequestedReviewer, policy); ok {
				events = append(events, domain.ReviewRequestRemoved{At: event.CreatedAt, PR: key, Actor: actor})
			}
		case eventReadyForReview:
			events = append(events, domain.PRDraftChanged{At: event.CreatedAt, PR: key, Draft: false})
		case eventConvertToDraft:
			events = append(events, domain.PRDraftChanged{At: event.CreatedAt, PR: key, Draft: true})
		case eventMerged:
			events = append(events, domain.PRMerged{At: event.CreatedAt, PR: key})
		case eventClosed:
			events = append(events, domain.PRClosed{At: event.CreatedAt, PR: key})
		case eventReopened:
			events = append(events, domain.PRReopened{At: event.CreatedAt, PR: key})
		}
	}
	return events
}

// reviewerFrom filters out review requests aimed at teams or at automation. A
// team request carries no individual, so there is nobody whose verdict could
// ever satisfy the review states.
func reviewerFrom(reviewer *userJSON, policy domain.ReviewerPolicy) (domain.ActorID, bool) {
	if reviewer == nil {
		return "", false
	}
	if !isHumanReviewer(reviewer.Login, reviewer.Type, policy) {
		return "", false
	}
	return domain.ActorID(reviewer.Login), true
}

func reviewEvents(key domain.PRKey, reviews []reviewJSON, policy domain.ReviewerPolicy) []domain.Event {
	var events []domain.Event

	for _, review := range reviews {
		// A pending review has not been submitted, so it has no timestamp and
		// nobody but its author can see it.
		if review.SubmittedAt == nil || review.State == reviewPending {
			continue
		}
		if !isHumanReviewer(review.User.Login, review.User.Type, policy) {
			continue
		}

		state, ok := toReviewerState(review.State)
		if !ok {
			continue
		}
		events = append(events, domain.ReviewSubmitted{
			At:    *review.SubmittedAt,
			PR:    key,
			Actor: domain.ActorID(review.User.Login),
			State: state,
		})
	}
	return events
}

func toReviewerState(state string) (domain.ReviewerState, bool) {
	switch state {
	case reviewApproved:
		return domain.ReviewerApproved, true
	case reviewChangesRequested:
		return domain.ReviewerChangesRequested, true
	case reviewCommented:
		return domain.ReviewerCommented, true
	case reviewDismissed:
		// A dismissed review no longer carries a verdict, so the reviewer is
		// back to being awaited rather than counting as having approved.
		return domain.ReviewerRequested, true
	default:
		return 0, false
	}
}
