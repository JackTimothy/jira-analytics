// Package github implements the CodeHost port against the GitHub REST API.
package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
	"github.com/jacktimothy/jira-analytics/internal/infra/parallel"
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
	window domain.Window,
) (map[domain.IssueKey][]domain.Event, error) {
	if len(keys) == 0 || len(repos) == 0 {
		return nil, nil
	}

	matcher := newKeyMatcher(keys)
	results := map[domain.IssueKey][]domain.Event{}
	var mu sync.Mutex

	record := func(key domain.IssueKey, events []domain.Event) {
		mu.Lock()
		results[key] = append(results[key], events...)
		mu.Unlock()
	}

	for _, repo := range repos {
		// Pull requests are found first, and by issue key rather than by
		// branch. A merged pull request usually has no branch left — deleting
		// the head on merge is the default — so anything branch-led would go
		// blind on exactly the work that finished.
		pulls, err := c.pullsInWindow(ctx, repo, window, matcher)
		if err != nil {
			return nil, err
		}

		covered := make(map[string]struct{}, len(pulls))
		for _, pull := range pulls {
			covered[pull.pull.Head.Ref] = struct{}{}
		}

		// Branches still standing that no pull request covers. This is work in
		// progress: a branch exists, review has not started.
		branches, err := c.matchingBranches(ctx, repo, keys, matcher)
		if err != nil {
			return nil, err
		}
		orphans := branches[:0:0]
		for _, branch := range branches {
			if _, ok := covered[branch.name]; !ok {
				orphans = append(orphans, branch)
			}
		}

		if err := parallel.ForEach(ctx, pulls, maxConcurrency, func(ctx context.Context, match pullMatch) error {
			events, err := c.eventsForPullRequest(ctx, repo, match.pull, policy)
			if err != nil {
				return err
			}
			record(match.key, events)
			return nil
		}); err != nil {
			return nil, err
		}

		if err := parallel.ForEach(ctx, orphans, maxConcurrency, func(ctx context.Context, match branchMatch) error {
			firstSeen, err := c.branchFirstSeen(ctx, repo, match.name)
			if err != nil {
				return err
			}
			record(match.key, []domain.Event{
				domain.BranchFirstSeen{At: firstSeen, Name: repo.String() + ":" + match.name},
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}

	return results, nil
}

type pullMatch struct {
	key  domain.IssueKey
	pull pullJSON
}

// pullLookback is how far before a sprint the pull request scan reaches. A pull
// request opened well before the sprint and left untouched still describes the
// state the sprint opened in, so the scan cannot stop at the sprint boundary.
const pullLookback = 120 * 24 * time.Hour

// maxPullPages bounds the scan. Reaching it means the repository saw more than
// 2000 pull requests in the window, at which point the oldest are far enough
// outside it not to matter.
const maxPullPages = 20

// pullsInWindow lists the repository's pull requests touching the sprint window
// and keeps those naming an issue we care about.
//
// Matching considers the head branch first and the title second. The branch is
// the reliable signal while it exists; the title is what survives a squash
// merge, where the message becomes "Title (#123)" and the branch is deleted.
func (c *CodeHost) pullsInWindow(ctx context.Context, repo domain.RepoRef, window domain.Window, matcher keyMatcher) ([]pullMatch, error) {
	cutoff := window.Start.Add(-pullLookback)
	var matches []pullMatch

	// Guard against the same pull request arriving twice. Replaying an opened
	// event after a merge would clear the merge and leave the timeline
	// oscillating, so a listing that repeats itself must not be trusted to
	// stop on its own.
	seen := map[int]struct{}{}

	for page := 1; page <= maxPullPages; page++ {
		query := url.Values{
			"state":     {"all"},
			"sort":      {"updated"},
			"direction": {"desc"},
			"per_page":  {"100"},
			"page":      {strconv.Itoa(page)},
		}
		var pulls []pullJSON
		path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/pulls"
		if err := c.client.DoJSON(ctx, c.request(path, query), &pulls); err != nil {
			return nil, fmt.Errorf("listing pull requests for %s: %w", repo, err)
		}

		exhausted, fresh := false, 0
		for _, pull := range pulls {
			if _, already := seen[pull.Number]; already {
				continue
			}
			seen[pull.Number] = struct{}{}
			fresh++

			if pull.UpdatedAt.Before(cutoff) {
				// Sorted newest-first, so everything after this is older still.
				exhausted = true
				break
			}
			if pull.CreatedAt.After(window.End) {
				// Opened after the sprint closed; nothing it did is in frame.
				continue
			}
			key, ok := matcher.match(pull.Head.Ref)
			if !ok {
				key, ok = matcher.match(pull.Title)
			}
			if ok {
				matches = append(matches, pullMatch{key: key, pull: pull})
			}
		}

		if exhausted || len(pulls) < 100 || fresh == 0 {
			break
		}
	}
	return matches, nil
}

type branchMatch struct {
	name string
	key  domain.IssueKey
}

// matchingBranches finds the branches naming an issue we care about.
//
// It leans on the convention that a branch name begins with its issue key,
// which is what a tracker's "create branch" button produces. Every sub-task in
// a sprint shares the same project key, so a single prefix query per project
// asks the server for exactly the candidate branches — no full repository scan,
// and none of the dependabot, release or long-lived branches that a monorepo
// accumulates. The cost then scales with the sprint, not with the repository.
//
// Two properties of the server-side match need handling, both verified against
// the live API rather than assumed:
//
//   - It is case sensitive, so the lowercase form is queried too when it
//     differs. A branch typed by hand rather than generated is still found.
//   - It matches raw characters, not whole tokens, so the prefix "PROJ-1" also
//     returns "PROJ-10-…" and "PROJ-123-…". Every candidate is therefore run
//     back through the key matcher, which parses whole keys and rejects those.
func (c *CodeHost) matchingBranches(ctx context.Context, repo domain.RepoRef, keys []domain.IssueKey, matcher keyMatcher) ([]branchMatch, error) {
	seen := map[string]struct{}{}
	var matches []branchMatch

	for _, prefix := range queryPrefixes(keys) {
		refs, err := c.refsWithPrefix(ctx, repo, prefix)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			name := strings.TrimPrefix(ref.Ref, "refs/heads/")
			if _, already := seen[name]; already {
				continue
			}
			key, ok := matcher.match(name)
			if !ok {
				continue
			}
			seen[name] = struct{}{}
			matches = append(matches, branchMatch{name: name, key: key})
		}
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].name < matches[j].name })
	return matches, nil
}

func (c *CodeHost) refsWithPrefix(ctx context.Context, repo domain.RepoRef, prefix string) ([]refJSON, error) {
	var all []refJSON
	seen := map[string]struct{}{}

	for page := 1; ; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var refs []refJSON
		path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) +
			"/git/matching-refs/heads/" + prefix
		if err := c.client.DoJSON(ctx, c.request(path, query), &refs); err != nil {
			return nil, fmt.Errorf("listing branches matching %q in %s: %w", prefix, repo, err)
		}

		fresh := 0
		for _, ref := range refs {
			if _, already := seen[ref.Ref]; already {
				continue
			}
			seen[ref.Ref] = struct{}{}
			all = append(all, ref)
			fresh++
		}

		// Stop on a short page, and also when a page adds nothing new — which
		// is what happens if the endpoint ignores the page parameter and keeps
		// returning the same set.
		if len(refs) < 100 || fresh == 0 {
			break
		}
	}
	return all, nil
}

// queryPrefixes reduces the issue keys to the shortest set of prefixes that
// covers them: normally one per project. The lowercase form is included when it
// differs, since the server-side match is case sensitive.
func queryPrefixes(keys []domain.IssueKey) []string {
	prefixes := map[string]struct{}{}
	for _, key := range keys {
		hyphen := strings.LastIndex(string(key), "-")
		if hyphen <= 0 {
			continue
		}
		prefix := string(key)[:hyphen+1]
		prefixes[prefix] = struct{}{}
		prefixes[strings.ToLower(prefix)] = struct{}{}
	}

	out := make([]string, 0, len(prefixes))
	for prefix := range prefixes {
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
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

func (c *CodeHost) eventsForPullRequest(
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

	// The branch existed from its first commit, which the pull request still
	// records after the branch itself is gone.
	events := []domain.Event{}
	if firstCommit, err := c.firstCommitOf(ctx, repo, pull.Number); err != nil {
		return nil, err
	} else if !firstCommit.IsZero() {
		events = append(events, domain.BranchFirstSeen{
			At:   firstCommit,
			Name: repo.String() + ":" + pull.Head.Ref,
		})
	}

	events = append(events, domain.PROpened{
		At:    pull.CreatedAt,
		PR:    key,
		Draft: draftAtCreation(pull, timeline),
	})
	events = append(events, timelineEvents(key, timeline, policy)...)
	events = append(events, reviewEvents(key, reviews, policy)...)
	return events, nil
}

// firstCommitOf returns when the earliest commit on a pull request was made.
// The commits endpoint lists them oldest first, and it keeps working after the
// head branch is deleted — which is why it is preferred over comparing refs.
func (c *CodeHost) firstCommitOf(ctx context.Context, repo domain.RepoRef, number int) (time.Time, error) {
	query := url.Values{"per_page": {"1"}, "page": {"1"}}
	path := "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) +
		"/pulls/" + strconv.Itoa(number) + "/commits"

	var commits []commitJSON
	if err := c.client.DoJSON(ctx, c.request(path, query), &commits); err != nil {
		return time.Time{}, fmt.Errorf("reading commits for %s#%d: %w", repo, number, err)
	}
	if len(commits) == 0 {
		return time.Time{}, nil
	}
	return commits[0].Commit.Committer.Date, nil
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
