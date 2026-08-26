package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The GraphQL fake answers from the same REST fixtures the REST fake uses.
//
// That is the point. Duplicating the fixtures would let the two transports
// drift apart in exactly the place a test is supposed to catch it: the whole
// claim of this change is that the transports produce identical domain events,
// and a claim tested against two separately-maintained fixture sets is not
// tested at all.

var aliasPattern = regexp.MustCompile(`p(\d+): pullRequest\(number:(\d+)\)`)

// graphQLMode controls how the fake misbehaves, so the fallback paths get
// exercised rather than merely written.
type graphQLMode int

const (
	graphQLNormal graphQLMode = iota
	// graphQLAlwaysTruncated inflates every totalCount so the adapter refetches
	// every pull request over REST. Running the same assertions in this mode is
	// what proves the two transports agree.
	graphQLAlwaysTruncated
	// graphQLPartialFailure returns data and errors together, which GraphQL
	// does with a 200.
	graphQLPartialFailure
	// graphQLOmitsAPull drops one pull request from the answer without saying
	// so — the silent gap the adapter must not believe.
	graphQLOmitsAPull
	// graphQLForbidden is what a credential that may not use the GraphQL API
	// returns: a 200 carrying FORBIDDEN errors with no path.
	graphQLForbidden
	// graphQLForbidsOnePull denies a single pull request by path, leaving the
	// rest of the batch perfectly good.
	graphQLForbidsOnePull
	// graphQLHTTPForbidden refuses at the transport layer instead.
	graphQLHTTPForbidden
)

func serveGraphQL(w http.ResponseWriter, r *http.Request, mode graphQLMode) {
	var body struct {
		Query string `json:"query"`
	}
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if mode == graphQLHTTPForbidden {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)
		return
	}

	if mode == graphQLForbidden {
		io.WriteString(w, `{"data":{"repository":null},"errors":[
			{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"},
			{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"}]}`)
		return
	}

	if mode == graphQLPartialFailure {
		io.WriteString(w, `{"data":{"repository":{"p0":null}},
			"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
		return
	}

	matches := aliasPattern.FindAllStringSubmatch(body.Query, -1)
	entries := make([]string, 0, len(matches))

	for _, match := range matches {
		alias, number := match[1], match[2]
		// alias is the bare index from the capture group, not the "p" prefix.
		if mode == graphQLOmitsAPull && alias == "0" {
			continue
		}
		entries = append(entries, fmt.Sprintf("%q:%s", "p"+alias, pullAsGraphQL(number, mode)))
	}

	errors := ""
	if mode == graphQLForbidsOnePull && len(matches) > 0 {
		// One pull request denied by path, the rest answered normally. This is
		// how GraphQL reports a partially readable batch.
		errors = `,"errors":[{"type":"FORBIDDEN",
			"message":"Resource not accessible by personal access token",
			"path":["repository","p0"]}]`
	}

	fmt.Fprintf(w, `{"data":{"rateLimit":{"cost":9,"remaining":4991,"resetAt":"2026-08-26T05:00:00Z"},
		"repository":{%s}}%s}`, strings.Join(entries, ","), errors)
}

// pullAsGraphQL rebuilds one pull request's REST fixtures in GraphQL's shape.
func pullAsGraphQL(number string, mode graphQLMode) string {
	reviews := graphQLReviews(fixtures["/repos/org/repo/pulls/"+number+"/reviews"])
	timeline := graphQLTimeline(fixtures["/repos/org/repo/issues/"+number+"/timeline"])
	commit := graphQLFirstCommit(fixtures["/repos/org/repo/pulls/"+number+"/commits"])

	more := "false"
	if mode == graphQLAlwaysTruncated {
		more = "true"
	}

	// totalCount is deliberately reported as larger than the nodes returned,
	// the way a filtered timeline connection really does report it. Nothing may
	// read it as a truncation signal.
	return fmt.Sprintf(`{"number":%s,
		"commits":{"totalCount":1,"nodes":%s},
		"reviews":{"totalCount":%d,"pageInfo":{"hasNextPage":%s},"nodes":[%s]},
		"timelineItems":{"totalCount":%d,"pageInfo":{"hasNextPage":%s},"nodes":[%s]}}`,
		number, commit,
		len(reviews)+50, more, strings.Join(reviews, ","),
		len(timeline)+50, more, strings.Join(timeline, ","))
}

func graphQLReviews(restJSON string) []string {
	var rest []reviewJSON
	if restJSON != "" {
		_ = json.Unmarshal([]byte(restJSON), &rest)
	}

	out := make([]string, 0, len(rest))
	for _, review := range rest {
		submitted := "null"
		if review.SubmittedAt != nil {
			submitted = strconv.Quote(review.SubmittedAt.Format(time.RFC3339))
		}
		out = append(out, fmt.Sprintf(`{"state":%q,"submittedAt":%s,
			"author":{"login":%q,"__typename":%q}}`,
			review.State, submitted, review.User.Login, review.User.Type))
	}
	return out
}

func graphQLTimeline(restJSON string) []string {
	var rest []timelineEventJSON
	if restJSON != "" {
		_ = json.Unmarshal([]byte(restJSON), &rest)
	}

	out := make([]string, 0, len(rest))
	for _, event := range rest {
		typeName, ok := graphQLTypeName(event.Event)
		if !ok {
			// itemTypes filters these server-side, so the fake drops them too.
			continue
		}
		reviewer := ""
		if event.RequestedReviewer != nil {
			reviewer = fmt.Sprintf(`,"requestedReviewer":{"__typename":%q,"login":%q}`,
				event.RequestedReviewer.Type, event.RequestedReviewer.Login)
		}
		out = append(out, fmt.Sprintf(`{"__typename":%q,"createdAt":%q%s}`,
			typeName, event.CreatedAt.Format(time.RFC3339), reviewer))
	}
	return out
}

func graphQLFirstCommit(restJSON string) string {
	var rest []commitJSON
	if restJSON != "" {
		_ = json.Unmarshal([]byte(restJSON), &rest)
	}
	if len(rest) == 0 {
		return "[]"
	}
	return fmt.Sprintf(`[{"commit":{"committedDate":%q}}]`,
		rest[0].Commit.Committer.Date.Format(time.RFC3339))
}

// graphQLTypeName is restEventName backwards, kept here rather than exported
// from the adapter so a mistake in one is not mirrored into the other.
func graphQLTypeName(event string) (string, bool) {
	switch event {
	case eventReviewRequested:
		return "ReviewRequestedEvent", true
	case eventReviewRequestRemoved:
		return "ReviewRequestRemovedEvent", true
	case eventReadyForReview:
		return "ReadyForReviewEvent", true
	case eventConvertToDraft:
		return "ConvertToDraftEvent", true
	case eventMerged:
		return "MergedEvent", true
	case eventClosed:
		return "ClosedEvent", true
	case eventReopened:
		return "ReopenedEvent", true
	default:
		return "", false
	}
}
