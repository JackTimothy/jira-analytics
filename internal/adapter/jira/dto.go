package jira

import (
	"strings"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// The types below mirror Jira's JSON. They stay in this package so that Jira's
// field names, custom-field ids, and pagination quirks never leak inward.

type sprintPage struct {
	IsLast bool         `json:"isLast"`
	Values []sprintJSON `json:"values"`
}

type sprintJSON struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

type issuePage struct {
	StartAt       int         `json:"startAt"`
	MaxResults    int         `json:"maxResults"`
	Total         int         `json:"total"`
	IsLast        *bool       `json:"isLast"`
	NextPageToken string      `json:"nextPageToken"`
	Issues        []issueJSON `json:"issues"`
}

type issueJSON struct {
	Key    string     `json:"key"`
	Fields fieldsJSON `json:"fields"`
}

type fieldsJSON struct {
	Summary   string      `json:"summary"`
	DueDate   string      `json:"duedate"`
	Created   string      `json:"created"`
	Status    statusJSON  `json:"status"`
	IssueType typeJSON    `json:"issuetype"`
	Parent    *parentJSON `json:"parent"`
}

type typeJSON struct {
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

type parentJSON struct {
	Key string `json:"key"`
}

type statusJSON struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Category statusCategoryJSON `json:"statusCategory"`
}

type statusCategoryJSON struct {
	Key string `json:"key"`
}

type changelogPage struct {
	IsLast bool            `json:"isLast"`
	Values []changelogJSON `json:"values"`
}

type changelogJSON struct {
	Created string              `json:"created"`
	Items   []changelogItemJSON `json:"items"`
}

type changelogItemJSON struct {
	Field      string `json:"field"`
	From       string `json:"from"`
	FromString string `json:"fromString"`
	To         string `json:"to"`
	ToString   string `json:"toString"`
}

// Jira's status category keys. Mapping by category rather than by status name
// is what lets this adapter work against any workflow: a team can rename or add
// statuses freely and only the three buckets matter.
const (
	categoryKeyToDo       = "new"
	categoryKeyInProgress = "indeterminate"
	categoryKeyDone       = "done"
)

func toCategory(key string) domain.StatusCategory {
	switch strings.ToLower(key) {
	case categoryKeyDone:
		return domain.CategoryDone
	case categoryKeyInProgress:
		return domain.CategoryInProgress
	default:
		return domain.CategoryToDo
	}
}

func (s statusJSON) toDomain() domain.IssueStatus {
	return domain.IssueStatus{Name: s.Name, Category: toCategory(s.Category.Key)}
}

// jiraTimeLayout is Jira's timestamp form: ISO 8601 with a numeric offset and
// no colon, which time.RFC3339 does not accept.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(jiraTimeLayout, value); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}
