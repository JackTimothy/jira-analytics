// Package jira implements the IssueTracker port against Jira Cloud.
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

	logger *slog.Logger

	// expandMu guards what this site was observed to do with an expand of the
	// changelog on a search. It is a runtime observation rather than
	// configuration because Jira Cloud, Jira Data Center and a site behind a
	// proxy all behave differently, and none of them says so up front.
	expandMu      sync.Mutex
	expandRefused bool

	// agileWarned keeps the fallback warning to once per process. It is worth
	// saying loudly and worth saying only once.
	agileWarned sync.Once
}

// Option adjusts a Tracker at construction.
type Option func(*Tracker)

// WithLogger gives the tracker somewhere to report a degraded path. Without it
// the fallback below is silent, which is the worst outcome: every build quietly
// pays for a full project scan and nothing says why.
func WithLogger(logger *slog.Logger) Option {
	return func(t *Tracker) {
		if logger != nil {
			t.logger = logger
		}
	}
}

type sprintCacheEntry struct {
	sprints []domain.Sprint
	at      time.Time
}

// sprintCacheTTL is short enough that a newly created sprint appears without a
// restart, long enough that repeated navigation does not rescan the project.
const sprintCacheTTL = 5 * time.Minute

func NewTracker(config Config, client *httpclient.Client, opts ...Option) *Tracker {
	tracker := &Tracker{
		config: config,
		client: client,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(tracker)
	}
	return tracker
}

// pageSize is Jira's practical maximum for these endpoints.
const pageSize = 100

// maxConcurrency bounds in-flight requests to Jira. One retrospective asks for
// a changelog per sub-task, and those requests have nothing to do with each
// other — but Jira rate limits by burst, and the resulting backoff costs more
// than the concurrency saves.
const maxConcurrency = 8

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

	err = t.eachSearchPage(ctx, jql, []string{field}, "", func(issues []issueJSON, raw []json.RawMessage) error {
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

// Sprint returns one sprint's dates by id.
//
// It asks the Agile API directly rather than scanning the project. This is the
// single largest saving in a retrospective build: the scan behind ListSprints
// costs one request per hundred issues that have ever been in a sprint —
// thirty-odd on a mature project — and every one of them was being paid to
// learn two timestamps.
//
// Fetching a sprint *by id* has none of the ambiguity that made board-based
// listing wrong. The objection there was that a shared board's sprint list
// contains other teams' sprints and nothing distinguishes them; here the caller
// already holds the id, so there is no "whose sprint is this" question to get
// wrong. The project scoping that keeps other teams out of the retrospective
// happens in SprintParents, which is where it belongs.
//
// The Agile API may be unavailable to a token that can read the platform API,
// so a failure falls back to the scan rather than failing the build.
func (t *Tracker) Sprint(ctx context.Context, tracker domain.TrackerRef, id domain.SprintID) (domain.Sprint, error) {
	var raw sprintJSON
	path := "/rest/agile/1.0/sprint/" + url.PathEscape(string(id))

	err := t.client.DoJSON(ctx, t.request(http.MethodGet, path, nil, nil), &raw)
	if err == nil {
		sprint, convErr := raw.toDomain()
		if convErr == nil && !sprint.Start.IsZero() && !sprint.End.IsZero() {
			return sprint, nil
		}
		// A sprint that exists but has never been started carries no dates, and
		// no amount of scanning will invent them.
		if convErr == nil {
			return domain.Sprint{}, fmt.Errorf("%w: sprint %s has no start or end date", domain.ErrNotFound, id)
		}
		err = convErr
	}

	t.agileWarned.Do(func() {
		t.logger.Warn("falling back to a full project scan to resolve sprints",
			slog.String("endpoint", path),
			slog.String("consequence", "every retrospective now costs one extra request per hundred issues in the project"),
			slog.Any("error", err))
	})
	return t.sprintFromScan(ctx, tracker, id)
}

func (t *Tracker) sprintFromScan(ctx context.Context, tracker domain.TrackerRef, id domain.SprintID) (domain.Sprint, error) {
	sprints, err := t.ListSprints(ctx, tracker)
	if err != nil {
		return domain.Sprint{}, fmt.Errorf("listing sprints: %w", err)
	}
	for _, sprint := range sprints {
		if sprint.ID == id {
			return sprint, nil
		}
	}
	return domain.Sprint{}, fmt.Errorf("%w: sprint %s", domain.ErrNotFound, id)
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
	err := t.eachSearchPage(ctx, jql, []string{"summary", "duedate", "created", "status", "issuetype"}, "",
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
func (t *Tracker) eachSearchPage(ctx context.Context, jql string, fields []string, expand string, fn func([]issueJSON, []json.RawMessage) error) error {
	var pageToken string

	for {
		body := map[string]any{
			"jql":        jql,
			"maxResults": pageSize,
			"fields":     fields,
		}
		if expand != "" {
			body["expand"] = expand
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
	err := t.eachSearchPage(ctx, jql, []string{"summary", "created", "status", "issuetype", "parent"}, "",
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

	if !t.expandKnownRefused() {
		history, truncated, honoured, err := t.inlineHistory(ctx, keys, statuses)
		switch {
		case err != nil:
			return nil, err
		case honoured:
			// Only the issues whose history Jira declined to inline in full
			// need a request of their own, which is normally none of them.
			if len(truncated) > 0 {
				rest, err := t.changelogsFor(ctx, truncated, statuses)
				if err != nil {
					return nil, err
				}
				for key, changes := range rest {
					history[key] = changes
				}
			}
			return history, nil
		}
	}

	return t.changelogsFor(ctx, keys, statuses)
}

// searchChunk is how many issue keys go into one JQL "key IN (...)" clause.
// Jira pages at a hundred anyway, so a larger chunk would only make the query
// string longer for no benefit.
const searchChunk = 100

// inlineHistory tries to read every issue's changelog from the search response
// rather than from one request per issue.
//
// It reports whether the site honoured the request. A site that quietly ignores
// the expand returns issues with no changelog attached at all, which is
// indistinguishable from "nothing ever happened" unless the absence of the
// field is checked for specifically — and charting a sprint's worth of work as
// if nothing had happened is a far worse failure than making a few extra
// requests.
func (t *Tracker) inlineHistory(
	ctx context.Context,
	keys []domain.IssueKey,
	statuses map[string]domain.IssueStatus,
) (history map[domain.IssueKey][]domain.StatusChange, truncated []domain.IssueKey, honoured bool, err error) {
	chunks := chunkKeys(keys, searchChunk)

	// Three outcomes, not two. "Every issue came back with a changelog
	// attached" and "no issue came back at all" are both free of missing
	// changelogs, but only the first is evidence that the site honours the
	// expand — and concluding it does on no evidence would chart a sprint's
	// work as if nothing had ever happened to it.
	type result struct {
		history   map[domain.IssueKey][]domain.StatusChange
		truncated []domain.IssueKey
		sawIssue  bool
		sawGap    bool
	}

	results, err := parallel.Map(ctx, chunks, maxConcurrency, func(ctx context.Context, chunk []domain.IssueKey) (result, error) {
		var out result
		out.history = map[domain.IssueKey][]domain.StatusChange{}

		quoted := make([]string, 0, len(chunk))
		for _, key := range chunk {
			quoted = append(quoted, `"`+string(key)+`"`)
		}
		jql := "key IN (" + strings.Join(quoted, ",") + ") ORDER BY key ASC"

		err := t.eachSearchPage(ctx, jql, []string{"summary"}, "changelog", func(_ []issueJSON, raw []json.RawMessage) error {
			for _, entry := range raw {
				var envelope struct {
					Key       string               `json:"key"`
					Changelog *inlineChangelogJSON `json:"changelog"`
				}
				if err := json.Unmarshal(entry, &envelope); err != nil {
					return fmt.Errorf("decoding issue: %w", err)
				}
				out.sawIssue = true
				if envelope.Changelog == nil {
					// The expand was ignored. Nothing in this chunk can be
					// trusted, and neither can any other.
					out.sawGap = true
					return nil
				}
				key := domain.IssueKey(envelope.Key)
				if envelope.Changelog.truncated() {
					out.truncated = append(out.truncated, key)
					continue
				}
				changes, err := statusChangesIn(envelope.Changelog.Histories, key, statuses)
				if err != nil {
					return err
				}
				if len(changes) > 0 {
					out.history[key] = changes
				}
			}
			return nil
		})
		return out, err
	})
	if err != nil {
		// A refusal is a fact about the site and is worth remembering;
		// anything else is this request's problem alone.
		if refusesExpand(err) {
			t.refuseExpand(err)
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}

	history = make(map[domain.IssueKey][]domain.StatusChange, len(keys))
	sawIssue := false
	for _, out := range results {
		if out.sawGap {
			t.refuseExpand(nil)
			return nil, nil, false, nil
		}
		sawIssue = sawIssue || out.sawIssue
		for key, changes := range out.history {
			history[key] = changes
		}
		truncated = append(truncated, out.truncated...)
	}
	if !sawIssue {
		// No evidence either way. Fall back for this call without concluding
		// anything permanent about the site.
		return nil, nil, false, nil
	}
	return history, truncated, true, nil
}

// changelogsFor reads one changelog per issue. It is the fallback, and on a
// site that inlines changelogs it runs for at most a handful of issues.
func (t *Tracker) changelogsFor(
	ctx context.Context,
	keys []domain.IssueKey,
	statuses map[string]domain.IssueStatus,
) (map[domain.IssueKey][]domain.StatusChange, error) {
	// One request per issue, but concurrently: nothing about one issue's
	// changelog depends on another's, and serially this was the single largest
	// block of wall-clock time in a build.
	changelogs, err := parallel.Map(ctx, keys, maxConcurrency,
		func(ctx context.Context, key domain.IssueKey) ([]domain.StatusChange, error) {
			return t.changelogFor(ctx, key, statuses)
		})
	if err != nil {
		return nil, err
	}

	history := make(map[domain.IssueKey][]domain.StatusChange, len(keys))
	for i, changes := range changelogs {
		if len(changes) > 0 {
			history[keys[i]] = changes
		}
	}
	return history, nil
}

func chunkKeys(keys []domain.IssueKey, size int) [][]domain.IssueKey {
	chunks := make([][]domain.IssueKey, 0, (len(keys)+size-1)/size)
	for start := 0; start < len(keys); start += size {
		end := start + size
		if end > len(keys) {
			end = len(keys)
		}
		chunks = append(chunks, keys[start:end])
	}
	return chunks
}

// refusesExpand reports whether a failure says the site cannot do this at all,
// as opposed to this request having gone wrong.
//
// 400 is a rejected parameter, 404 an endpoint that is not there, and 410 the
// deprecated search endpoint on a site that has retired it. All three are facts
// about the site. Anything else — a timeout, a 500, a rate limit — is this
// request's problem and must not disable the fast path for the whole process.
func refusesExpand(err error) bool {
	var status *httpclient.StatusError
	if !errors.As(err, &status) {
		return false
	}
	switch status.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusGone:
		return true
	}
	return false
}

func (t *Tracker) expandKnownRefused() bool {
	t.expandMu.Lock()
	defer t.expandMu.Unlock()
	return t.expandRefused
}

// refuseExpand records that this site will not inline changelogs, and says so
// once. The consequence is real — a request per sub-task on every build — and
// invisible otherwise.
func (t *Tracker) refuseExpand(cause error) {
	t.expandMu.Lock()
	already := t.expandRefused
	t.expandRefused = true
	t.expandMu.Unlock()

	if already {
		return
	}
	t.logger.Warn("this Jira site will not attach changelogs to a search",
		slog.String("consequence", "status history now costs one request per sub-task on every retrospective"),
		slog.Any("error", cause))
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

		decoded, err := statusChangesIn(page.Values, key, statuses)
		if err != nil {
			return nil, err
		}
		changes = append(changes, decoded...)

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

// statusChangesIn turns changelog entries into status transitions, dropping
// every other kind of field change.
//
// It is shared by both sources deliberately. The per-issue endpoint and the
// search expand return the same entries under different names, and decoding
// them twice would be two chances to disagree about what a transition is.
func statusChangesIn(entries []changelogJSON, key domain.IssueKey, statuses map[string]domain.IssueStatus) ([]domain.StatusChange, error) {
	var changes []domain.StatusChange

	for _, entry := range entries {
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
