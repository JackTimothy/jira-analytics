package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/usecase"
)

type stubProjects struct {
	projects  map[domain.ProjectID]domain.Project
	updates   []domain.ProjectSettings
	updateErr error
}

func (s *stubProjects) List(context.Context) ([]domain.Project, error) {
	out := make([]domain.Project, 0, len(s.projects))
	for _, project := range s.projects {
		out = append(out, project)
	}
	return out, nil
}

func (s *stubProjects) Get(_ context.Context, id domain.ProjectID) (domain.Project, error) {
	project, ok := s.projects[id]
	if !ok {
		return domain.Project{}, fmt.Errorf("%w: project %s", domain.ErrNotFound, id)
	}
	return project, nil
}

func (s *stubProjects) UpdateSettings(_ context.Context, id domain.ProjectID, settings domain.ProjectSettings) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updates = append(s.updates, settings)
	project := s.projects[id]
	project.Settings = settings
	s.projects[id] = project
	return nil
}

type stubSprints struct{ sprints []domain.Sprint }

func (s stubSprints) List(context.Context, domain.ProjectID) ([]domain.Sprint, error) {
	return s.sprints, nil
}

type stubRetrospectives struct {
	result  domain.Retrospective
	lastReq usecase.RetrospectiveRequest
	err     error
}

func (s *stubRetrospectives) Build(_ context.Context, req usecase.RetrospectiveRequest) (domain.Retrospective, error) {
	s.lastReq = req
	return s.result, s.err
}

func newTestServer() (*Server, *stubProjects, *stubRetrospectives) {
	projects := &stubProjects{projects: map[domain.ProjectID]domain.Project{
		"activation": {
			ID:       "activation",
			Name:     "Activation",
			Settings: domain.ProjectSettings{Timezone: "America/New_York"},
			Tracker:  domain.TrackerRef{ProjectKey: "PROJ", BoardID: "45"},
			Repos:    []domain.RepoRef{{Owner: "org", Name: "repo"}},
		},
	}}
	retros := &stubRetrospectives{}
	sprints := stubSprints{sprints: []domain.Sprint{{
		ID:    "100",
		Name:  "Sprint 26-31",
		Start: time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC),
	}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(projects, sprints, retros, logger), projects, retros
}

func do(t *testing.T, server *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	server, _, _ := newTestServer()
	if rec := do(t, server, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
}

func TestListProjects(t *testing.T) {
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}

	var got []projectView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(got) != 1 || got[0].ID != "activation" {
		t.Fatalf("unexpected body: %s", rec.Body)
	}
	if got[0].Repos[0] != "org/repo" {
		t.Errorf("repos = %v", got[0].Repos)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

func TestUpdateSettingsAppliesTimezone(t *testing.T) {
	server, projects, _ := newTestServer()
	rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", `{"timezone":"Europe/Berlin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(projects.updates) != 1 || projects.updates[0].Timezone != "Europe/Berlin" {
		t.Fatalf("store received %+v", projects.updates)
	}

	var got projectView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Settings.Timezone != "Europe/Berlin" {
		t.Errorf("response timezone = %q", got.Settings.Timezone)
	}
}

func TestUpdateSettingsRejectsUnknownTimezoneWith422(t *testing.T) {
	server, projects, _ := newTestServer()
	projects.updateErr = fmt.Errorf("%w: unknown timezone %q", domain.ErrInvalidSettings, "Nowhere/Land")

	rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", `{"timezone":"Nowhere/Land"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d, want 422: %s", rec.Code, rec.Body)
	}
}

func TestUpdateSettingsRejectsMalformedBody(t *testing.T) {
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

func TestUpdateSettingsLeavesOmittedFieldsAlone(t *testing.T) {
	server, projects, _ := newTestServer()
	rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	if projects.updates[0].Timezone != "America/New_York" {
		t.Errorf("an empty patch changed the timezone to %q", projects.updates[0].Timezone)
	}
}

func TestListSprints(t *testing.T) {
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	var got []sprintView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Sprint 26-31" {
		t.Errorf("unexpected body: %s", rec.Body)
	}
}

func TestRetrospectiveDefaultsToAllScope(t *testing.T) {
	server, _, retros := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}
	if retros.lastReq.Scope != domain.ScopeAll {
		t.Errorf("scope = %q, want all", retros.lastReq.Scope)
	}
	if retros.lastReq.ProjectID != "activation" || retros.lastReq.SprintID != "100" {
		t.Errorf("unexpected request: %+v", retros.lastReq)
	}
}

func TestRetrospectiveHonoursCommittedScope(t *testing.T) {
	server, _, retros := newTestServer()
	do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective?scope=committed", "")
	if retros.lastReq.Scope != domain.ScopeCommitted {
		t.Errorf("scope = %q, want committed", retros.lastReq.Scope)
	}
}

func TestRetrospectiveRejectsUnknownScope(t *testing.T) {
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective?scope=sideways", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

func TestRetrospectivePresentsIntervalsAndWarnings(t *testing.T) {
	server, _, retros := newTestServer()
	due := domain.NewCalendarDate(2026, time.August, 17)
	retros.result = domain.Retrospective{
		Sprint: domain.Sprint{ID: "100", Name: "Sprint 26-31"},
		Groups: []domain.ParentGroup{{
			Parent:  domain.WorkItem{Key: "PROJ-1", Summary: "Story", DueDate: &due},
			InScope: true,
			Rows: []domain.Row{{
				Kind: domain.RowSubTask, Key: "PROJ-11", Label: "API",
				Intervals: []domain.Interval{{
					State: domain.StateApproved,
					From:  time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
					To:    time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC),
				}},
			}},
		}},
		Warnings: []string{"PROJ-20: no linked branch or pull request found"},
	}

	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective", "")
	var got retrospectiveView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if len(got.Parents) != 1 || got.Parents[0].Key != "PROJ-1" || !got.Parents[0].InScope {
		t.Fatalf("unexpected parents: %s", rec.Body)
	}
	if got.Parents[0].DueDate == nil || *got.Parents[0].DueDate != "2026-08-17" {
		t.Errorf("due date rendered as %v, want 2026-08-17", got.Parents[0].DueDate)
	}
	interval := got.Parents[0].Rows[0].Intervals[0]
	if interval.State != "APPROVED" {
		t.Errorf("state = %q, want APPROVED", interval.State)
	}
	if len(got.Warnings) != 1 {
		t.Errorf("warnings = %v", got.Warnings)
	}
}

func TestRetrospectiveAlwaysRendersWarningsAsAnArray(t *testing.T) {
	// A nil slice would marshal to null and force the GUI to handle two shapes.
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective", "")
	if !strings.Contains(rec.Body.String(), `"warnings":[]`) {
		t.Errorf("expected an empty array, got %s", rec.Body)
	}
}

func TestUpdateSettingsAppliesWorkingHours(t *testing.T) {
	server, projects, _ := newTestServer()
	rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings",
		`{"workingHours":{"days":["monday","tuesday","wednesday"],"start":"09:00","end":"17:30"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}

	applied := projects.updates[0].WorkingHours
	if applied == nil {
		t.Fatal("working hours did not reach the store")
	}
	if len(applied.Days) != 3 || applied.Start != 9*60 || applied.End != 17*60+30 {
		t.Errorf("stored %+v", applied)
	}
	// Timezone was omitted from the patch and must survive untouched.
	if projects.updates[0].Timezone != "America/New_York" {
		t.Errorf("timezone changed to %q", projects.updates[0].Timezone)
	}

	var got projectView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Settings.WorkingHours.Start != "09:00" || got.Settings.WorkingHours.End != "17:30" {
		t.Errorf("response hours = %+v", got.Settings.WorkingHours)
	}
}

func TestUpdateSettingsRejectsMalformedWorkingHours(t *testing.T) {
	server, _, _ := newTestServer()
	for name, body := range map[string]string{
		"bad day":   `{"workingHours":{"days":["funday"],"start":"09:00","end":"17:00"}}`,
		"bad clock": `{"workingHours":{"days":["monday"],"start":"9am","end":"17:00"}}`,
	} {
		rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422: %s", name, rec.Code, rec.Body)
		}
	}
}

func TestProjectsExposeTheScheduleWithDefaults(t *testing.T) {
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation", "")
	var got projectView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	hours := got.Settings.WorkingHours
	if hours.Start != "08:00" || hours.End != "18:00" || len(hours.Days) != 5 {
		t.Errorf("expected the default schedule on an unconfigured project, got %+v", hours)
	}
}

func TestUpdateSettingsAppliesHolidays(t *testing.T) {
	server, projects, _ := newTestServer()
	rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings",
		`{"holidays":[{"date":"2026-11-26","name":"Thanksgiving"},{"date":"2026-12-25"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}

	applied := projects.updates[0]
	if len(applied.Holidays) != 2 ||
		applied.Holidays[0] != (domain.Holiday{Date: domain.NewCalendarDate(2026, 11, 26), Name: "Thanksgiving"}) ||
		applied.Holidays[1] != (domain.Holiday{Date: domain.NewCalendarDate(2026, 12, 25)}) {
		t.Errorf("stored holidays %+v", applied.Holidays)
	}
	// Neither the timezone nor the working hours were in the patch.
	if applied.Timezone != "America/New_York" || applied.WorkingHours != nil {
		t.Errorf("other settings changed: %+v", applied)
	}

	var got projectView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(got.Settings.Holidays) != 2 || got.Settings.Holidays[0] != (holidayView{Date: "2026-11-26", Name: "Thanksgiving"}) {
		t.Errorf("response holidays = %+v", got.Settings.Holidays)
	}

	// An empty list clears them; a patch without the field leaves them alone.
	do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", `{"timezone":"Europe/London"}`)
	if len(projects.updates[1].Holidays) != 2 {
		t.Errorf("a patch without holidays dropped them: %+v", projects.updates[1].Holidays)
	}
	do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", `{"holidays":[]}`)
	if len(projects.updates[2].Holidays) != 0 {
		t.Errorf("an empty list left %+v", projects.updates[2].Holidays)
	}
}

func TestUpdateSettingsRejectsMalformedHolidays(t *testing.T) {
	server, _, _ := newTestServer()
	for name, body := range map[string]string{
		"not a date":  `{"holidays":[{"date":"26/11/2026"}]}`,
		"no date":     `{"holidays":[{"name":"Someday"}]}`,
		"no such day": `{"holidays":[{"date":"2026-02-30"}]}`,
	} {
		rec := do(t, server, http.MethodPatch, "/api/v1/projects/activation/settings", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422: %s", name, rec.Code, rec.Body)
		}
	}
}

// A project with no holidays says so with an empty list, not null.
func TestProjectsExposeAnEmptyHolidayList(t *testing.T) {
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation", "")
	if !strings.Contains(rec.Body.String(), `"holidays":[]`) {
		t.Errorf("expected an empty holiday list in %s", rec.Body)
	}
}

func TestRetrospectivePresentsTheHolidaysOnTheAxis(t *testing.T) {
	server, _, retros := newTestServer()
	from := time.Date(2026, 9, 4, 22, 0, 0, 0, time.UTC)
	retros.result = domain.Retrospective{
		Sprint: domain.Sprint{ID: "100", Name: "Sprint 26-31"},
		Axis: []domain.AxisSegment{
			{From: from.Add(-4 * time.Hour), To: from, Kind: domain.SegmentWorking},
			{From: from, To: from.Add(82 * time.Hour), Kind: domain.SegmentOffHours, Holidays: []domain.Holiday{
				{Date: domain.NewCalendarDate(2026, 9, 7), Name: "Labor Day"},
			}},
		},
	}

	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective", "")
	var got retrospectiveView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(got.Axis) != 2 || got.Axis[0].Holidays != nil {
		t.Fatalf("axis = %+v, want a working segment with no holidays first", got.Axis)
	}
	if h := got.Axis[1].Holidays; len(h) != 1 || h[0] != (holidayView{Date: "2026-09-07", Name: "Labor Day"}) {
		t.Errorf("band holidays = %+v, want Labor Day", h)
	}
}

func TestRetrospectivePresentsCycleTime(t *testing.T) {
	server, _, retros := newTestServer()
	story := domain.WorkItem{Key: "PROJ-1", Summary: "Story", Type: "Story", Points: 2}
	cycle := domain.CycleTime{
		Item:     story,
		Started:  time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC),
		Finished: time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC),
		Working:  5 * time.Hour,
	}
	row := []domain.Row{{Kind: domain.RowWorkItem, Key: "PROJ-1", Label: "Story"}}
	retros.result = domain.Retrospective{
		Sprint: domain.Sprint{ID: "100", Name: "Sprint 26-31"},
		Groups: []domain.ParentGroup{
			{Parent: story, Rows: row},
			{Parent: domain.WorkItem{Key: "PROJ-2", Summary: "Open"}, Rows: row},
		},
		CycleTime: domain.SprintCycleTime{
			Items:       []domain.CycleTime{cycle},
			Counted:     []domain.CycleTime{cycle},
			PerPoint:    150 * time.Minute,
			Unestimated: []domain.IssueKey{"PROJ-3"},
		},
	}

	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective", "")
	var got retrospectiveView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if span := got.Parents[0].CycleTime; span == nil || span.Hours != 5 || !span.Finished.Equal(cycle.Finished) {
		t.Errorf("PROJ-1 cycle time = %+v, want 5 hours finishing %s", span, cycle.Finished)
	}
	if got.Parents[1].CycleTime != nil {
		t.Errorf("PROJ-2 has a cycle time of %+v, want none — it is not finished", got.Parents[1].CycleTime)
	}
	sprint := got.CycleTime
	if sprint.HoursPerPoint == nil || *sprint.HoursPerPoint != 2.5 {
		t.Errorf("hours per point = %v, want 2.5", sprint.HoursPerPoint)
	}
	if len(sprint.Items) != 1 || sprint.Items[0].Key != "PROJ-1" ||
		sprint.Items[0].Hours != 5 || sprint.Items[0].HoursPerPoint != 2.5 || sprint.Items[0].Points != 2 {
		t.Errorf("items = %+v, want PROJ-1 at 5 hours, 2.5 per point", sprint.Items)
	}
	if len(sprint.Unestimated) != 1 || sprint.Unestimated[0] != "PROJ-3" {
		t.Errorf("unestimated = %v, want [PROJ-3]", sprint.Unestimated)
	}
}

func TestRetrospectiveRendersEmptyCycleTimeAsNullAndArrays(t *testing.T) {
	// Nothing finished is not zero hours per point, and nil slices would
	// marshal to null and force the GUI to handle two shapes.
	server, _, _ := newTestServer()
	rec := do(t, server, http.MethodGet, "/api/v1/projects/activation/sprints/100/retrospective", "")
	var body struct {
		CycleTime json.RawMessage `json:"cycleTime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	want := `{"hoursPerPoint":null,"items":[],"unestimated":[],"unstarted":[]}`
	if string(body.CycleTime) != want {
		t.Errorf("cycleTime = %s, want %s", body.CycleTime, want)
	}
}
