package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	input := `
# A comment line
JIRA_BASE_URL=https://example.atlassian.net

  JIRA_EMAIL = someone@example.com
export GITHUB_TOKEN=ghp_exported
QUOTED_DOUBLE="spaces preserved"
QUOTED_SINGLE='also preserved'
EMPTY=
TOKEN_WITH_HASH=abc#def#ghi
URL_WITH_FRAGMENT=https://example.com/page#section
EQUALS_IN_VALUE=key=value=more
`

	got, err := parseEnvFile(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}

	want := map[string]string{
		"JIRA_BASE_URL":     "https://example.atlassian.net",
		"JIRA_EMAIL":        "someone@example.com",
		"GITHUB_TOKEN":      "ghp_exported",
		"QUOTED_DOUBLE":     "spaces preserved",
		"QUOTED_SINGLE":     "also preserved",
		"EMPTY":             "",
		"TOKEN_WITH_HASH":   "abc#def#ghi",
		"URL_WITH_FRAGMENT": "https://example.com/page#section",
		"EQUALS_IN_VALUE":   "key=value=more",
	}

	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s = %q, want %q", key, got[key], expected)
		}
	}
}

func TestParseEnvFileRejectsMalformedLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no equals sign", "JIRA_EMAIL someone@example.com", "expected KEY=VALUE"},
		{"empty name", "=orphaned", "empty variable name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEnvFile(strings.NewReader(tc.input))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func writeEnv(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestLoadEnvFileSetsUnsetVariables(t *testing.T) {
	path := writeEnv(t, "DOTENV_TEST_FRESH=from-file\n")
	t.Setenv("DOTENV_TEST_FRESH", "")
	os.Unsetenv("DOTENV_TEST_FRESH")

	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_FRESH"); got != "from-file" {
		t.Errorf("got %q, want from-file", got)
	}
	os.Unsetenv("DOTENV_TEST_FRESH")
}

func TestLoadEnvFileDoesNotOverrideTheEnvironment(t *testing.T) {
	// A value exported for this one run must win, so that overriding a token on
	// the command line is not silently undone by a stale file.
	path := writeEnv(t, "DOTENV_TEST_EXISTING=from-file\n")
	t.Setenv("DOTENV_TEST_EXISTING", "from-environment")

	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_EXISTING"); got != "from-environment" {
		t.Errorf("the file overrode the environment: got %q", got)
	}
}

func TestLoadEnvFileIgnoresAMissingFile(t *testing.T) {
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("a missing file should not be an error, got %v", err)
	}
}

func TestLoadEnvFileReportsAMalformedFile(t *testing.T) {
	path := writeEnv(t, "this is not a pair\n")
	err := LoadEnvFile(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error should name the file, got %q", err)
	}
}

func TestLoadReadsTheEnvFileNamedByEnvFile(t *testing.T) {
	path := writeEnv(t, strings.Join([]string{
		"JIRA_BASE_URL=https://from-file.atlassian.net/",
		"JIRA_EMAIL=file@example.com",
		"JIRA_API_TOKEN=file-token",
		"GITHUB_TOKEN=file-gh",
	}, "\n"))

	t.Setenv("ENV_FILE", path)
	for _, key := range []string{"JIRA_BASE_URL", "JIRA_EMAIL", "JIRA_API_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	config, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if config.JiraEmail != "file@example.com" || config.GitHubToken != "file-gh" {
		t.Errorf("config not populated from the file: %+v", config)
	}
	// The trailing slash is still trimmed, so file-sourced values go through the
	// same normalisation as exported ones.
	if config.JiraBaseURL != "https://from-file.atlassian.net" {
		t.Errorf("base URL = %q", config.JiraBaseURL)
	}
}
