package configstore

import (
	"fmt"
	"strings"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// The types below are the on-disk shape. They are separate from the domain
// types on purpose: the file format is an external contract that should be free
// to drift from the model, and the translation between them is where a
// malformed file is caught.

type fileFormat struct {
	Projects []projectFormat `yaml:"projects"`
}

type projectFormat struct {
	ID        string          `yaml:"id"`
	Name      string          `yaml:"name"`
	Settings  settingsFormat  `yaml:"settings"`
	Tracker   trackerFormat   `yaml:"tracker"`
	Repos     []repoFormat    `yaml:"repos"`
	Reviewers reviewersFormat `yaml:"reviewers"`
}

type settingsFormat struct {
	Timezone string `yaml:"timezone"`
}

type trackerFormat struct {
	Type       string `yaml:"type"`
	ProjectKey string `yaml:"projectKey"`
	BoardID    string `yaml:"boardId"`
}

type repoFormat struct {
	Host  string `yaml:"host"`
	Owner string `yaml:"owner"`
	Name  string `yaml:"name"`
}

type reviewersFormat struct {
	ExcludeBots   *bool    `yaml:"excludeBots"`
	ExcludeLogins []string `yaml:"excludeLogins"`
}

// SupportedTrackerType and SupportedRepoHost are the only values understood
// today. Unknown values are rejected at load rather than producing an app that
// starts cleanly and then fails on first use.
const (
	SupportedTrackerType = "jira"
	SupportedRepoHost    = "github"
)

func (f fileFormat) toDomain() ([]domain.Project, error) {
	if len(f.Projects) == 0 {
		return nil, fmt.Errorf("no projects defined")
	}

	projects := make([]domain.Project, 0, len(f.Projects))
	seen := map[string]bool{}

	for i, raw := range f.Projects {
		project, err := raw.toDomain()
		if err != nil {
			return nil, fmt.Errorf("project %d: %w", i, err)
		}
		if seen[string(project.ID)] {
			return nil, fmt.Errorf("duplicate project id %q", project.ID)
		}
		seen[string(project.ID)] = true
		projects = append(projects, project)
	}
	return projects, nil
}

func (p projectFormat) toDomain() (domain.Project, error) {
	if strings.TrimSpace(p.ID) == "" {
		return domain.Project{}, fmt.Errorf("id is required")
	}
	if !strings.EqualFold(p.Tracker.Type, SupportedTrackerType) {
		return domain.Project{}, fmt.Errorf("unsupported tracker type %q (only %q is implemented)", p.Tracker.Type, SupportedTrackerType)
	}
	if strings.TrimSpace(p.Tracker.ProjectKey) == "" {
		return domain.Project{}, fmt.Errorf("tracker.projectKey is required")
	}
	if strings.TrimSpace(p.Tracker.BoardID) == "" {
		return domain.Project{}, fmt.Errorf("tracker.boardId is required to list sprints")
	}
	if len(p.Repos) == 0 {
		return domain.Project{}, fmt.Errorf("at least one repo is required")
	}

	repos := make([]domain.RepoRef, 0, len(p.Repos))
	for j, repo := range p.Repos {
		if !strings.EqualFold(repo.Host, SupportedRepoHost) {
			return domain.Project{}, fmt.Errorf("repo %d: unsupported host %q (only %q is implemented)", j, repo.Host, SupportedRepoHost)
		}
		if strings.TrimSpace(repo.Owner) == "" || strings.TrimSpace(repo.Name) == "" {
			return domain.Project{}, fmt.Errorf("repo %d: owner and name are required", j)
		}
		repos = append(repos, domain.RepoRef{Owner: repo.Owner, Name: repo.Name})
	}

	settings := domain.ProjectSettings{Timezone: p.Settings.Timezone}
	if _, err := settings.Location(); err != nil {
		return domain.Project{}, err
	}

	excludeBots := true // filtering automation out of review states is the sane default
	if p.Reviewers.ExcludeBots != nil {
		excludeBots = *p.Reviewers.ExcludeBots
	}

	name := p.Name
	if strings.TrimSpace(name) == "" {
		name = p.ID
	}

	return domain.Project{
		ID:       domain.ProjectID(p.ID),
		Name:     name,
		Settings: settings,
		Tracker: domain.TrackerRef{
			ProjectKey: p.Tracker.ProjectKey,
			BoardID:    p.Tracker.BoardID,
		},
		Repos: repos,
		Reviewers: domain.ReviewerPolicy{
			ExcludeBots:   excludeBots,
			ExcludeLogins: p.Reviewers.ExcludeLogins,
		},
	}, nil
}

func fromDomain(projects []domain.Project) fileFormat {
	out := fileFormat{Projects: make([]projectFormat, 0, len(projects))}
	for _, project := range projects {
		repos := make([]repoFormat, 0, len(project.Repos))
		for _, repo := range project.Repos {
			repos = append(repos, repoFormat{Host: SupportedRepoHost, Owner: repo.Owner, Name: repo.Name})
		}
		excludeBots := project.Reviewers.ExcludeBots
		out.Projects = append(out.Projects, projectFormat{
			ID:       string(project.ID),
			Name:     project.Name,
			Settings: settingsFormat{Timezone: project.Settings.Timezone},
			Tracker: trackerFormat{
				Type:       SupportedTrackerType,
				ProjectKey: project.Tracker.ProjectKey,
				BoardID:    project.Tracker.BoardID,
			},
			Repos: repos,
			Reviewers: reviewersFormat{
				ExcludeBots:   &excludeBots,
				ExcludeLogins: project.Reviewers.ExcludeLogins,
			},
		})
	}
	return out
}
