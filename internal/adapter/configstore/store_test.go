package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

const validFile = `
projects:
  - id: activation
    name: Activation POD
    settings:
      timezone: America/New_York
    tracker: { type: jira, projectKey: PROJ, boardId: "45" }
    repos:
      - { host: github, owner: example-org, name: example-repo }
      - { host: github, owner: example-org, name: other-repo }
    reviewers:
      excludeBots: true
      excludeLogins: [some-ai-reviewer]
`

func writeStore(t *testing.T, contents string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projects.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return store, path
}

func TestLoadParsesAProject(t *testing.T) {
	store, _ := writeStore(t, validFile)

	projects, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}

	project := projects[0]
	if project.ID != "activation" || project.Name != "Activation POD" {
		t.Errorf("unexpected identity: %+v", project)
	}
	if project.Tracker.ProjectKey != "PROJ" || project.Tracker.BoardID != "45" {
		t.Errorf("unexpected tracker: %+v", project.Tracker)
	}
	if len(project.Repos) != 2 || project.Repos[0].String() != "example-org/example-repo" {
		t.Errorf("unexpected repos: %+v", project.Repos)
	}
	if !project.Reviewers.ExcludeBots || len(project.Reviewers.ExcludeLogins) != 1 {
		t.Errorf("unexpected reviewer policy: %+v", project.Reviewers)
	}
}

func TestLoadRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		wantErr  string
	}{
		{"empty file", "projects: []", "no projects defined"},
		{"missing id", "projects:\n  - name: x\n", "id is required"},
		{
			name:     "unsupported tracker",
			contents: "projects:\n  - id: a\n    tracker: { type: linear, projectKey: X, boardId: \"1\" }\n    repos: [{host: github, owner: o, name: r}]\n",
			wantErr:  "unsupported tracker type",
		},
		{
			name:     "missing board id",
			contents: "projects:\n  - id: a\n    tracker: { type: jira, projectKey: X }\n    repos: [{host: github, owner: o, name: r}]\n",
			wantErr:  "boardId is required",
		},
		{
			name:     "no repos",
			contents: "projects:\n  - id: a\n    tracker: { type: jira, projectKey: X, boardId: \"1\" }\n    repos: []\n",
			wantErr:  "at least one repo is required",
		},
		{
			name:     "unknown timezone",
			contents: "projects:\n  - id: a\n    settings: { timezone: Mars/Olympus_Mons }\n    tracker: { type: jira, projectKey: X, boardId: \"1\" }\n    repos: [{host: github, owner: o, name: r}]\n",
			wantErr:  "unknown timezone",
		},
		{
			name:     "duplicate ids",
			contents: "projects:\n  - id: a\n    tracker: { type: jira, projectKey: X, boardId: \"1\" }\n    repos: [{host: github, owner: o, name: r}]\n  - id: a\n    tracker: { type: jira, projectKey: Y, boardId: \"2\" }\n    repos: [{host: github, owner: o, name: r}]\n",
			wantErr:  "duplicate project id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "projects.yaml")
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadFailsLoudlyOnMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("expected an error for a missing project file")
	}
}

func TestUpdateSettingsPersistsAndReloads(t *testing.T) {
	store, path := writeStore(t, validFile)
	ctx := context.Background()

	if err := store.UpdateSettings(ctx, "activation", domain.ProjectSettings{Timezone: "Europe/Berlin"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	project, err := store.Get(ctx, "activation")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if project.Settings.Timezone != "Europe/Berlin" {
		t.Errorf("in-memory timezone = %q, want Europe/Berlin", project.Settings.Timezone)
	}

	// The file, not memory, is the durable record: reload from disk and check.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	project, err = reloaded.Get(ctx, "activation")
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if project.Settings.Timezone != "Europe/Berlin" {
		t.Errorf("persisted timezone = %q, want Europe/Berlin", project.Settings.Timezone)
	}
	// Everything else must survive the round trip untouched.
	if project.Tracker.BoardID != "45" || len(project.Repos) != 2 || len(project.Reviewers.ExcludeLogins) != 1 {
		t.Errorf("round trip lost configuration: %+v", project)
	}
}

func TestUpdateSettingsRejectsUnknownTimezoneWithoutTouchingTheFile(t *testing.T) {
	store, path := writeStore(t, validFile)
	before, _ := os.ReadFile(path)

	err := store.UpdateSettings(context.Background(), "activation", domain.ProjectSettings{Timezone: "Nowhere/Land"})
	if err == nil {
		t.Fatal("expected an error for an unknown timezone")
	}

	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("an invalid update rewrote the project file")
	}
}

func TestUpdateSettingsUnknownProject(t *testing.T) {
	store, _ := writeStore(t, validFile)
	err := store.UpdateSettings(context.Background(), "nope", domain.ProjectSettings{Timezone: "UTC"})
	if !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("got %v, want ErrProjectNotFound", err)
	}
}

func TestGetUnknownProject(t *testing.T) {
	store, _ := writeStore(t, validFile)
	if _, err := store.Get(context.Background(), "nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("got %v, want ErrProjectNotFound", err)
	}
}

func TestListReturnsACopy(t *testing.T) {
	store, _ := writeStore(t, validFile)
	projects, _ := store.List(context.Background())
	projects[0].Name = "mutated"

	again, _ := store.List(context.Background())
	if again[0].Name == "mutated" {
		t.Error("List handed out a slice aliasing internal state")
	}
}
