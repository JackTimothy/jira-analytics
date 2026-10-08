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
	Holidays     []holidayView    `json:"holidays"`
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

// holidayView is one day off; the date is ISO-8601 with no time of day.
type holidayView struct {
	Date string `json:"date"`
	Name string `json:"name,omitempty"`
}

// presentHolidays never returns nil, so a project with none says so with an
// empty list rather than a null the client has to guard against.
func presentHolidays(holidays []domain.Holiday) []holidayView {
	out := make([]holidayView, 0, len(holidays))
	for _, holiday := range holidays {
		out = append(out, holidayView{Date: holiday.Date.String(), Name: holiday.Name})
	}
	return out
}

func holidaysFromView(views []holidayView) ([]domain.Holiday, error) {
	if len(views) == 0 {
		return nil, nil
	}
	holidays := make([]domain.Holiday, 0, len(views))
	for _, v := range views {
		date, err := domain.ParseCalendarDate(strings.TrimSpace(v.Date))
		if err != nil {
			return nil, fmt.Errorf("%w: holiday %q is not a YYYY-MM-DD date", domain.ErrInvalidSettings, v.Date)
		}
		holidays = append(holidays, domain.Holiday{Date: date, Name: strings.TrimSpace(v.Name)})
	}
	return holidays, nil
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
			Holidays:     presentHolidays(p.Settings.Holidays),
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
	Sprint    sprintView          `json:"sprint"`
	Parents   []parentView        `json:"parents"`
	Warnings  []string            `json:"warnings"`
	Axis      []axisSegmentView   `json:"axis"`
	Burndown  burndownView        `json:"burndown"`
	CycleTime sprintCycleTimeView `json:"cycleTime"`
}

type burndownView struct {
	Total       float64             `json:"total"`
	Remaining   []burndownPointView `json:"remaining"`
	Ideal       []burndownPointView `json:"ideal"`
	Unestimated []string            `json:"unestimated"`
}

type burndownPointView struct {
	At        time.Time `json:"at"`
	Remaining float64   `json:"remaining"`
}

func presentBurndown(b domain.Burndown) burndownView {
	view := burndownView{
		Total:       float64(b.Total),
		Remaining:   presentBurndownPoints(b.Remaining),
		Ideal:       presentBurndownPoints(b.Ideal),
		Unestimated: presentKeys(b.Unestimated),
	}
	return view
}

func presentBurndownPoints(points []domain.BurndownPoint) []burndownPointView {
	out := make([]burndownPointView, 0, len(points))
	for _, point := range points {
		out = append(out, burndownPointView{At: point.At, Remaining: float64(point.Remaining)})
	}
	return out
}

// sprintCycleTimeView is the sprint-level cycle time. Hours are sent unrounded;
// how many places to show is the reader's concern, not the contract's.
type sprintCycleTimeView struct {
	// HoursPerPoint is null when nothing finished during the sprint. Zero
	// would claim the team delivered instantly.
	HoursPerPoint *float64            `json:"hoursPerPoint"`
	Items         []cycleTimeItemView `json:"items"`
	Unestimated   []string            `json:"unestimated"`
	Unstarted     []string            `json:"unstarted"`
}

// cycleTimeItemView is one of the items the sprint's average was taken over.
type cycleTimeItemView struct {
	Key           string    `json:"key"`
	Summary       string    `json:"summary"`
	Type          string    `json:"type"`
	Points        float64   `json:"points"`
	Hours         float64   `json:"hours"`
	HoursPerPoint float64   `json:"hoursPerPoint"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished"`
}

// cycleSpanView is one work item's own cycle time, on its timeline heading.
type cycleSpanView struct {
	Hours    float64   `json:"hours"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
}

func presentCycleTime(c domain.SprintCycleTime) sprintCycleTimeView {
	view := sprintCycleTimeView{
		Items:       make([]cycleTimeItemView, 0, len(c.Counted)),
		Unestimated: presentKeys(c.Unestimated),
		Unstarted:   presentKeys(c.Unstarted),
	}
	if len(c.Counted) > 0 {
		perPoint := c.PerPoint.Hours()
		view.HoursPerPoint = &perPoint
	}
	for _, cycle := range c.Counted {
		view.Items = append(view.Items, cycleTimeItemView{
			Key:           string(cycle.Item.Key),
			Summary:       cycle.Item.Summary,
			Type:          cycle.Item.Type,
			Points:        float64(cycle.Item.Points),
			Hours:         cycle.Working.Hours(),
			HoursPerPoint: cycle.PerPoint().Hours(),
			Started:       cycle.Started,
			Finished:      cycle.Finished,
		})
	}
	return view
}

func presentKeys(keys []domain.IssueKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, string(key))
	}
	return out
}

type axisSegmentView struct {
	From     time.Time     `json:"from"`
	To       time.Time     `json:"to"`
	Kind     string        `json:"kind"`
	Holidays []holidayView `json:"holidays,omitempty"`
}

func presentAxis(segments []domain.AxisSegment) []axisSegmentView {
	out := make([]axisSegmentView, 0, len(segments))
	for _, segment := range segments {
		view := axisSegmentView{From: segment.From, To: segment.To, Kind: segment.Kind.String()}
		if len(segment.Holidays) > 0 {
			view.Holidays = presentHolidays(segment.Holidays)
		}
		out = append(out, view)
	}
	return out
}

type parentView struct {
	Key     string    `json:"key"`
	Summary string    `json:"summary"`
	Type    string    `json:"type"`
	DueDate *string   `json:"dueDate"`
	InScope bool      `json:"inScope"`
	Rows    []rowView `json:"rows"`

	// CycleTime is null unless the item is a Story, Task or Bug that is Done
	// and whose cycle could be measured.
	CycleTime *cycleSpanView `json:"cycleTime"`
}

// rowView is one charted line. Kind says what it stands for: a sub-task, the
// work item itself where nobody broke it down, or one branch of a work item
// that has several.
type rowView struct {
	Kind      string         `json:"kind"`
	Key       string         `json:"key"`
	Label     string         `json:"label"`
	Intervals []intervalView `json:"intervals"`
}

type intervalView struct {
	State string    `json:"state"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

func presentRetrospective(r domain.Retrospective) retrospectiveView {
	cycles := make(map[domain.IssueKey]*cycleSpanView, len(r.CycleTime.Items))
	for _, cycle := range r.CycleTime.Items {
		cycles[cycle.Item.Key] = &cycleSpanView{
			Hours:    cycle.Working.Hours(),
			Started:  cycle.Started,
			Finished: cycle.Finished,
		}
	}

	parents := make([]parentView, 0, len(r.Groups))
	for _, group := range r.Groups {
		var due *string
		if group.Parent.DueDate != nil {
			formatted := group.Parent.DueDate.String()
			due = &formatted
		}

		rows := make([]rowView, 0, len(group.Rows))
		for _, row := range group.Rows {
			intervals := make([]intervalView, 0, len(row.Intervals))
			for _, interval := range row.Intervals {
				intervals = append(intervals, intervalView{
					State: interval.State.String(),
					From:  interval.From,
					To:    interval.To,
				})
			}
			rows = append(rows, rowView{
				Kind:      row.Kind.String(),
				Key:       string(row.Key),
				Label:     row.Label,
				Intervals: intervals,
			})
		}

		parents = append(parents, parentView{
			Key:       string(group.Parent.Key),
			Summary:   group.Parent.Summary,
			Type:      group.Parent.Type,
			DueDate:   due,
			InScope:   group.InScope,
			Rows:      rows,
			CycleTime: cycles[group.Parent.Key],
		})
	}

	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}

	return retrospectiveView{
		Sprint:    presentSprint(r.Sprint),
		Parents:   parents,
		Warnings:  warnings,
		Axis:      presentAxis(r.Axis),
		Burndown:  presentBurndown(r.Burndown),
		CycleTime: presentCycleTime(r.CycleTime),
	}
}
