// Package config reads deployment settings from the environment.
//
// Everything here is deployment-specific by definition — a company's Atlassian
// site, its credentials, where its project file lives — which is exactly why
// none of it may be hard-coded. The repository is meant to be publishable.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Port          string
	ProjectsFile  string
	WebDir        string
	JiraBaseURL   string
	JiraEmail     string
	JiraAPIToken  string
	GitHubBaseURL string
	GitHubToken   string
}

const (
	defaultPort         = "8080"
	defaultProjectsFile = "projects.yaml"
	defaultWebDir       = "web/dist"
)

// Load reads the environment and reports every problem at once, so an operator
// setting the app up for the first time sees the whole list rather than
// discovering one missing variable per restart.
func Load() (Config, error) {
	// A local .env supplies defaults for anything not already exported. Done
	// here so every entry point picks it up rather than only the server.
	if err := LoadEnvFile(envOr("ENV_FILE", DefaultEnvFile)); err != nil {
		return Config{}, err
	}

	config := Config{
		Port:          envOr("PORT", defaultPort),
		ProjectsFile:  envOr("PROJECTS_FILE", defaultProjectsFile),
		WebDir:        envOr("WEB_DIR", defaultWebDir),
		JiraBaseURL:   strings.TrimSuffix(os.Getenv("JIRA_BASE_URL"), "/"),
		JiraEmail:     os.Getenv("JIRA_EMAIL"),
		JiraAPIToken:  os.Getenv("JIRA_API_TOKEN"),
		GitHubBaseURL: os.Getenv("GITHUB_BASE_URL"),
		GitHubToken:   os.Getenv("GITHUB_TOKEN"),
	}

	var problems []error
	if config.JiraBaseURL == "" {
		problems = append(problems, errors.New("JIRA_BASE_URL is required (for example https://your-site.atlassian.net)"))
	}
	if config.JiraEmail == "" {
		problems = append(problems, errors.New("JIRA_EMAIL is required: the account your API token belongs to"))
	}
	if config.JiraAPIToken == "" {
		problems = append(problems, errors.New("JIRA_API_TOKEN is required: create one at https://id.atlassian.com/manage-profile/security/api-tokens"))
	}
	if config.GitHubToken == "" {
		problems = append(problems, errors.New("GITHUB_TOKEN is required: a token with read access to the configured repositories"))
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("configuration is incomplete:\n  - %s", joinErrors(problems, "\n  - "))
	}
	return config, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func joinErrors(errs []error, sep string) string {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		messages = append(messages, err.Error())
	}
	return strings.Join(messages, sep)
}
