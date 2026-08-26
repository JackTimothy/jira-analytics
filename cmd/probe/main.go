// Command probe answers, against a real Jira site, the questions this app has
// to guess at otherwise.
//
// The adapter copes either way — every fast path here has a working fallback —
// so this is not required to run the server. It exists because the fallbacks
// are expensive and silent-by-nature, and knowing which one a site takes turns
// "the retrospective is slow" into a specific, fixable fact.
//
// It reads the same environment as the server and makes a handful of read-only
// requests.
//
//	go run ./cmd/probe -sprint 7354
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/infra/config"
	"github.com/jacktimothy/jira-analytics/internal/infra/httpclient"
)

func main() {
	sprint := flag.String("sprint", "", "id of a sprint to look up (required)")
	project := flag.String("project", "", "project key to sample issues from (defaults to the first configured project's)")
	flag.Parse()

	if err := run(*sprint, *project); err != nil {
		fmt.Fprintln(os.Stderr, "probe failed:", err)
		os.Exit(1)
	}
}

func run(sprint, project string) error {
	settings, err := config.Load()
	if err != nil {
		return err
	}
	if project == "" {
		project, err = firstProjectKey(settings.ProjectsFile)
		if err != nil {
			return err
		}
	}
	if sprint == "" {
		return fmt.Errorf("-sprint is required; take an id from the sprint dropdown's URL")
	}

	client := httpclient.New(&http.Client{Timeout: 30 * time.Second})
	p := &prober{settings: settings, client: client}
	ctx := context.Background()

	fmt.Printf("site    %s\nproject %s\n\n", settings.JiraBaseURL, project)

	p.check("sprint lookup by id",
		"one request instead of a scan of every issue in the project",
		"every retrospective pays the scan; see the warning the server logs",
		func() (string, error) { return p.sprintByID(ctx, sprint) })

	p.check("changelog attached to a search",
		"the whole sprint's status history in one request",
		"one request per sub-task, on every build",
		func() (string, error) { return p.expandChangelog(ctx, project) })

	p.check("sub-tasks carry the sprint field",
		"parents and sub-tasks could be fetched in one query rather than two",
		"the two queries stay as they are, which costs one round trip",
		func() (string, error) { return p.subTasksInSprint(ctx, project, sprint) })

	return nil
}

type prober struct {
	settings config.Config
	client   *httpclient.Client
}

// check runs one probe and reports it in the same shape every time: what was
// gained, or what it will cost instead.
func (p *prober) check(name, gain, cost string, probe func() (string, error)) {
	detail, err := probe()
	if err != nil {
		fmt.Printf("✗ %s\n  %v\n  consequence: %s\n\n", name, err, cost)
		return
	}
	fmt.Printf("✓ %s\n  %s\n  %s\n\n", name, detail, gain)
}

func (p *prober) request(method, path string, body any) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		var req *http.Request
		var err error
		target := strings.TrimSuffix(p.settings.JiraBaseURL, "/") + path

		if body != nil {
			encoded, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				return nil, marshalErr
			}
			req, err = http.NewRequest(method, target, strings.NewReader(string(encoded)))
		} else {
			req, err = http.NewRequest(method, target, nil)
		}
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(p.settings.JiraEmail, p.settings.JiraAPIToken)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}
}

func (p *prober) sprintByID(ctx context.Context, id string) (string, error) {
	var sprint struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		StartDate string `json:"startDate"`
		EndDate   string `json:"endDate"`
	}
	if err := p.client.DoJSON(ctx, p.request(http.MethodGet, "/rest/agile/1.0/sprint/"+id, nil), &sprint); err != nil {
		return "", err
	}
	if sprint.StartDate == "" || sprint.EndDate == "" {
		return "", fmt.Errorf("sprint %s came back with no dates (state %q)", id, sprint.State)
	}
	return fmt.Sprintf("%q ran %s to %s", sprint.Name, sprint.StartDate, sprint.EndDate), nil
}

func (p *prober) expandChangelog(ctx context.Context, project string) (string, error) {
	body := map[string]any{
		"jql":        fmt.Sprintf("project = %q AND issuetype = Sub-task ORDER BY created DESC", project),
		"maxResults": 5,
		"fields":     []string{"summary"},
		"expand":     "changelog",
	}

	var page struct {
		Issues []struct {
			Key       string `json:"key"`
			Changelog *struct {
				MaxResults int `json:"maxResults"`
				Total      int `json:"total"`
				Histories  []struct {
					Created string `json:"created"`
				} `json:"histories"`
			} `json:"changelog"`
		} `json:"issues"`
	}
	if err := p.client.DoJSON(ctx, p.request(http.MethodPost, "/rest/api/3/search/jql", body), &page); err != nil {
		return "", err
	}
	if len(page.Issues) == 0 {
		return "", fmt.Errorf("no sub-tasks in %s to test with", project)
	}

	// The dangerous outcome is not a rejection but an acceptance that quietly
	// drops the parameter, so the absence of the field is what gets checked.
	truncated := 0
	for _, issue := range page.Issues {
		if issue.Changelog == nil {
			return "", fmt.Errorf("the expand was accepted and ignored: %s came back with no changelog attached", issue.Key)
		}
		if issue.Changelog.Total > len(issue.Changelog.Histories) {
			truncated++
		}
	}

	note := fmt.Sprintf("%d of %d sampled sub-tasks carried a full changelog", len(page.Issues)-truncated, len(page.Issues))
	if truncated > 0 {
		note += fmt.Sprintf("; %d were truncated and would be refetched", truncated)
	}
	return note, nil
}

func (p *prober) subTasksInSprint(ctx context.Context, project, sprint string) (string, error) {
	body := map[string]any{
		"jql":        fmt.Sprintf("sprint = %s AND project = %q", sprint, project),
		"maxResults": 100,
		"fields":     []string{"issuetype"},
	}

	var page struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				IssueType struct {
					Subtask bool `json:"subtask"`
				} `json:"issuetype"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := p.client.DoJSON(ctx, p.request(http.MethodPost, "/rest/api/3/search/jql", body), &page); err != nil {
		return "", err
	}

	subTasks := 0
	for _, issue := range page.Issues {
		if issue.Fields.IssueType.Subtask {
			subTasks++
		}
	}
	if subTasks == 0 {
		return "", fmt.Errorf("the sprint query returned %d issues and none of them a sub-task", len(page.Issues))
	}
	return fmt.Sprintf("%d of the %d issues the sprint query returned are sub-tasks", subTasks, len(page.Issues)), nil
}

func firstProjectKey(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	// Deliberately crude: the probe wants one project key, not the whole
	// configuration model, and depending on the store would drag the adapter in
	// for no benefit.
	for _, line := range strings.Split(string(contents), "\n") {
		if _, key, found := strings.Cut(line, "projectKey:"); found {
			return strings.Trim(strings.TrimSpace(strings.Trim(strings.TrimSpace(key), "}")), `"'`), nil
		}
	}
	return "", fmt.Errorf("no projectKey found in %s; pass -project", path)
}
