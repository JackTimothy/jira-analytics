// Package github implements the CodeHost port against the GitHub REST API.
package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
)

// Config carries deployment-specific connection details.
type Config struct {
	// BaseURL is the API root. Configurable so the app works against GitHub
	// Enterprise as well as github.com.
	BaseURL string
	Token   string
}

// DefaultBaseURL is github.com's API root.
const DefaultBaseURL = "https://api.github.com"

// maxConcurrency bounds in-flight requests. Assembling one retrospective can
// touch a couple of hundred endpoints; unbounded fan-out would trip secondary
// rate limits, which cost far more time than the parallelism saves.
const maxConcurrency = 8

// CodeHost reads delivery facts from GitHub.
type CodeHost struct {
	config Config
	client *httpclient.Client

	defaultBranchMu sync.Mutex
	defaultBranches map[string]string
}

func NewCodeHost(config Config, client *httpclient.Client) *CodeHost {
	if config.BaseURL == "" {
		config.BaseURL = DefaultBaseURL
	}
	return &CodeHost{
		config:          config,
		client:          client,
		defaultBranches: map[string]string{},
	}
}

func (c *CodeHost) request(path string, query url.Values) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		target := strings.TrimSuffix(c.config.BaseURL, "/") + path
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.config.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.config.Token)
		}
		return req, nil
	}
}

// LinkedEvents returns the domain events implied by code activity for each
// issue key. Keys with no matching branch are absent from the result, which is
// how the caller distinguishes "nothing happened" from "we found nothing".
func (c *CodeHost) LinkedEvents(
	ctx context.Context,
	repos []domain.RepoRef,
	policy domain.ReviewerPolicy,
	keys []domain.IssueKey,
) (map[domain.IssueKey][]domain.Event, error) {
	if len(keys) == 0 || len(repos) == 0 {
		return nil, nil
	}

	matcher := newKeyMatcher(keys)
	results := map[domain.IssueKey][]domain.Event{}
	var mu sync.Mutex

	for _, repo := range repos {
		branches, err := c.matchingBranches(ctx, repo, matcher)
		if err != nil {
			return nil, err
		}

		err = forEachBounded(ctx, branches, maxConcurrency, func(ctx context.Context, match branchMatch) error {
			events, err := c.eventsForBranch(ctx, repo, match, policy)
			if err != nil {
				return err
			}
			mu.Lock()
			results[match.key] = append(results[match.key], events...)
			mu.Unlock()
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return results, nil
}

type branchMatch struct {
	name string
	key  domain.IssueKey
}

// matchingBranches lists a repository's branches and keeps those naming an
// issue we care about. Listing every branch is cheaper than it looks — it is a
// handful of paginated calls — and it is the only way to see a branch that has
// no pull request yet, which is precisely the In Progress state.
func (c *CodeHost) matchingBranches(ctx context.Context, repo domain.RepoRef, matcher keyMatcher) ([]branchMatch, error) {
	var matches []branchMatch

	for page := 1; ; page++ {
		query := url.Values{
			"per_page": {"100"},
			"page":     {strconv.Itoa(page)},
		}
		var branches []branchJSON
		path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/branches"
		if err := c.client.DoJSON(ctx, c.request(path, query), &branches); err != nil {
			return nil, fmt.Errorf("listing branches for %s: %w", repo, err)
		}

		for _, branch := range branches {
			if key, ok := matcher.match(branch.Name); ok {
				matches = append(matches, branchMatch{name: branch.Name, key: key})
			}
		}

		if len(branches) < 100 {
			break
		}
	}
	return matches, nil
}

func (c *CodeHost) eventsForBranch(
	ctx context.Context,
	repo domain.RepoRef,
	match branchMatch,
	policy domain.ReviewerPolicy,
) ([]domain.Event, error) {
	firstSeen, err := c.branchFirstSeen(ctx, repo, match.name)
	if err != nil {
		return nil, err
	}

	events := []domain.Event{domain.BranchFirstSeen{At: firstSeen, Name: repo.String() + ":" + match.name}}

	pulls, err := c.pullsForBranch(ctx, repo, match.name)
	if err != nil {
		return nil, err
	}
	for _, pull := range pulls {
		pullEvents, err := c.eventsForPull(ctx, repo, pull, policy)
		if err != nil {
			return nil, err
		}
		events = append(events, pullEvents...)
	}
	return events, nil
}

// branchFirstSeen approximates when a branch came into existence.
//
// GitHub records no branch-creation timestamp, so the earliest commit unique to
// the branch stands in for it. That is the one place this timeline is inferred
// rather than observed. A branch with no unique commits — created and not yet
// committed to, or already merged and reset — falls back to the repository's
// view of the branch tip.
func (c *CodeHost) branchFirstSeen(ctx context.Context, repo domain.RepoRef, branch string) (time.Time, error) {
	base, err := c.defaultBranch(ctx, repo)
	if err != nil {
		return time.Time{}, err
	}

	path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) +
		"/compare/" + url.PathEscape(base) + "..." + url.PathEscape(branch)

	var comparison compareJSON
	if err := c.client.DoJSON(ctx, c.request(path, url.Values{"per_page": {"1"}}), &comparison); err != nil {
		return time.Time{}, fmt.Errorf("comparing %s against %s in %s: %w", branch, base, repo, err)
	}
	if len(comparison.Commits) == 0 {
		return time.Time{}, nil
	}
	return comparison.Commits[0].Commit.Committer.Date, nil
}

func (c *CodeHost) defaultBranch(ctx context.Context, repo domain.RepoRef) (string, error) {
	c.defaultBranchMu.Lock()
	defer c.defaultBranchMu.Unlock()

	if branch, ok := c.defaultBranches[repo.String()]; ok {
		return branch, nil
	}

	var details repoJSON
	path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name)
	if err := c.client.DoJSON(ctx, c.request(path, nil), &details); err != nil {
		return "", fmt.Errorf("reading %s: %w", repo, err)
	}
	c.defaultBranches[repo.String()] = details.DefaultBranch
	return details.DefaultBranch, nil
}

func (c *CodeHost) pullsForBranch(ctx context.Context, repo domain.RepoRef, branch string) ([]pullJSON, error) {
	query := url.Values{
		"head":     {repo.Owner + ":" + branch},
		"state":    {"all"},
		"per_page": {"100"},
	}
	var pulls []pullJSON
	path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/pulls"
	if err := c.client.DoJSON(ctx, c.request(path, query), &pulls); err != nil {
		return nil, fmt.Errorf("listing pull requests for %s in %s: %w", branch, repo, err)
	}
	return pulls, nil
}

func (c *CodeHost) eventsForPull(
	ctx context.Context,
	repo domain.RepoRef,
	pull pullJSON,
	policy domain.ReviewerPolicy,
) ([]domain.Event, error) {
	key := domain.PRKey{Repo: repo.String(), Number: pull.Number}

	timeline, err := c.timelineFor(ctx, repo, pull.Number)
	if err != nil {
		return nil, err
	}
	reviews, err := c.reviewsFor(ctx, repo, pull.Number)
	if err != nil {
		return nil, err
	}

	events := []domain.Event{domain.PROpened{
		At:    pull.CreatedAt,
		PR:    key,
		Draft: draftAtCreation(pull, timeline),
	}}
	events = append(events, timelineEvents(key, timeline, policy)...)
	events = append(events, reviewEvents(key, reviews, policy)...)
	return events, nil
}

func (c *CodeHost) timelineFor(ctx context.Context, repo domain.RepoRef, number int) ([]timelineEventJSON, error) {
	var all []timelineEventJSON

	for page := 1; ; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var events []timelineEventJSON
		path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) +
			"/issues/" + strconv.Itoa(number) + "/timeline"
		if err := c.client.DoJSON(ctx, c.request(path, query), &events); err != nil {
			return nil, fmt.Errorf("reading timeline for %s#%d: %w", repo, number, err)
		}
		all = append(all, events...)
		if len(events) < 100 {
			break
		}
	}
	return all, nil
}

func (c *CodeHost) reviewsFor(ctx context.Context, repo domain.RepoRef, number int) ([]reviewJSON, error) {
	var all []reviewJSON

	for page := 1; ; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var reviews []reviewJSON
		path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) +
			"/pulls/" + strconv.Itoa(number) + "/reviews"
		if err := c.client.DoJSON(ctx, c.request(path, query), &reviews); err != nil {
			return nil, fmt.Errorf("reading reviews for %s#%d: %w", repo, number, err)
		}
		all = append(all, reviews...)
		if len(reviews) < 100 {
			break
		}
	}
	return all, nil
}
