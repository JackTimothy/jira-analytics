package github

import (
	"fmt"
	"sort"
	"strings"
	"time"
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

// partition splits GraphQL errors into those that name one aliased pull request
// and those that describe the query as a whole.
//
// The distinction decides everything downstream. GraphQL answers partially by
// design: a query about ten pull requests can fail on two of them and return
// the other eight perfectly well. Treating that as a failed query throws away
// eight good answers and, worse, makes one inaccessible pull request look like
// a broken deployment.
func (e graphQLEnvelope) partition() (perAlias map[string]string, global []string) {
	perAlias = map[string]string{}

	for _, item := range e.Errors {
		message := strings.TrimSpace(item.Type + " " + item.Message)
		if alias, ok := item.alias(); ok {
			perAlias[alias] = message
			continue
		}
		global = append(global, message)
	}
	sort.Strings(global)
	return perAlias, global
}

// alias reads the pull request alias out of an error path such as
// ["repository", "p3"].
func (e graphQLErrorJSON) alias() (string, bool) {
	for _, step := range e.Path {
		name, ok := step.(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "p") && len(name) > 1 {
			if _, numeric := aliasIndex(name); numeric {
				return name, true
			}
		}
	}
	return "", false
}

// deniesEverything reports whether the errors say this deployment cannot use
// the batched query at all, as opposed to this query having gone wrong.
//
// A token that cannot reach the GraphQL API says so on every request, so
// retrying it on every build would be a permanent tax for a permanent fact.
func deniesEverything(global []string) bool {
	if len(global) == 0 {
		return false
	}
	for _, message := range global {
		switch {
		case strings.HasPrefix(message, "FORBIDDEN"),
			strings.HasPrefix(message, "UNAUTHORIZED"),
			strings.Contains(message, "not accessible by personal access token"),
			strings.Contains(message, "Resource not accessible by integration"):
		default:
			return false
		}
	}
	return true
}

type graphPullJSON struct {
	Number  int `json:"number"`
	Commits struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Commit struct {
				AuthoredDate  time.Time `json:"authoredDate"`
				CommittedDate time.Time `json:"committedDate"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		TotalCount int                 `json:"totalCount"`
		PageInfo   pageInfoJSON        `json:"pageInfo"`
		Nodes      []graphQLReviewJSON `json:"nodes"`
	} `json:"reviews"`
	TimelineItems struct {
		TotalCount int                   `json:"totalCount"`
		PageInfo   pageInfoJSON          `json:"pageInfo"`
		Nodes      []graphQLTimelineJSON `json:"nodes"`
	} `json:"timelineItems"`
}

type pageInfoJSON struct {
	HasNextPage bool `json:"hasNextPage"`
}

// truncated reports whether the caps cut anything off.
//
// hasNextPage, not totalCount. The timeline connection is filtered by
// itemTypes, and its totalCount counts the whole timeline — comments, labels,
// commits, cross-references — rather than the seven event types asked for. A
// busy pull request therefore reports a total far larger than the nodes it
// returned while having lost nothing at all, which made this fire on every pull
// request in a real repository and quietly sent all of them back to the REST
// path the batch exists to avoid.
//
// hasNextPage is scoped to the connection as filtered, so it answers the
// question actually being asked: is there more of what I requested.
//
// Commits are excluded either way: only the first is ever wanted, so a pull
// request with two commits has a next page and has lost nothing.
func (p graphPullJSON) truncated() bool {
	return p.Reviews.PageInfo.HasNextPage || p.TimelineItems.PageInfo.HasNextPage
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
		commit := p.Commits.Nodes[0].Commit
		// The earlier of the two, for the same reason the REST path does it:
		// a rebase moves the committer date to the rebase.
		detail.FirstCommit = earlier(commit.AuthoredDate, commit.CommittedDate)
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
