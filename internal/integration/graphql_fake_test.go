package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// The GraphQL fake answers from the same REST fixtures the rest of the file
// uses, so the end-to-end assertions cover the transport the app actually takes
// without a second fixture set that could drift away from the first.

var aliasPattern = regexp.MustCompile(`p(\d+): pullRequest\(number:(\d+)\)`)

func serveGraphQL(w http.ResponseWriter, r *http.Request, routes map[string]string) {
	var body struct {
		Query string `json:"query"`
	}
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	entries := make([]string, 0, 4)
	for _, match := range aliasPattern.FindAllStringSubmatch(body.Query, -1) {
		alias, number := match[1], match[2]
		entries = append(entries, fmt.Sprintf(`"p%s":%s`, alias, pullAsGraphQL(number, routes)))
	}

	fmt.Fprintf(w, `{"data":{"rateLimit":{"cost":9,"remaining":4991,"resetAt":"2026-08-26T05:00:00Z"},
		"repository":{%s}}}`, strings.Join(entries, ","))
}

func pullAsGraphQL(number string, routes map[string]string) string {
	reviews := reviewsAsGraphQL(routes["/repos/acme/service/pulls/"+number+"/reviews"])
	timeline := timelineAsGraphQL(routes["/repos/acme/service/issues/"+number+"/timeline"])
	commits := firstCommitAsGraphQL(routes["/repos/acme/service/pulls/"+number+"/commits"])

	return fmt.Sprintf(`{"number":%s,
		"commits":{"totalCount":1,"nodes":%s},
		"reviews":{"totalCount":%d,"nodes":[%s]},
		"timelineItems":{"totalCount":%d,"nodes":[%s]}}`,
		number, commits,
		len(reviews), strings.Join(reviews, ","),
		len(timeline), strings.Join(timeline, ","))
}

func reviewsAsGraphQL(restJSON string) []string {
	var rest []struct {
		User struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
		State       string     `json:"state"`
		SubmittedAt *time.Time `json:"submitted_at"`
	}
	if restJSON != "" {
		_ = json.Unmarshal([]byte(restJSON), &rest)
	}

	out := make([]string, 0, len(rest))
	for _, review := range rest {
		submitted := "null"
		if review.SubmittedAt != nil {
			submitted = `"` + review.SubmittedAt.Format(time.RFC3339) + `"`
		}
		out = append(out, fmt.Sprintf(`{"state":%q,"submittedAt":%s,"author":{"login":%q,"__typename":%q}}`,
			review.State, submitted, review.User.Login, review.User.Type))
	}
	return out
}

func timelineAsGraphQL(restJSON string) []string {
	var rest []struct {
		Event             string    `json:"event"`
		CreatedAt         time.Time `json:"created_at"`
		RequestedReviewer *struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"requested_reviewer"`
	}
	if restJSON != "" {
		_ = json.Unmarshal([]byte(restJSON), &rest)
	}

	typeNames := map[string]string{
		"review_requested":       "ReviewRequestedEvent",
		"review_request_removed": "ReviewRequestRemovedEvent",
		"ready_for_review":       "ReadyForReviewEvent",
		"convert_to_draft":       "ConvertToDraftEvent",
		"merged":                 "MergedEvent",
		"closed":                 "ClosedEvent",
		"reopened":               "ReopenedEvent",
	}

	out := make([]string, 0, len(rest))
	for _, event := range rest {
		typeName, ok := typeNames[event.Event]
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

func firstCommitAsGraphQL(restJSON string) string {
	var rest []struct {
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if restJSON != "" {
		_ = json.Unmarshal([]byte(restJSON), &rest)
	}
	if len(rest) == 0 {
		return "[]"
	}
	return fmt.Sprintf(`[{"commit":{"committedDate":%q}}]`, rest[0].Commit.Committer.Date.Format(time.RFC3339))
}
