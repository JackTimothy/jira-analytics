package github

import (
	"fmt"
	"strings"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// These types decode GraphQL into the shapes the REST path already produces.
//
// That is deliberate and is the main reason this swap is safe: translate.go
// turns reviewJSON and timelineEventJSON into domain events, and it does not
// change at all. Both transports therefore go through one implementation of
// what a review state means and which timeline events matter, so they cannot
// drift apart — which a second translation layer would eventually let them do.

type graphQLEnvelope struct {
	Data struct {
		RateLimit  *rateLimitJSON            `json:"rateLimit"`
		Repository map[string]*graphPullJSON `json:"repository"`
	} `json:"data"`
	Errors []graphQLErrorJSON `json:"errors"`
}

type rateLimitJSON struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

type graphQLErrorJSON struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

// err turns a GraphQL error array into one Go error naming every problem.
//
// All of them, not the first: a query about ten pull requests that fails on
// three should say which three, since the usual cause is something structural
// about the query rather than about any one pull request.
func (e graphQLEnvelope) err(repo domain.RepoRef) error {
	if len(e.Errors) == 0 {
		return nil
	}
	messages := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		if item.Type != "" {
			messages = append(messages, item.Type+": "+item.Message)
			continue
		}
		messages = append(messages, item.Message)
	}
	return fmt.Errorf("querying %s: %s", repo, strings.Join(messages, "; "))
}

type graphPullJSON struct {
	Number  int `json:"number"`
	Commits struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Commit struct {
				CommittedDate time.Time `json:"committedDate"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		TotalCount int                 `json:"totalCount"`
		Nodes      []graphQLReviewJSON `json:"nodes"`
	} `json:"reviews"`
	TimelineItems struct {
		TotalCount int                   `json:"totalCount"`
		Nodes      []graphQLTimelineJSON `json:"nodes"`
	} `json:"timelineItems"`
}

// truncated reports whether the caps cut anything off.
//
// Commits are excluded on purpose: only the first is ever asked for, so
// totalCount always exceeds it on a pull request with more than one commit and
// treating that as truncation would refetch every one of them.
func (p graphPullJSON) truncated() bool {
	return p.Reviews.TotalCount > len(p.Reviews.Nodes) ||
		p.TimelineItems.TotalCount > len(p.TimelineItems.Nodes)
}

type graphQLReviewJSON struct {
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submittedAt"`
	Author      *struct {
		Login    string `json:"login"`
		TypeName string `json:"__typename"`
	} `json:"author"`
}

type graphQLTimelineJSON struct {
	TypeName          string      `json:"__typename"`
	CreatedAt         time.Time   `json:"createdAt"`
	RequestedReviewer *graphActor `json:"requestedReviewer"`
}

type graphActor struct {
	TypeName string `json:"__typename"`
	Login    string `json:"login"`
}

// pullDetail is what one pull request contributes, in the REST path's own
// vocabulary.
type pullDetail struct {
	FirstCommit time.Time
	Reviews     []reviewJSON
	Timeline    []timelineEventJSON
}

func (p graphPullJSON) toDetail() (pullDetail, error) {
	detail := pullDetail{
		Reviews:  make([]reviewJSON, 0, len(p.Reviews.Nodes)),
		Timeline: make([]timelineEventJSON, 0, len(p.TimelineItems.Nodes)),
	}

	if len(p.Commits.Nodes) > 0 {
		detail.FirstCommit = p.Commits.Nodes[0].Commit.CommittedDate
	}

	for _, review := range p.Reviews.Nodes {
		converted := reviewJSON{State: review.State, SubmittedAt: review.SubmittedAt}
		if review.Author != nil {
			// A review whose author has since been deleted arrives with a null
			// author. Leaving the login empty is what isHumanReviewer already
			// treats as "not a person whose verdict counts".
			converted.User = userJSON{Login: review.Author.Login, Type: review.Author.TypeName}
		}
		detail.Reviews = append(detail.Reviews, converted)
	}

	for _, item := range p.TimelineItems.Nodes {
		name, ok := restEventName(item.TypeName)
		if !ok {
			// itemTypes already filtered server-side, so this means the schema
			// grew a type the query asked for and this code does not know.
			return pullDetail{}, fmt.Errorf("unrecognised timeline item %q", item.TypeName)
		}
		event := timelineEventJSON{Event: name, CreatedAt: item.CreatedAt}
		if item.RequestedReviewer != nil {
			// A request aimed at a Team carries no login, which reviewerFrom
			// already discards — there is nobody whose verdict could satisfy it.
			event.RequestedReviewer = &userJSON{
				Login: item.RequestedReviewer.Login,
				Type:  item.RequestedReviewer.TypeName,
			}
		}
		detail.Timeline = append(detail.Timeline, event)
	}

	return detail, nil
}

// restEventName maps a GraphQL timeline type to the REST event name, which is
// the vocabulary translate.go is written in.
func restEventName(typeName string) (string, bool) {
	switch typeName {
	case "ReviewRequestedEvent":
		return eventReviewRequested, true
	case "ReviewRequestRemovedEvent":
		return eventReviewRequestRemoved, true
	case "ReadyForReviewEvent":
		return eventReadyForReview, true
	case "ConvertToDraftEvent":
		return eventConvertToDraft, true
	case "MergedEvent":
		return eventMerged, true
	case "ClosedEvent":
		return eventClosed, true
	case "ReopenedEvent":
		return eventReopened, true
	default:
		return "", false
	}
}
