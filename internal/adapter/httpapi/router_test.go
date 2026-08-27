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
