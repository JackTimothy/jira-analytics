package usecase

import (
	"context"
	"fmt"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// Sprints lists the sprints available for a project.
type Sprints struct {
	projects ProjectStore
	tracker  IssueTracker
}

func NewSprints(projects ProjectStore, tracker IssueTracker) *Sprints {
	return &Sprints{projects: projects, tracker: tracker}
}

func (s *Sprints) List(ctx context.Context, projectID domain.ProjectID) ([]domain.Sprint, error) {
	project, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sprints, err := s.tracker.ListSprints(ctx, project.Tracker)
	if err != nil {
		return nil, fmt.Errorf("listing sprints: %w", err)
	}
	return sprints, nil
}
