package domain

import (
	"fmt"
	"time"
)

// DefaultTimezone is used when a project does not specify one. Scope
// classification depends on it, so there is always a definite answer rather
// than a silent fallback to UTC.
const DefaultTimezone = "America/New_York"

// ProjectSettings holds the parts of a project a user may edit at runtime.
type ProjectSettings struct {
	Timezone string
}

// Location resolves the configured timezone, defaulting when unset. An
// unrecognised zone is an error rather than a fallback: the wrong zone silently
// shifts which work counts as committed, so it must fail loudly.
func (s ProjectSettings) Location() (*time.Location, error) {
	name := s.Timezone
	if name == "" {
		name = DefaultTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", name, err)
	}
	return loc, nil
}

// TrackerRef points at the issue tracker a project sources from.
type TrackerRef struct {
	ProjectKey string
	BoardID    string
}

// RepoRef points at one code repository a project sources from.
type RepoRef struct {
	Owner string
	Name  string
}

func (r RepoRef) String() string { return r.Owner + "/" + r.Name }

// ReviewerPolicy decides which reviewers count toward the review states. AI and
// automation reviewers are excluded so that a bot's approval never stands in
// for a human's.
type ReviewerPolicy struct {
	ExcludeBots   bool
	ExcludeLogins []string
}

// Project ties one tracker project to one or more repositories.
type Project struct {
	ID        ProjectID
	Name      string
	Settings  ProjectSettings
	Tracker   TrackerRef
	Repos     []RepoRef
	Reviewers ReviewerPolicy
}
