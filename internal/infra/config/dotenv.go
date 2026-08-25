package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// DefaultEnvFile is read on startup when present. Override with ENV_FILE.
const DefaultEnvFile = ".env"

// LoadEnvFile applies KEY=VALUE pairs from a file to the process environment.
//
// A missing file is not an error: the file is a convenience for local
// development, and every value in it can equally be exported by hand.
//
// Variables already present in the environment win. The file supplies defaults,
// so a one-off `JIRA_API_TOKEN=... go run ./cmd/server` behaves as expected
// rather than being silently overridden by a stale file.
func LoadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", path, err)
	}
	defer file.Close()

	values, err := parseEnvFile(file)
	if err != nil {
		return fmt.Errorf("in %s: %w", path, err)
	}

	for key, value := range values {
		if _, alreadySet := os.LookupEnv(key); alreadySet {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("setting %s: %w", key, err)
		}
	}
	return nil
}

// parseEnvFile reads the KEY=VALUE format.
//
// Deliberately minimal, and in particular there are no inline comments: an
// unquoted value runs to the end of the line. Treating a mid-line '#' as a
// comment would silently truncate any secret containing one, and a token that
// is quietly wrong is far worse to debug than one that is obviously wrong.
// A '#' at the start of a line is still a comment.
//
// Surrounding single or double quotes are stripped. No escape sequences are
// interpreted, so what is between the quotes is the value.
func parseEnvFile(r io.Reader) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		// "export KEY=VALUE" is accepted so the same file can be sourced by a
		// shell without editing.
		text = strings.TrimPrefix(text, "export ")

		key, value, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", line, text)
		}

		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("line %d: empty variable name", line)
		}

		values[key] = unquote(strings.TrimSpace(value))
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if first == last && (first == '"' || first == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}
