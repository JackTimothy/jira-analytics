package httpapi

import (
	"fmt"
	"strings"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/domain"
)

// The types below are the wire contract. They are separate from the domain
// types so that renaming a field for the GUI never means editing an entity, and
// so that nothing internal leaks into a public response by accident.

type projectView struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Settings settingsView `json:"settings"`
	Tracker  trackerView  `json:"tracker"`
	Repos    []string     `json:"repos"`
}

type settingsView struct {
	Timezone     string           `json:"timezone"`
	WorkingHours workingHoursView `json:"workingHours"`
}

// workingHoursView speaks day names and HH:MM clock times; the
// minutes-past-midnight representation stays internal.
type workingHoursView struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

func presentWorkingHours(hours domain.WorkingHours) workingHoursView {
	days := make([]string, 0, len(hours.Days))
	for _, day := range hours.Days {
		days = append(days, strings.ToLower(day.String()))
	}
	return workingHoursView{
		Days:  days,
		Start: fmt.Sprintf("%02d:%02d", hours.Start/60, hours.Start%60),
		End:   fmt.Sprintf("%02d:%02d", hours.End/60, hours.End%60),
	}
}

var weekdayByName = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
}

func (v workingHoursView) toDomain() (*domain.WorkingHours, error) {
	hours := domain.WorkingHours{}
	for _, name := range v.Days {
		day, ok := weekdayByName[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, fmt.Errorf("%w: unknown working day %q", domain.ErrInvalidSettings, name)
		}
		hours.Days = append(hours.Days, day)
	}
	var err error
	if hours.Start, err = parseClockView(v.Start); err != nil {
		return nil, err
	}
	if hours.End, err = parseClockView(v.End); err != nil {
		return nil, err
	}
	return &hours, nil
}

func parseClockView(value string) (int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not an HH:MM clock time", domain.ErrInvalidSettings, value)
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

type trackerView struct {
	ProjectKey string `json:"projectKey"`
	BoardID    string `json:"boardId"`
}

func presentProject(p domain.Project) projectView {
	repos := make([]string, 0, len(p.Repos))
	for _, repo := range p.Repos {
		repos = append(repos, repo.String())
	}
	timezone := p.Settings.Timezone
	if timezone == "" {
		timezone = domain.DefaultTimezone
	}
	return projectView{
		ID:   string(p.ID),
		Name: p.Name,
		Settings: settingsView{
			Timezone:     timezone,
			WorkingHours: presentWorkingHours(p.Settings.Schedule()),
		},
		Tracker: trackerView{ProjectKey: p.Tracker.ProjectKey, BoardID: p.Tracker.BoardID},
		Repos:   repos,
	}
}

func presentProjects(projects []domain.Project) []projectView {
	out := make([]projectView, 0, len(projects))
	for _, project := range projects {
		out = append(out, presentProject(project))
	}
	return out
}

type sprintView struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func presentSprint(s domain.Sprint) sprintView {
	return sprintView{ID: string(s.ID), Name: s.Name, Start: s.Start, End: s.End}
}

func presentSprints(sprints []domain.Sprint) []sprintView {
	out := make([]sprintView, 0, len(sprints))
	for _, sprint := range sprints {
		out = append(out, presentSprint(sprint))
	}
	return out
}

type retrospectiveView struct {
	Sprint   sprintView        `json:"sprint"`
	Parents  []parentView      `json:"parents"`
	Warnings []string          `json:"warnings"`
	Axis     []axisSegmentView `json:"axis"`
}

type axisSegmentView struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Kind string    `json:"kind"`
}

func presentAxis(segments []domain.AxisSegment) []axisSegmentView {
	out := make([]axisSegmentView, 0, len(segments))
	for _, segment := range segments {
		out = append(out, axisSegmentView{From: segment.From, To: segment.To, Kind: segment.Kind.String()})
	}
	return out
}

type parentView struct {
	Key      string        `json:"key"`
	Summary  string        `json:"summary"`
	DueDate  *string       `json:"dueDate"`
	InScope  bool          `json:"inScope"`
	SubTasks []subTaskView `json:"subtasks"`
}

type subTaskView struct {
	Key       string         `json:"key"`
	Summary   string         `json:"summary"`
	Intervals []intervalView `json:"intervals"`
}

type intervalView struct {
	State string    `json:"state"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

func presentRetrospective(r domain.Retrospective) retrospectiveView {
	parents := make([]parentView, 0, len(r.Groups))
	for _, group := range r.Groups {
		var due *string
		if group.Parent.DueDate != nil {
			formatted := group.Parent.DueDate.String()
			due = &formatted
		}

		subTasks := make([]subTaskView, 0, len(group.SubTasks))
		for _, timeline := range group.SubTasks {
			intervals := make([]intervalView, 0, len(timeline.Intervals))
			for _, interval := range timeline.Intervals {
				intervals = append(intervals, intervalView{
					State: interval.State.String(),
					From:  interval.From,
					To:    interval.To,
				})
			}
			subTasks = append(subTasks, subTaskView{
				Key:       string(timeline.SubTask.Key),
				Summary:   timeline.SubTask.Summary,
				Intervals: intervals,
			})
		}

		parents = append(parents, parentView{
			Key:      string(group.Parent.Key),
			Summary:  group.Parent.Summary,
			DueDate:  due,
			InScope:  group.InScope,
			SubTasks: subTasks,
		})
	}

	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}

	return retrospectiveView{
		Sprint:   presentSprint(r.Sprint),
		Parents:  parents,
		Warnings: warnings,
		Axis:     presentAxis(r.Axis),
	}
}
