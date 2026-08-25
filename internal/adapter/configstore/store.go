// Package configstore implements the ProjectStore port against a YAML file.
//
// It is deliberately the whole persistence story for now. Because the use cases
// depend on the port and not on this package, replacing it with a database
// later means writing one new adapter and changing one line of wiring.
package configstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// ErrProjectNotFound is returned for an unknown project id. It wraps the domain
// sentinel so transport layers can translate it without knowing this package.
var ErrProjectNotFound = fmt.Errorf("%w: project", domain.ErrNotFound)

// Store reads projects from a YAML file and writes user-editable settings back
// to it. Reads are served from memory; the file is the durable record.
type Store struct {
	path string

	mu       sync.RWMutex
	projects []domain.Project
}

// Load reads the project file into memory. A missing file is an error rather
// than an empty list: running with no projects is never what the operator
// intended, and silently starting empty hides a misconfigured path.
func Load(path string) (*Store, error) {
	store := &Store{path: path}
	if err := store.reload(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) reload() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("reading project file %s: %w", s.path, err)
	}

	var file fileFormat
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parsing project file %s: %w", s.path, err)
	}

	projects, err := file.toDomain()
	if err != nil {
		return fmt.Errorf("in project file %s: %w", s.path, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects = projects
	return nil
}

func (s *Store) List(_ context.Context) ([]domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]domain.Project, len(s.projects))
	copy(out, s.projects)
	return out, nil
}

func (s *Store) Get(_ context.Context, id domain.ProjectID) (domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, project := range s.projects {
		if project.ID == id {
			return project, nil
		}
	}
	return domain.Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, id)
}

// UpdateSettings validates and persists a project's settings.
//
// The write is atomic: the whole file is rendered to a temporary file in the
// same directory and renamed over the original, so a crash mid-write cannot
// leave an operator with a truncated project file and no way to start.
func (s *Store) UpdateSettings(_ context.Context, id domain.ProjectID, settings domain.ProjectSettings) error {
	if _, err := settings.Location(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	index := -1
	for i, project := range s.projects {
		if project.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	}

	updated := make([]domain.Project, len(s.projects))
	copy(updated, s.projects)
	updated[index].Settings = settings

	if err := s.writeAtomically(updated); err != nil {
		return err
	}

	s.projects = updated
	return nil
}

func (s *Store) writeAtomically(projects []domain.Project) error {
	encoded, err := yaml.Marshal(fromDomain(projects))
	if err != nil {
		return fmt.Errorf("encoding project file: %w", err)
	}

	dir := filepath.Dir(s.path)
	temp, err := os.CreateTemp(dir, ".projects-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temporary project file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // no-op once the rename succeeds

	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("writing temporary project file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("syncing temporary project file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("closing temporary project file: %w", err)
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return fmt.Errorf("replacing project file: %w", err)
	}
	return nil
}
