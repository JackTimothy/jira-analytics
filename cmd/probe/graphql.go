package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/adapter/github"
	"github.com/jacktimothy/jira-analytics/internal/infra/config"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
)

// The GraphQL probe checks the query the adapter actually sends.
//
// It calls github.PullDetailQuery rather than restating the query here, because
// a probe that validates a hand-copied approximation of the real query proves
// nothing about the real one.

type githubProber struct {
	config config.Config
	client *httpclient.Client
	repo   string
}

func probeGitHub(ctx context.Context, settings config.Config, client *httpclient.Client, repo string) {
	p := &githubProber{config: settings, client: client, repo: repo}

	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		fmt.Printf("✗ -repo must be owner/name, got %q\n\n", repo)
		return
	}

	numbers, err := p.recentPulls(ctx, owner, name)
	if err != nil {
		fmt.Printf("✗ could not list pull requests in %s\n  %v\n\n", repo, err)
		return
	}
	if len(numbers) == 0 {
		// Still worth saying whether the credential can use GraphQL at all —
		// that answer does not depend on the repository having pull requests.
		fmt.Printf("!  %s has no pull requests to probe with\n\n", repo)
		p.narrow(ctx, owner, name, 0)
		return
	}

	fmt.Printf("repo    %s\n        probing with pull requests %v\n\n", repo, numbers)

	answer, err := p.batch(ctx, owner, name, numbers)
	if err != nil {
		fmt.Printf("✗ the batched query\n  %v\n\n", err)
		p.narrow(ctx, owner, name, numbers[0])
		return
	}
	if len(answer.denied) > 0 {
		fmt.Printf("!  %d of %d pull requests were denied individually: %v\n"+
			"   those fall back to REST; the rest still come from one query\n\n",
			len(answer.denied), len(numbers), answer.denied)
	}
	fmt.Printf("✓ the batched query\n  %d pull requests answered in one request\n  ~105 requests per retrospective become ~4\n\n", len(answer.pulls))

	p.reportCommitOrdering(ctx, owner, name, numbers[0], answer)
	p.reportCost(answer, len(numbers))
	p.reportTruncation(answer)
}

// reportCommitOrdering is the check worth running above all the others.
//
// BranchFirstSeen is taken from a pull request's earliest commit, and the REST
// path gets it from an endpoint documented to list commits oldest first. If
// GraphQL's commits(first:1) orders differently, every bar on the chart starts
// at the wrong time and nothing about the result looks broken — which is why
// this compares the two rather than trusting either.
func (p *githubProber) reportCommitOrdering(ctx context.Context, owner, name string, number int, answer batchAnswer) {
	viaGraphQL, ok := answer.pulls[number]
	if !ok || viaGraphQL.firstCommit.IsZero() {
		fmt.Printf("✗ commit ordering\n  pull request #%d returned no commit over GraphQL\n\n", number)
		return
	}

	viaREST, err := p.firstCommitREST(ctx, owner, name, number)
	if err != nil {
		fmt.Printf("✗ commit ordering\n  could not read the REST commits for #%d: %v\n\n", number, err)
		return
	}

	if !viaGraphQL.firstCommit.Equal(viaREST) {
		fmt.Printf("✗ commit ordering — DO NOT SHIP THE GRAPHQL PATH\n"+
			"  GraphQL commits(first:1) gave %s\n"+
			"  REST  commits?per_page=1  gave %s\n"+
			"  consequence: every branch-start time on the chart would be wrong,\n"+
			"  and nothing about the chart would look broken\n\n",
			viaGraphQL.firstCommit.Format(time.RFC3339), viaREST.Format(time.RFC3339))
		return
	}
	fmt.Printf("✓ commit ordering\n  both transports agree #%d began %s\n"+
		"  BranchFirstSeen keeps its meaning\n\n", number, viaREST.Format(time.RFC3339))
}

func (p *githubProber) reportCost(answer batchAnswer, pulls int) {
	if answer.rateLimit == nil {
		fmt.Printf("✗ rate limit\n  the response carried no rateLimit block\n\n")
		return
	}
	perBuild := answer.rateLimit.Cost * 4 // roughly four batches in a sprint
	fmt.Printf("✓ rate limit\n  %d points for %d pull requests; ~%d per retrospective, %d remaining until %s\n"+
		"  the hourly budget is 5,000, so this is not the constraint\n\n",
		answer.rateLimit.Cost, pulls, perBuild, answer.rateLimit.Remaining,
		answer.rateLimit.ResetAt.Format(time.Kitchen))
}

func (p *githubProber) reportTruncation(answer batchAnswer) {
	var truncated []int
	for number, pull := range answer.pulls {
		if pull.truncated {
			truncated = append(truncated, number)
		}
	}
	sort.Ints(truncated)

	if len(truncated) == 0 {
		fmt.Printf("✓ the response caps\n  none of the sampled pull requests exceeded them\n" +
			"  no REST refetches on a typical build\n\n")
		return
	}
	fmt.Printf("✓ the response caps\n  %d of %d sampled pull requests exceeded them: %v\n"+
		"  those fall back to REST, which is the designed behaviour — raise the caps\n"+
		"  in internal/adapter/github/graphql.go if this is most of them\n\n",
		len(truncated), len(answer.pulls), truncated)
}

// narrow finds the smallest query this credential will not serve.
//
// "FORBIDDEN" on a query with eleven fields says nothing actionable. Asking for
// one field at a time says exactly which permission is missing, which is the
// difference between a diagnosis and a shrug.
func (p *githubProber) narrow(ctx context.Context, owner, name string, number int) {
	fmt.Println("  narrowing down what this credential may read:")

	const repoRoot = `query($owner:String!,$name:String!){ repository(owner:$owner,name:$name)`

	steps := []struct {
		label string
		query string
	}{
		{"GraphQL at all", `query{ viewer{ login } }`},
		{"rateLimit", `query{ rateLimit{ cost remaining } }`},
		{"the repository", repoRoot + `{ name } }`},
	}
	if number > 0 {
		pull := repoRoot + fmt.Sprintf(`{ pullRequest(number:%d)`, number)
		steps = append(steps,
			struct{ label, query string }{"one pull request", pull + `{ number } } }`},
			struct{ label, query string }{"its commits", pull + `{ commits(first:1){ totalCount } } } }`},
			struct{ label, query string }{"its reviews", pull + `{ reviews(first:5){ totalCount } } } }`},
			struct{ label, query string }{"its timeline", pull + `{ timelineItems(first:5, itemTypes:[MERGED_EVENT]){ totalCount } } } }`},
		)
	}

	for _, step := range steps {
		if err := p.tryQuery(ctx, owner, name, step.query); err != nil {
			fmt.Printf("    ✗ %-18s %v\n", step.label, err)
			fmt.Printf("\n  the first ✗ above is the permission to fix.\n" +
				"  a classic token with repo scope reads all of these; a fine-grained token\n" +
				"  needs Pull requests: Read, Contents: Read and Metadata: Read.\n\n" +
				"  the app does not need this to work — it falls back to three REST requests\n" +
				"  per pull request, which is about a hundred extra per retrospective.\n\n")
			return
		}
		fmt.Printf("    ✓ %-18s readable\n", step.label)
	}
	fmt.Printf("\n  every field is readable on its own, so the failure is in the batch itself\n\n")
}

func (p *githubProber) tryQuery(ctx context.Context, owner, name, query string) error {
	var envelope struct {
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}
	body := map[string]any{"query": query, "variables": map[string]any{"owner": owner, "name": name}}

	if err := p.client.DoJSON(ctx, p.post(github.GraphQLEndpoint(p.githubBaseURL()), body), &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) == 0 {
		return nil
	}
	first := envelope.Errors[0]
	return fmt.Errorf("%s", strings.TrimSpace(first.Type+" "+first.Message))
}

type batchAnswer struct {
	pulls     map[int]probedPull
	denied    []string
	rateLimit *struct {
		Cost      int       `json:"cost"`
		Remaining int       `json:"remaining"`
		ResetAt   time.Time `json:"resetAt"`
	}
}

type probedPull struct {
	firstCommit time.Time
	truncated   bool
}

func (p *githubProber) batch(ctx context.Context, owner, name string, numbers []int) (batchAnswer, error) {
	body := map[string]any{
		"query":     github.PullDetailQuery(numbers),
		"variables": map[string]any{"owner": owner, "name": name},
	}

	var envelope struct {
		Data struct {
			RateLimit *struct {
				Cost      int       `json:"cost"`
				Remaining int       `json:"remaining"`
				ResetAt   time.Time `json:"resetAt"`
			} `json:"rateLimit"`
			Repository map[string]*struct {
				Number  int `json:"number"`
				Commits struct {
					Nodes []struct {
						Commit struct {
							CommittedDate time.Time `json:"committedDate"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
				Reviews struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						State string `json:"state"`
					} `json:"nodes"`
				} `json:"reviews"`
				TimelineItems struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						TypeName string `json:"__typename"`
					} `json:"nodes"`
				} `json:"timelineItems"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}

	endpoint := github.GraphQLEndpoint(p.githubBaseURL())
	if err := p.client.DoJSON(ctx, p.post(endpoint, body), &envelope); err != nil {
		return batchAnswer{}, err
	}

	// A 200 carrying errors is how GraphQL reports failure, so this is checked
	// before anything in data is believed. The path matters as much as the
	// message: an error naming one alias denies one pull request, while an
	// error with no path denies the query.
	var denied []string
	var global []string
	for _, item := range envelope.Errors {
		message := strings.TrimSpace(item.Type + " " + item.Message)
		if alias := aliasIn(item.Path); alias != "" {
			denied = append(denied, alias)
			continue
		}
		global = append(global, message+describePath(item.Path))
	}
	if len(global) > 0 {
		return batchAnswer{}, fmt.Errorf("%s", strings.Join(global, "; "))
	}

	answer := batchAnswer{pulls: map[int]probedPull{}, denied: denied, rateLimit: envelope.Data.RateLimit}
	for _, pull := range envelope.Data.Repository {
		if pull == nil {
			continue
		}
		probed := probedPull{
			truncated: pull.Reviews.TotalCount > len(pull.Reviews.Nodes) ||
				pull.TimelineItems.TotalCount > len(pull.TimelineItems.Nodes),
		}
		if len(pull.Commits.Nodes) > 0 {
			probed.firstCommit = pull.Commits.Nodes[0].Commit.CommittedDate
		}
		answer.pulls[pull.Number] = probed
	}
	return answer, nil
}

func (p *githubProber) recentPulls(ctx context.Context, owner, name string) ([]int, error) {
	var pulls []struct {
		Number int `json:"number"`
	}
	target := fmt.Sprintf("%s/repos/%s/%s/pulls?state=all&sort=updated&direction=desc&per_page=10",
		p.githubBaseURL(), owner, name)
	if err := p.client.DoJSON(ctx, p.get(target), &pulls); err != nil {
		return nil, err
	}

	numbers := make([]int, 0, len(pulls))
	for _, pull := range pulls {
		numbers = append(numbers, pull.Number)
	}
	sort.Ints(numbers)
	return numbers, nil
}

func (p *githubProber) firstCommitREST(ctx context.Context, owner, name string, number int) (time.Time, error) {
	var commits []struct {
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	target := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/commits?per_page=1&page=1",
		p.githubBaseURL(), owner, name, number)
	if err := p.client.DoJSON(ctx, p.get(target), &commits); err != nil {
		return time.Time{}, err
	}
	if len(commits) == 0 {
		return time.Time{}, fmt.Errorf("pull request #%d has no commits", number)
	}
	return commits[0].Commit.Committer.Date, nil
}

func (p *githubProber) githubBaseURL() string {
	if p.config.GitHubBaseURL != "" {
		return strings.TrimSuffix(p.config.GitHubBaseURL, "/")
	}
	return github.DefaultBaseURL
}

func (p *githubProber) get(target string) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		p.authorize(req)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		return req, nil
	}
}

func (p *githubProber) post(target string, body any) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(string(encoded)))
		if err != nil {
			return nil, err
		}
		p.authorize(req)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	}
}

func (p *githubProber) authorize(req *http.Request) {
	if p.config.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.GitHubToken)
	}
}

// aliasIn reports the pull request alias an error path names, if any.
func aliasIn(path []any) string {
	for _, step := range path {
		name, ok := step.(string)
		if ok && strings.HasPrefix(name, "p") && len(name) > 1 {
			return name
		}
	}
	return ""
}

// describePath appends the field an error came from, which is what turns
// "FORBIDDEN" into something a reader can act on.
func describePath(path []any) string {
	if len(path) == 0 {
		return " (no path — the whole query)"
	}
	parts := make([]string, 0, len(path))
	for _, step := range path {
		parts = append(parts, fmt.Sprintf("%v", step))
	}
	return " (at " + strings.Join(parts, ".") + ")"
}
