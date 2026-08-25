package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jacktimothy/jira-analytics/internal/domain"
	"github.com/jacktimothy/jira-analytics/internal/usecase"
)

// RetrospectiveBuilder is the one use case the API currently exposes beyond
// project administration.
type RetrospectiveBuilder interface {
	Build(ctx context.Context, req usecase.RetrospectiveRequest) (domain.Retrospective, error)
}

// SprintLister lists the sprints available for a project.
type SprintLister interface {
	List(ctx context.Context, projectID domain.ProjectID) ([]domain.Sprint, error)
}

type Server struct {
	projects       usecase.ProjectStore
	sprints        SprintLister
	retrospectives RetrospectiveBuilder
	logger         *slog.Logger
}

func NewServer(projects usecase.ProjectStore, sprints SprintLister, retrospectives RetrospectiveBuilder, logger *slog.Logger) *Server {
	return &Server{projects: projects, sprints: sprints, retrospectives: retrospectives, logger: logger}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/projects", s.listProjects)
	mux.HandleFunc("GET /api/v1/projects/{projectID}", s.getProject)
	mux.HandleFunc("PATCH /api/v1/projects/{projectID}/settings", s.updateSettings)
	mux.HandleFunc("GET /api/v1/projects/{projectID}/sprints", s.listSprints)
	mux.HandleFunc("GET /api/v1/projects/{projectID}/sprints/{sprintID}/retrospective", s.retrospective)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.logger, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.projects.List(r.Context())
	if err != nil {
		s.fail(w, "listing projects", err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, presentProjects(projects))
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.projects.Get(r.Context(), domain.ProjectID(r.PathValue("projectID")))
	if err != nil {
		s.fail(w, "getting project", err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, presentProject(project))
}

type updateSettingsRequest struct {
	Timezone *string `json:"timezone"`
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	id := domain.ProjectID(r.PathValue("projectID"))

	var body updateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, s.logger, http.StatusBadRequest, "request body must be a JSON object")
		return
	}

	current, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.fail(w, "getting project", err)
		return
	}

	settings := current.Settings
	if body.Timezone != nil {
		settings.Timezone = *body.Timezone
	}

	if err := s.projects.UpdateSettings(r.Context(), id, settings); err != nil {
		// An unrecognised timezone is the caller's mistake, not a server fault,
		// and it must be rejected rather than quietly defaulted: the wrong zone
		// silently changes which work counts as committed.
		if errors.Is(err, domain.ErrInvalidSettings) {
			writeError(w, s.logger, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.fail(w, "updating settings", err)
		return
	}

	updated, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.fail(w, "getting project", err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, presentProject(updated))
}

func (s *Server) listSprints(w http.ResponseWriter, r *http.Request) {
	sprints, err := s.sprints.List(r.Context(), domain.ProjectID(r.PathValue("projectID")))
	if err != nil {
		s.fail(w, "listing sprints", err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, presentSprints(sprints))
}

func (s *Server) retrospective(w http.ResponseWriter, r *http.Request) {
	scope := domain.Scope(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = domain.ScopeAll
	}
	if !scope.Valid() {
		writeError(w, s.logger, http.StatusBadRequest, `scope must be "all" or "committed"`)
		return
	}

	result, err := s.retrospectives.Build(r.Context(), usecase.RetrospectiveRequest{
		ProjectID: domain.ProjectID(r.PathValue("projectID")),
		SprintID:  domain.SprintID(r.PathValue("sprintID")),
		Scope:     scope,
	})
	if err != nil {
		s.fail(w, "building retrospective", err)
		return
	}
	writeJSON(w, s.logger, http.StatusOK, presentRetrospective(result))
}

// fail maps a use case error onto a status code. Only errors the domain marks
// as client-caused become 4xx; everything else is a server fault and is logged
// with its detail rather than echoed to the caller.
func (s *Server) fail(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, s.logger, http.StatusNotFound, err.Error())
	case errors.Is(err, domain.ErrInvalidSettings):
		writeError(w, s.logger, http.StatusUnprocessableEntity, err.Error())
	default:
		s.logger.Error(action, slog.Any("error", err))
		writeError(w, s.logger, http.StatusInternalServerError, "internal error")
	}
}
