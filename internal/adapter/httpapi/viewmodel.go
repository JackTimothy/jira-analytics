package httpapi

import (
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
	Timezone string `json:"timezone"`
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
		ID:       string(p.ID),
		Name:     p.Name,
		Settings: settingsView{Timezone: timezone},
		Tracker:  trackerView{ProjectKey: p.Tracker.ProjectKey, BoardID: p.Tracker.BoardID},
		Repos:    repos,
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
	Sprint   sprintView   `json:"sprint"`
	Parents  []parentView `json:"parents"`
	Warnings []string     `json:"warnings"`
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
	}
}
