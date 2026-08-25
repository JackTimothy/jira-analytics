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
}

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

// ListSprints returns a board's sprints, most recently started first.
func (t *Tracker) ListSprints(ctx context.Context, tracker domain.TrackerRef) ([]domain.Sprint, error) {
	var sprints []domain.Sprint

	for startAt := 0; ; startAt += pageSize {
		query := url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(pageSize)},
		}
		var page sprintPage
		path := "/rest/agile/1.0/board/" + url.PathEscape(tracker.BoardID) + "/sprint"
		if err := t.client.DoJSON(ctx, t.request(http.MethodGet, path, query, nil), &page); err != nil {
			return nil, err
		}

		for _, raw := range page.Values {
			sprint, err := raw.toDomain()
			if err != nil {
				return nil, err
			}
			// A sprint with no dates cannot bound a timeline, and a future
			// sprint that has never started has none. Skipping is correct;
			// failing would make the whole list unusable.
			if sprint.Start.IsZero() || sprint.End.IsZero() {
				continue
			}
			sprints = append(sprints, sprint)
		}

		if page.IsLast || len(page.Values) == 0 {
			break
		}
	}

	sort.SliceStable(sprints, func(i, j int) bool { return sprints[i].Start.After(sprints[j].Start) })
	return sprints, nil
}

func (s sprintJSON) toDomain() (domain.Sprint, error) {
	start, err := parseTime(s.StartDate)
	if err != nil {
		return domain.Sprint{}, fmt.Errorf("sprint %d start date: %w", s.ID, err)
	}
	end, err := parseTime(s.EndDate)
	if err != nil {
		return domain.Sprint{}, fmt.Errorf("sprint %d end date: %w", s.ID, err)
	}
	return domain.Sprint{
		ID:    domain.SprintID(strconv.Itoa(s.ID)),
		Name:  s.Name,
		Start: start,
		End:   end,
	}, nil
}

// SprintParents returns the parent-level work items in a sprint.
//
// Sub-tasks carry no sprint field of their own — they inherit membership from
// their parent — so this deliberately returns only non-sub-task issues, and the
// sub-tasks are fetched by parent afterwards.
func (t *Tracker) SprintParents(ctx context.Context, _ domain.TrackerRef, sprint domain.SprintID) ([]domain.WorkItem, error) {
	var parents []domain.WorkItem

	for startAt := 0; ; startAt += pageSize {
		query := url.Values{
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(pageSize)},
			"fields":     {"summary,duedate,created,status,issuetype"},
		}
		var page issuePage
		path := "/rest/agile/1.0/sprint/" + url.PathEscape(string(sprint)) + "/issue"
		if err := t.client.DoJSON(ctx, t.request(http.MethodGet, path, query, nil), &page); err != nil {
			return nil, err
		}

		for _, issue := range page.Issues {
			if issue.Fields.IssueType.Subtask {
				continue
			}
			item, err := issue.toWorkItem()
			if err != nil {
				return nil, err
			}
			parents = append(parents, item)
		}

		if len(page.Issues) < pageSize || startAt+len(page.Issues) >= page.Total {
			break
		}
	}
	return parents, nil
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

// SubTasksOf fetches the sub-tasks of the given parents in one JQL query.
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
	var pageToken string

	for {
		body := map[string]any{
			"jql":        jql,
			"maxResults": pageSize,
			"fields":     []string{"summary", "created", "status", "issuetype", "parent"},
		}
		if pageToken != "" {
			body["nextPageToken"] = pageToken
		}

		var page issuePage
		if err := t.client.DoJSON(ctx, t.request(http.MethodPost, "/rest/api/3/search/jql", nil, body), &page); err != nil {
			return nil, err
		}

		for _, issue := range page.Issues {
			subTask, err := issue.toSubTask()
			if err != nil {
				return nil, err
			}
			subTasks = append(subTasks, subTask)
		}

		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
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

	for startAt := 0; ; startAt += pageSize {
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

		if page.IsLast || len(page.Values) == 0 {
			break
		}
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
