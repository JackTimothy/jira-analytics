package github

import (
	"testing"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

func TestKeyMatcher(t *testing.T) {
	matcher := newKeyMatcher([]domain.IssueKey{"PROJ-10", "PROJ-9", "OTHER-1"})

	tests := []struct {
		name  string
		text  string
		want  domain.IssueKey
		found bool
	}{
		{"key at the start, as the tracker generates", "PROJ-10-add-a-thing", "PROJ-10", true},
		{"key with a path prefix", "feature/PROJ-10-add-a-thing", "PROJ-10", true},
		{"lowercase branch name", "proj-10-add-a-thing", "PROJ-10", true},
		{"pull request title form", "PROJ-9: fix the thing", "PROJ-9", true},
		{"two keys takes the first", "PROJ-10-fixes-PROJ-9", "PROJ-10", true},
		{"a key we did not ask about", "UNRELATED-5-something", "", false},
		{"no key at all", "dependabot/npm_and_yarn/react-19", "", false},
		{"a longer number is a different issue", "PROJ-100-other", "", false},
		{"digits alone are not a key", "release-2026", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, found := matcher.match(tc.text)
			if found != tc.found || got != tc.want {
				t.Errorf("match(%q) = (%q, %v), want (%q, %v)", tc.text, got, found, tc.want, tc.found)
			}
		})
	}
}

func TestIsHumanReviewer(t *testing.T) {
	strict := domain.ReviewerPolicy{ExcludeBots: true, ExcludeLogins: []string{"some-ai-reviewer"}}

	tests := []struct {
		name        string
		login       string
		accountType string
		policy      domain.ReviewerPolicy
		want        bool
	}{
		{"a person", "alice", "User", strict, true},
		{"an app by account type", "coverage-app", "Bot", strict, false},
		{"an app by login suffix", "copilot-pull-request-reviewer[bot]", "", strict, false},
		{"an app by login suffix, mixed case", "Renovate[Bot]", "", strict, false},
		{"a configured AI reviewer that looks like a user", "some-ai-reviewer", "User", strict, false},
		{"the configured login, differently cased", "Some-AI-Reviewer", "User", strict, false},
		{"a bot when bot exclusion is off", "coverage-app", "Bot", domain.ReviewerPolicy{}, true},
		{"an empty login is never a reviewer", "", "User", strict, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHumanReviewer(tc.login, tc.accountType, tc.policy); got != tc.want {
				t.Errorf("isHumanReviewer(%q, %q) = %v, want %v", tc.login, tc.accountType, got, tc.want)
			}
		})
	}
}
