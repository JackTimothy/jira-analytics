// Package jira implements the IssueTracker port against Jira Cloud.
package jira

import (
	"bytes"
	"context"
	"encoding/json"
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
)

// Config carries the deployment-specific connection details. None of these
// values may be hard-coded: they identify one company's Jira site.
type Config struct {
	BaseURL  string
	Email    string
	APIToken string
}

// Tracker reads planning facts from Jira.
type Tracker struct {
	config Config
	client *httpclient.Client

	// statusMu guards a lazily built map from status id to status. The
	// changelog reports transitions by status id only, so resolving a past
	// status to a category requires this lookup.
	statusMu sync.Mutex
	statuses map[string]domain.IssueStatus

	// sprintFieldMu guards the discovered id of the sprint custom field, which
	// is numbered differently on every Jira site.
	sprintFieldMu sync.Mutex
	sprintField   string

	// sprintsMu guards the per-project sprint list. Building it costs one
	// request per hundred issues, and the answer changes at most once a sprint.
	sprintsMu sync.Mutex
	sprints   map[string]sprintCacheEntry
}

type sprintCacheEntry struct {
	sprints []domain.Sprint
	at      time.Time
}

// sprintCacheTTL is short enough that a newly created sprint appears without a
// restart, long enough that repeated navigation does not rescan the project.
const sprintCacheTTL = 5 * time.Minute

func NewTracker(config Config, client *httpclient.Client) *Tracker {
	return &Tracker{config: config, client: client}
}

// pageSize is Jira's practical maximum for these endpoints.
const pageSize = 100

func (t *Tracker) request(method, path string, query url.Values, body any) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		target := strings.TrimSuffix(t.config.BaseURL, "/") + path
		if len(query) > 0 {
			target += "?" + query.Encode()
		}

		var reader *bytes.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("encoding request body: %w", err)
			}
			reader = bytes.NewReader(encoded)
		}

		var req *http.Request
		var err error
		if reader != nil {
			req, err = http.NewRequest(method, target, reader)
		} else {
			req, err = http.NewRequest(method, target, nil)
		}
		if err != nil {
			return nil, err
		}

		req.SetBasicAuth(t.config.Email, t.config.APIToken)
		req.Header.Set("Accept", "application/json")
		if reader != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, nil
	}
}

// ListSprints returns the sprints the project's own work actually belongs to,
// most recently started first.
//
// It reads the sprints off the project's issues rather than off a board. A
// board's sprint list is the board's, not the project's: a long-lived or shared
// board carries sprints belonging to other teams entirely, and there is no way
// to tell from the board endpoint which is which. Sprint membership recorded on
// an issue is unambiguous, so scoping by it is correct by construction.
//
// The scan is cached, because it costs one request per hundred issues and the
// set of sprints changes at most once a fortnight.
func (t *Tracker) ListSprints(ctx context.Context, tracker domain.TrackerRef) ([]domain.Sprint, error) {
	if cached, ok := t.cachedSprints(tracker.ProjectKey); ok {
		return cached, nil
	}

	field, err := t.sprintFieldID(ctx)
	if err != nil {
		return nil, err
	}

	jql := fmt.Sprintf("project = %q AND sprint IS NOT EMPTY ORDER BY created ASC", tracker.ProjectKey)
	byID := map[int]domain.Sprint{}

	err = t.eachSearchPage(ctx, jql, []string{field}, func(issues []issueJSON, raw []json.RawMessage) error {
		for _, entry := range raw {
			for _, sprint := range sprintsInIssue(entry, field) {
				converted, err := sprint.toDomain()
				if err != nil {
					return err
				}
				// A sprint with no dates cannot bound a timeline. Future sprints
				// that have never been started are the usual case.
				if converted.Start.IsZero() || converted.End.IsZero() {
					continue
				}
				byID[sprint.ID] = converted
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sprints := make([]domain.Sprint, 0, len(byID))
	for _, sprint := range byID {
		sprints = append(sprints, sprint)
	}
	sort.SliceStable(sprints, func(i, j int) bool {
		if !sprints[i].Start.Equal(sprints[j].Start) {
			return sprints[i].Start.After(sprints[j].Start)
		}
		return sprints[i].ID < sprints[j].ID
	})

	t.storeSprints(tracker.ProjectKey, sprints)
	return sprints, nil
}

// sprintsInIssue pulls the sprint objects out of an issue's sprint custom
// field. The field id differs per Jira site, so it is resolved at runtime and
// the raw JSON is decoded here rather than being given a fixed struct tag.
func sprintsInIssue(raw json.RawMessage, field string) []sprintJSON {
	var envelope struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	value, ok := envelope.Fields[field]
	if !ok {
		return nil
	}
	var sprints []sprintJSON
	if err := json.Unmarshal(value, &sprints); err != nil {
		return nil
	}
	return sprints
}

// sprintFieldID finds the sprint custom field for this site. Its numeric id is
// site-specific, so it is discovered by its well-known schema rather than
// hard-coded.
func (t *Tracker) sprintFieldID(ctx context.Context) (string, error) {
	t.sprintFieldMu.Lock()
	defer t.sprintFieldMu.Unlock()

	if t.sprintField != "" {
		return t.sprintField, nil
	}

	var fields []fieldJSON
	if err := t.client.DoJSON(ctx, t.request(http.MethodGet, "/rest/api/3/field", nil, nil), &fields); err != nil {
		return "", fmt.Errorf("loading field definitions: %w", err)
	}
	for _, field := range fields {
		if field.Schema.Custom == sprintFieldSchema {
			t.sprintField = field.ID
			return t.sprintField, nil
		}
	}
	return "", fmt.Errorf("this Jira site has no sprint field (looked for schema %q)", sprintFieldSchema)
}

func (t *Tracker) cachedSprints(projectKey string) ([]domain.Sprint, bool) {
	t.sprintsMu.Lock()
	defer t.sprintsMu.Unlock()

	entry, ok := t.sprints[projectKey]
	if !ok || time.Since(entry.at) > sprintCacheTTL {
		return nil, false
	}
	out := make([]domain.Sprint, len(entry.sprints))
	copy(out, entry.sprints)
	return out, true
}

func (t *Tracker) storeSprints(projectKey string, sprints []domain.Sprint) {
	t.sprintsMu.Lock()
	defer t.sprintsMu.Unlock()

	if t.sprints == nil {
		t.sprints = map[string]sprintCacheEntry{}
	}
	stored := make([]domain.Sprint, len(sprints))
	copy(stored, sprints)
	t.sprints[projectKey] = sprintCacheEntry{sprints: stored, at: time.Now()}
}

// SprintParents returns the parent-level work items a project had in a sprint.
//
// The query is scoped to the project, not just the sprint. A sprint on a shared
// board contains other teams' issues too, and a retrospective that silently
// mixed them in would be worse than useless.
//
// Sub-tasks are excluded because they carry no sprint field of their own — they
// inherit membership from their parent — and are fetched by parent afterwards.
func (t *Tracker) SprintParents(ctx context.Context, tracker domain.TrackerRef, sprint domain.SprintID) ([]domain.WorkItem, error) {
	jql := fmt.Sprintf("sprint = %s AND project = %q ORDER BY key ASC", string(sprint), tracker.ProjectKey)

	var parents []domain.WorkItem
	err := t.eachSearchPage(ctx, jql, []string{"summary", "duedate", "created", "status", "issuetype"},
		func(issues []issueJSON, _ []json.RawMessage) error {
			for _, issue := range issues {
				if issue.Fields.IssueType.Subtask {
					continue
				}
				item, err := issue.toWorkItem()
				if err != nil {
					return err
				}
				parents = append(parents, item)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return parents, nil
}

// eachSearchPage walks a JQL search, handing each page to fn. Both the decoded
// issues and their raw JSON are passed, since custom fields are addressed by an
// id discovered at runtime and cannot be given a struct tag.
//
// Paging is by the token the server returns, so there is no page arithmetic to
// get wrong.
func (t *Tracker) eachSearchPage(ctx context.Context, jql string, fields []string, fn func([]issueJSON, []json.RawMessage) error) error {
	var pageToken string

	for {
		body := map[string]any{
			"jql":        jql,
			"maxResults": pageSize,
			"fields":     fields,
		}
		if pageToken != "" {
			body["nextPageToken"] = pageToken
		}

		var page struct {
			Issues        []json.RawMessage `json:"issues"`
			NextPageToken string            `json:"nextPageToken"`
		}
		if err := t.client.DoJSON(ctx, t.request(http.MethodPost, "/rest/api/3/search/jql", nil, body), &page); err != nil {
			return fmt.Errorf("searching issues: %w", err)
		}

		issues := make([]issueJSON, 0, len(page.Issues))
		for _, raw := range page.Issues {
			var issue issueJSON
			if err := json.Unmarshal(raw, &issue); err != nil {
				return fmt.Errorf("decoding issue: %w", err)
			}
			issues = append(issues, issue)
		}

		if err := fn(issues, page.Issues); err != nil {
			return err
		}

		if page.NextPageToken == "" || len(page.Issues) == 0 {
			return nil
		}
		pageToken = page.NextPageToken
	}
}

func (i issueJSON) toWorkItem() (domain.WorkItem, error) {
	created, err := parseTime(i.Fields.Created)
	if err != nil {
		return domain.WorkItem{}, fmt.Errorf("issue %s created: %w", i.Key, err)
	}

	var due *domain.CalendarDate
	if i.Fields.DueDate != "" {
		parsed, err := domain.ParseCalendarDate(i.Fields.DueDate)
		if err != nil {
			return domain.WorkItem{}, fmt.Errorf("issue %s due date: %w", i.Key, err)
		}
		due = &parsed
	}

	return domain.WorkItem{
		Key:     domain.IssueKey(i.Key),
		Summary: i.Fields.Summary,
		DueDate: due,
		Created: created,
	}, nil
}

// SubTasksOf fetches the sub-tasks of the given parents.
func (t *Tracker) SubTasksOf(ctx context.Context, parents []domain.IssueKey) ([]domain.SubTask, error) {
	if len(parents) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(parents))
	for _, key := range parents {
		keys = append(keys, `"`+string(key)+`"`)
	}
	jql := "parent IN (" + strings.Join(keys, ",") + ") ORDER BY key ASC"

	var subTasks []domain.SubTask
	err := t.eachSearchPage(ctx, jql, []string{"summary", "created", "status", "issuetype", "parent"},
		func(issues []issueJSON, _ []json.RawMessage) error {
			for _, issue := range issues {
				subTask, err := issue.toSubTask()
				if err != nil {
					return err
				}
				subTasks = append(subTasks, subTask)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return subTasks, nil
}

func (i issueJSON) toSubTask() (domain.SubTask, error) {
	created, err := parseTime(i.Fields.Created)
	if err != nil {
		return domain.SubTask{}, fmt.Errorf("sub-task %s created: %w", i.Key, err)
	}
	var parent domain.IssueKey
	if i.Fields.Parent != nil {
		parent = domain.IssueKey(i.Fields.Parent.Key)
	}
	return domain.SubTask{
		Key:       domain.IssueKey(i.Key),
		ParentKey: parent,
		Summary:   i.Fields.Summary,
		Created:   created,
		Status:    i.Fields.Status.toDomain(),
	}, nil
}

// StatusHistory returns each issue's status transitions.
//
// Jira's changelog records transitions by status id, so the ids are resolved
// against the site's status list to recover each side's category. Falling back
// to the changelog's display names keeps a status that has since been deleted
// from breaking the whole timeline.
func (t *Tracker) StatusHistory(ctx context.Context, keys []domain.IssueKey) (map[domain.IssueKey][]domain.StatusChange, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	statuses, err := t.statusesByID(ctx)
	if err != nil {
		return nil, err
	}

	history := make(map[domain.IssueKey][]domain.StatusChange, len(keys))
	for _, key := range keys {
		changes, err := t.changelogFor(ctx, key, statuses)
		if err != nil {
			return nil, err
		}
		if len(changes) > 0 {
			history[key] = changes
		}
	}
	return history, nil
}

func (t *Tracker) changelogFor(ctx context.Context, key domain.IssueKey, statuses map[string]domain.IssueStatus) ([]domain.StatusChange, error) {
	var changes []domain.StatusChange

	for startAt := 0; ; {
		query := url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(pageSize)},
		}
		var page changelogPage
		path := "/rest/api/3/issue/" + url.PathEscape(string(key)) + "/changelog"
		if err := t.client.DoJSON(ctx, t.request(http.MethodGet, path, query, nil), &page); err != nil {
			return nil, fmt.Errorf("changelog for %s: %w", key, err)
		}

		for _, entry := range page.Values {
			at, err := parseTime(entry.Created)
			if err != nil {
				return nil, fmt.Errorf("changelog entry for %s: %w", key, err)
			}
			for _, item := range entry.Items {
				if !strings.EqualFold(item.Field, "status") {
					continue
				}
				changes = append(changes, domain.StatusChange{
					At:   at,
					From: resolveStatus(statuses, item.From, item.FromString),
					To:   resolveStatus(statuses, item.To, item.ToString),
				})
			}
		}

		// Advance by what the server actually returned, never by what was
		// asked for. Jira caps maxResults per endpoint, so adding the requested
		// page size would step straight over every entry it declined to send.
		if page.IsLast || len(page.Values) == 0 {
			break
		}
		startAt += len(page.Values)
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].At.Before(changes[j].At) })
	return changes, nil
}

func resolveStatus(statuses map[string]domain.IssueStatus, id, name string) domain.IssueStatus {
	if status, ok := statuses[id]; ok {
		return status
	}
	// The id is unknown — a deleted status, or a site that reports none. The
	// name still carries the one status the process names explicitly, so
	// Blocked survives; anything else is treated as in flight, which is the
	// safer assumption for a status that existed mid-sprint.
	status := domain.IssueStatus{Name: name, Category: domain.CategoryInProgress}
	if status.IsBlocked() {
		status.Category = domain.CategoryToDo
	}
	return status
}

func (t *Tracker) statusesByID(ctx context.Context) (map[string]domain.IssueStatus, error) {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()

	if t.statuses != nil {
		return t.statuses, nil
	}

	var raw []statusJSON
	if err := t.client.DoJSON(ctx, t.request(http.MethodGet, "/rest/api/3/status", nil, nil), &raw); err != nil {
		return nil, fmt.Errorf("loading status definitions: %w", err)
	}

	statuses := make(map[string]domain.IssueStatus, len(raw))
	for _, status := range raw {
		statuses[status.ID] = status.toDomain()
	}
	t.statuses = statuses
	return statuses, nil
}
