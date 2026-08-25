package github

import (
	"regexp"
	"strings"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// issueKeyPattern matches a tracker issue key: letters and digits, a hyphen,
// then a number. It is deliberately generic rather than built from the
// configured project key, so a branch referencing another project's key is
// recognised and then simply not matched to anything we asked for.
var issueKeyPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*-[0-9]+`)

// keyMatcher resolves a branch name or pull request title to one of the issue
// keys we are interested in.
//
// The convention this relies on is the one a tracker's "create branch" button
// produces: the issue key appears in the branch name. Matching is case
// insensitive because branch names are typed by hand often enough.
type keyMatcher struct {
	wanted map[string]domain.IssueKey
}

func newKeyMatcher(keys []domain.IssueKey) keyMatcher {
	wanted := make(map[string]domain.IssueKey, len(keys))
	for _, key := range keys {
		wanted[strings.ToUpper(string(key))] = key
	}
	return keyMatcher{wanted: wanted}
}

// match returns the first requested issue key referenced by the text.
//
// "First" matters: a branch named PROJ-10-fixes-PROJ-9 belongs to the work item
// it was created from, which is the one named first.
func (m keyMatcher) match(text string) (domain.IssueKey, bool) {
	for _, candidate := range issueKeyPattern.FindAllString(text, -1) {
		if key, ok := m.wanted[strings.ToUpper(candidate)]; ok {
			return key, true
		}
	}
	return "", false
}

// isHumanReviewer reports whether a reviewer's verdict should count toward the
// review states. Automation is excluded so a bot's approval never stands in for
// a person's, which is the whole point of the review states in a retrospective.
func isHumanReviewer(login, accountType string, policy domain.ReviewerPolicy) bool {
	if login == "" {
		return false
	}
	if policy.ExcludeBots {
		if strings.EqualFold(accountType, "Bot") {
			return false
		}
		// GitHub Apps appear as "name[bot]" even where the account type is not
		// reported, for instance inside a timeline event's actor.
		if strings.HasSuffix(strings.ToLower(login), "[bot]") {
			return false
		}
	}
	for _, excluded := range policy.ExcludeLogins {
		if strings.EqualFold(excluded, login) {
			return false
		}
	}
	return true
}
