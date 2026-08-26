package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
)

// The GraphQL path exists for one reason: reading a pull request's timeline,
// reviews and first commit over REST costs three requests each, and a sprint's
// worth of them is a hundred-odd round trips that no amount of concurrency
// flattens. One GraphQL query answers ten pull requests at once.
//
// Only that part moves. The pull request *listing* stays on REST because
// GraphQL pages by cursor — each page needs the previous page's endCursor — so
// listing two thousand pull requests would be twenty sequential round trips per
// repository, where REST takes a page number and can fetch four at once.

const (
	// graphQLBatchSize is how many pull requests go into one query. Ten keeps
	// the node count — which is what GitHub actually bills — comfortably small
	// while removing almost all the round trips.
	graphQLBatchSize = 10

	// The caps below bound what one query costs. GitHub charges roughly the sum
	// of every connection's `first:` divided by a hundred, so asking for 100
	// reviews and 100 timeline items on 10 pull requests would cost ~200 points
	// of a 5,000/hour budget in a single request. These caps cost ~9.
	//
	// A pull request that exceeds them is detected by totalCount and refetched
	// over REST, so the caps trade a rare extra request for a much cheaper
	// common case rather than trading away correctness.
	graphQLReviewCap   = 30
	graphQLTimelineCap = 60
)

// timelineItemTypes filters the timeline server-side to the events that can
// change a state. It is the same set translate.go already filters for, applied
// one layer earlier — which both shrinks the response and, because `first:` is
// applied after this filter, means the cap counts only events that matter
// rather than being consumed by comment noise.
const timelineItemTypes = `REVIEW_REQUESTED_EVENT, REVIEW_REQUEST_REMOVED_EVENT, ` +
	`READY_FOR_REVIEW_EVENT, CONVERT_TO_DRAFT_EVENT, MERGED_EVENT, CLOSED_EVENT, REOPENED_EVENT`

// PullDetailQuery builds the query for a batch of pull requests.
//
// It is exported so cmd/probe can validate the exact query that ships. A probe
// that checks a hand-copied approximation of the real query proves nothing.
func PullDetailQuery(numbers []int) string {
	var b strings.Builder

	b.WriteString("query($owner:String!,$name:String!){\n")
	b.WriteString("  rateLimit{ cost remaining resetAt }\n")
	b.WriteString("  repository(owner:$owner,name:$name){\n")
	for i, number := range numbers {
		fmt.Fprintf(&b, "    p%d: pullRequest(number:%d){ ...prDetail }\n", i, number)
	}
	b.WriteString("  }\n}\n")

	fmt.Fprintf(&b, `
fragment prDetail on PullRequest {
  number
  commits(first:1){ totalCount nodes{ commit{ committedDate } } }
  reviews(first:%d){
    totalCount
    nodes{ state submittedAt author{ login __typename } }
  }
  timelineItems(first:%d, itemTypes:[%s]){
    totalCount
    nodes{
      __typename
      ... on ReviewRequestedEvent      { createdAt requestedReviewer{ ...actor } }
      ... on ReviewRequestRemovedEvent { createdAt requestedReviewer{ ...actor } }
      ... on ReadyForReviewEvent       { createdAt }
      ... on ConvertToDraftEvent       { createdAt }
      ... on MergedEvent               { createdAt }
      ... on ClosedEvent               { createdAt }
      ... on ReopenedEvent             { createdAt }
    }
  }
}

fragment actor on RequestedReviewer {
  __typename
  ... on User { login }
  ... on Bot  { login }
}
`, graphQLReviewCap, graphQLTimelineCap, timelineItemTypes)

	return b.String()
}

// GraphQLEndpoint derives the GraphQL URL from the REST base.
//
// GitHub Enterprise serves REST at /api/v3 and GraphQL at /api/graphql, a
// sibling rather than a child, which is the one case a naive suffix would get
// wrong.
func GraphQLEndpoint(restBase string) string {
	base := strings.TrimSuffix(restBase, "/")
	if trimmed, found := strings.CutSuffix(base, "/api/v3"); found {
		return trimmed + "/api/graphql"
	}
	return base + "/graphql"
}

func (c *CodeHost) graphQLRequest(query string, variables map[string]any) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
		if err != nil {
			return nil, fmt.Errorf("encoding query: %w", err)
		}

		req, err := http.NewRequest(http.MethodPost, GraphQLEndpoint(c.config.BaseURL), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		if c.config.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.config.Token)
		}
		return req, nil
	}
}

// pullDetails fetches a batch of pull requests in one query.
//
// It returns the details keyed by pull request number, and the numbers whose
// data did not arrive usable and must be refetched over REST — because the
// response was truncated, because the answer omitted them, or because this
// token may not read them. All three are the same instruction to the caller.
func (c *CodeHost) pullDetails(ctx context.Context, repo domain.RepoRef, numbers []int) (map[int]pullDetail, []int, error) {
	if len(numbers) == 0 {
		return nil, nil, nil
	}

	var envelope graphQLEnvelope
	build := c.graphQLRequest(PullDetailQuery(numbers), map[string]any{
		"owner": repo.Owner,
		"name":  repo.Name,
	})
	if err := c.client.DoJSON(ctx, build, &envelope); err != nil {
		// A token the GraphQL endpoint will not serve at all rejects every
		// request the same way, so this is a fact about the deployment rather
		// than about this query.
		if refusesGraphQL(err) {
			c.refuseGraphQL(err)
			return nil, numbers, nil
		}
		return nil, nil, fmt.Errorf("querying pull request details for %s: %w", repo, err)
	}

	// GraphQL answers 200 OK with an errors array beside partial data, and
	// httpclient treats any 200 as success — so the errors are read before
	// anything in data is believed.
	perAlias, global := envelope.partition()
	if len(global) > 0 {
		if deniesEverything(global) {
			c.refuseGraphQL(fmt.Errorf("%s", strings.Join(global, "; ")))
			return nil, numbers, nil
		}
		return nil, nil, fmt.Errorf("querying %s: %s", repo, strings.Join(global, "; "))
	}

	c.recordRateLimit(envelope.Data.RateLimit)

	details := make(map[int]pullDetail, len(numbers))
	var refetch []int

	// A pull request this token may not read individually is refetched over
	// REST, where it may well be readable — the two APIs do not always agree
	// about what a token can see. If REST refuses it too, that error surfaces
	// there, which is the right place for it.
	for alias := range perAlias {
		if index, ok := aliasIndex(alias); ok && index < len(numbers) {
			refetch = append(refetch, numbers[index])
		}
	}

	for alias, raw := range envelope.Data.Repository {
		if raw == nil {
			// Null with no error of its own: nothing says why, so refetching is
			// the only reading that cannot silently lose a pull request.
			if index, ok := aliasIndex(alias); ok && index < len(numbers) {
				refetch = append(refetch, numbers[index])
			}
			continue
		}
		if raw.truncated() {
			refetch = append(refetch, raw.Number)
			continue
		}
		detail, err := raw.toDetail()
		if err != nil {
			return nil, nil, fmt.Errorf("%s %s: %w", repo, alias, err)
		}
		details[raw.Number] = detail
	}

	// Map iteration is unordered; a caller comparing two runs should not see
	// the refetch list shuffle.
	sort.Ints(refetch)
	return details, dedupeInts(refetch), nil
}

func dedupeInts(values []int) []int {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

// refusesGraphQL reports whether an HTTP failure says this deployment cannot
// use the GraphQL endpoint at all.
func refusesGraphQL(err error) bool {
	var status *httpclient.StatusError
	if !errors.As(err, &status) {
		return false
	}
	switch status.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// refuseGraphQL records that the batched query is unavailable here and says so
// once.
//
// Once, because it is a standing fact about the credential; loudly, because the
// consequence is a hundred extra requests on every build and nothing else would
// explain why the retrospective is slow again.
func (c *CodeHost) refuseGraphQL(cause error) {
	c.rateLimitMu.Lock()
	already := c.graphQLRefused
	c.graphQLRefused = true
	c.rateLimitMu.Unlock()

	if already {
		return
	}
	c.logger.Warn("this GitHub credential cannot use the batched GraphQL query",
		slog.String("consequence", "pull request detail now costs three requests each, about a hundred per retrospective"),
		slog.String("remedy", "a classic token with repo scope, or a fine-grained token granted Pull requests: Read and Contents: Read"),
		slog.Any("error", cause))
}

func (c *CodeHost) graphQLKnownRefused() bool {
	c.rateLimitMu.Lock()
	defer c.rateLimitMu.Unlock()
	return c.graphQLRefused
}

// recordRateLimit keeps the cost of a build observable. GitHub bills GraphQL by
// computed node cost rather than by request, so "how many requests did this
// make" says nothing about how close the budget is to running out.
func (c *CodeHost) recordRateLimit(limit *rateLimitJSON) {
	if limit == nil {
		return
	}
	c.rateLimitMu.Lock()
	defer c.rateLimitMu.Unlock()

	c.graphQLCost += limit.Cost
	c.graphQLRemaining = limit.Remaining
	c.graphQLResetAt = limit.ResetAt
}

// GraphQLBudget reports what this process has spent on GraphQL and what the
// server says is left.
func (c *CodeHost) GraphQLBudget() (spent, remaining int, resetAt time.Time) {
	c.rateLimitMu.Lock()
	defer c.rateLimitMu.Unlock()
	return c.graphQLCost, c.graphQLRemaining, c.graphQLResetAt
}

func batchNumbers(numbers []int, size int) [][]int {
	batches := make([][]int, 0, (len(numbers)+size-1)/size)
	for start := 0; start < len(numbers); start += size {
		end := start + size
		if end > len(numbers) {
			end = len(numbers)
		}
		batches = append(batches, numbers[start:end])
	}
	return batches
}

// aliasIndex recovers the batch position from an alias like "p3", for error
// messages that name something the reader can find.
func aliasIndex(alias string) (int, bool) {
	digits, found := strings.CutPrefix(alias, "p")
	if !found {
		return 0, false
	}
	index, err := strconv.Atoi(digits)
	return index, err == nil
}
