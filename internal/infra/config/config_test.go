package config

import (
	"strings"
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("JIRA_BASE_URL", "https://example.atlassian.net/")
	t.Setenv("JIRA_EMAIL", "someone@example.com")
	t.Setenv("JIRA_API_TOKEN", "token")
	t.Setenv("GITHUB_TOKEN", "ghp_x")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequired(t)

	config, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.Port != defaultPort || config.ProjectsFile != defaultProjectsFile {
		t.Errorf("defaults not applied: %+v", config)
	}
	// A trailing slash on the base URL would produce doubled slashes in every
	// request path.
	if config.JiraBaseURL != "https://example.atlassian.net" {
		t.Errorf("base URL = %q", config.JiraBaseURL)
	}
}

func TestLoadReportsEveryMissingVariableAtOnce(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"JIRA_BASE_URL", "JIRA_EMAIL", "JIRA_API_TOKEN", "GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}

func TestLoadHonoursOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "9999")
	t.Setenv("PROJECTS_FILE", "/etc/app/projects.yaml")

	config, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.Port != "9999" || config.ProjectsFile != "/etc/app/projects.yaml" {
		t.Errorf("overrides ignored: %+v", config)
	}
}
