package github

import (
	"testing"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

var testPRKey = domain.PRKey{Repo: "org/repo", Number: 7}

func when(hour int) time.Time { return time.Date(2026, 8, 5, hour, 0, 0, 0, time.UTC) }

func ptr(t time.Time) *time.Time { return &t }

func TestDraftAtCreation(t *testing.T) {
	tests := []struct {
		name     string
		pull     pullJSON
		timeline []timelineEventJSON
		want     bool
	}{
		{
			name:     "no transitions: it has always been what it is now",
			pull:     pullJSON{Draft: true},
			timeline: nil,
			want:     true,
		},
		{
			name:     "marked ready later, so it began as a draft",
			pull:     pullJSON{Draft: false},
			timeline: []timelineEventJSON{{Event: eventReadyForReview, CreatedAt: when(10)}},
			want:     true,
		},
		{
			name:     "converted to draft later, so it began ready",
			pull:     pullJSON{Draft: true},
			timeline: []timelineEventJSON{{Event: eventConvertToDraft, CreatedAt: when(10)}},
			want:     false,
		},
		{
			name: "only the earliest transition matters",
			pull: pullJSON{Draft: false},
			timeline: []timelineEventJSON{
				{Event: eventReadyForReview, CreatedAt: when(10)},
				{Event: eventConvertToDraft, CreatedAt: when(11)},
				{Event: eventReadyForReview, CreatedAt: when(12)},
			},
			want: true,
		},
		{
			name: "unrelated events are ignored",
			pull: pullJSON{Draft: false},
			timeline: []timelineEventJSON{
				{Event: "labeled", CreatedAt: when(9)},
				{Event: eventConvertToDraft, CreatedAt: when(10)},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := draftAtCreation(tc.pull, tc.timeline); got != tc.want {
				t.Errorf("draftAtCreation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTimelineEventsSkipsTeamRequestsAndBots(t *testing.T) {
	policy := domain.ReviewerPolicy{ExcludeBots: true}
	timeline := []timelineEventJSON{
		{Event: eventReviewRequested, CreatedAt: when(9), RequestedReviewer: &userJSON{Login: "alice", Type: "User"}},
		// A team request has no individual reviewer at all.
		{Event: eventReviewRequested, CreatedAt: when(10), RequestedReviewer: nil},
		{Event: eventReviewRequested, CreatedAt: when(11), RequestedReviewer: &userJSON{Login: "helper[bot]"}},
		{Event: "labeled", CreatedAt: when(12)},
	}

	events := timelineEvents(testPRKey, timeline, policy)

	if len(events) != 1 {
		t.Fatalf("got %d events, want only the human request: %+v", len(events), events)
	}
	requested, ok := events[0].(domain.ReviewRequested)
	if !ok || requested.Actor != "alice" {
		t.Errorf("unexpected event %+v", events[0])
	}
}

func TestTimelineEventsMapsLifecycle(t *testing.T) {
	timeline := []timelineEventJSON{
		{Event: eventConvertToDraft, CreatedAt: when(9)},
		{Event: eventReadyForReview, CreatedAt: when(10)},
		{Event: eventClosed, CreatedAt: when(11)},
		{Event: eventReopened, CreatedAt: when(12)},
		{Event: eventMerged, CreatedAt: when(13)},
	}

	events := timelineEvents(testPRKey, timeline, domain.ReviewerPolicy{})

	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}
	if draft, ok := events[0].(domain.PRDraftChanged); !ok || !draft.Draft {
		t.Errorf("event 0 = %+v, want a conversion to draft", events[0])
	}
	if _, ok := events[4].(domain.PRMerged); !ok {
		t.Errorf("event 4 = %+v, want a merge", events[4])
	}
}

func TestReviewEvents(t *testing.T) {
	policy := domain.ReviewerPolicy{ExcludeBots: true, ExcludeLogins: []string{"ai-reviewer"}}
	reviews := []reviewJSON{
		{User: userJSON{Login: "alice", Type: "User"}, State: reviewApproved, SubmittedAt: ptr(when(9))},
		{User: userJSON{Login: "bob", Type: "User"}, State: reviewChangesRequested, SubmittedAt: ptr(when(10))},
		{User: userJSON{Login: "carol", Type: "User"}, State: reviewCommented, SubmittedAt: ptr(when(11))},
		// A dismissed verdict no longer counts as an approval.
		{User: userJSON{Login: "dave", Type: "User"}, State: reviewDismissed, SubmittedAt: ptr(when(12))},
		// Pending reviews are unsubmitted drafts only their author can see.
		{User: userJSON{Login: "erin", Type: "User"}, State: reviewPending},
		{User: userJSON{Login: "coverage", Type: "Bot"}, State: reviewApproved, SubmittedAt: ptr(when(13))},
		{User: userJSON{Login: "ai-reviewer", Type: "User"}, State: reviewApproved, SubmittedAt: ptr(when(14))},
	}

	events := reviewEvents(testPRKey, reviews, policy)

	if len(events) != 4 {
		t.Fatalf("got %d events, want 4 human submissions: %+v", len(events), events)
	}

	want := []domain.ReviewerState{
		domain.ReviewerApproved,
		domain.ReviewerChangesRequested,
		domain.ReviewerCommented,
		domain.ReviewerRequested, // dismissed
	}
	for i, expected := range want {
		submitted, ok := events[i].(domain.ReviewSubmitted)
		if !ok {
			t.Fatalf("event %d is %T", i, events[i])
		}
		if submitted.State != expected {
			t.Errorf("event %d state = %v, want %v", i, submitted.State, expected)
		}
	}
}
