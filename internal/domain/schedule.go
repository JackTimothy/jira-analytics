package domain

import (
	"fmt"
	"time"
)

// WorkingHours is when a team is normally at work, expressed in the project's
// timezone. Start and End are minutes past local midnight so the schedule
// stays anchored to the clock on the wall: an 08:00 start is 08:00 on both
// sides of a daylight-saving transition, even though the real-time length of
// the working day changes twice a year.
type WorkingHours struct {
	Days  []time.Weekday
	Start int // minutes past local midnight, inclusive
	End   int // minutes past local midnight, exclusive

	// Holidays are days with no working hours at all, whatever weekday they
	// fall on. They are configured beside the weekly pattern rather than as
	// part of it, so ProjectSettings.Schedule fills them in.
	Holidays []Holiday
}

// Holiday is a calendar day the team does not work, in the project's timezone.
// It is a day rather than an instant for the same reason a due date is: "the
// 26th" is a fact about a calendar, and only becomes a span of time once a
// timezone says where that day begins and ends.
type Holiday struct {
	Date CalendarDate
	Name string // optional; what the chart calls the day off
}

// DefaultWorkingHours is Monday to Friday, 08:00 to 18:00 — used when a
// project does not configure its own.
func DefaultWorkingHours() WorkingHours {
	return WorkingHours{
		Days: []time.Weekday{
			time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday,
		},
		Start: 8 * 60,
		End:   18 * 60,
	}
}

const minutesPerDay = 24 * 60

// Validate reports whether the schedule can bound an axis at all. Holidays
// cannot make it unusable, so they are not its concern; ProjectSettings checks
// them where they are written.
func (h WorkingHours) Validate() error {
	if len(h.Days) == 0 {
		return fmt.Errorf("%w: working hours need at least one working day", ErrInvalidSettings)
	}
	seen := map[time.Weekday]bool{}
	for _, day := range h.Days {
		if day < time.Sunday || day > time.Saturday {
			return fmt.Errorf("%w: %d is not a weekday", ErrInvalidSettings, day)
		}
		if seen[day] {
			return fmt.Errorf("%w: %s appears twice in the working days", ErrInvalidSettings, day)
		}
		seen[day] = true
	}
	if h.Start < 0 || h.Start >= minutesPerDay {
		return fmt.Errorf("%w: start %d is outside the day", ErrInvalidSettings, h.Start)
	}
	if h.End <= 0 || h.End > minutesPerDay {
		return fmt.Errorf("%w: end %d is outside the day", ErrInvalidSettings, h.End)
	}
	if h.Start >= h.End {
		return fmt.Errorf("%w: working hours must start before they end", ErrInvalidSettings)
	}
	return nil
}

func (h WorkingHours) isWorkingDay(day time.Weekday) bool {
	for _, d := range h.Days {
		if d == day {
			return true
		}
	}
	return false
}

// AxisSegmentKind distinguishes the spans of a compressed time axis.
type AxisSegmentKind uint8

const (
	SegmentWorking AxisSegmentKind = iota
	SegmentOffHours
)

func (k AxisSegmentKind) String() string {
	if k == SegmentWorking {
		return "WORKING"
	}
	return "OFF_HOURS"
}

func (k AxisSegmentKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// AxisSegment is one contiguous span of a window that is either inside or
// outside working hours.
type AxisSegment struct {
	From time.Time
	To   time.Time
	Kind AxisSegmentKind

	// Holidays are the holidays that took working time out of this span, so a
	// chart can say why a weekday went missing. A holiday that fell on a day
	// off anyway changed nothing and is not listed.
	Holidays []Holiday
}

func (s AxisSegment) Duration() time.Duration { return s.To.Sub(s.From) }

// AxisSegments splits a window into alternating working and off-hours spans,
// in the given timezone. Adjacent spans of the same kind are merged, so a
// weekend arrives as one off-hours segment rather than several, and a holiday
// Monday arrives as part of that weekend.
//
// Days are walked by constructing each local midnight with time.Date rather
// than by adding 24 hours to an instant — the difference is exactly the two
// daylight-saving days, which are 23 and 25 real hours long. The working span
// still starts and ends at the configured local clock times on those days.
func AxisSegments(w Window, hours WorkingHours, loc *time.Location) []AxisSegment {
	if !w.End.After(w.Start) {
		return nil
	}
	if err := hours.Validate(); err != nil {
		// An unusable schedule degrades to a single working span: the chart
		// stays linear rather than failing to render at all. Validation at the
		// settings boundary is what prevents this from being reachable.
		return []AxisSegment{{From: w.Start, To: w.End, Kind: SegmentWorking}}
	}

	var segments []AxisSegment
	appendSpan := func(from, to time.Time, kind AxisSegmentKind) {
		from, to = clampSpan(from, to, w)
		if !to.After(from) {
			return
		}
		if n := len(segments); n > 0 && segments[n-1].Kind == kind {
			segments[n-1].To = to
			return
		}
		segments = append(segments, AxisSegment{From: from, To: to, Kind: kind})
	}

	holidays := make(map[CalendarDate]Holiday, len(hours.Holidays))
	for _, holiday := range hours.Holidays {
		holidays[holiday.Date] = holiday
	}

	local := w.Start.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	for day.Before(w.End) {
		nextDay := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc)
		holiday, isHoliday := holidays[NewCalendarDate(day.Year(), day.Month(), day.Day())]

		switch {
		case hours.isWorkingDay(day.Weekday()) && !isHoliday:
			// Built from clock components, not by adding a duration to
			// midnight: adding real time lands an hour late on the
			// spring-forward day and an hour early on the fall-back one.
			workStart := time.Date(day.Year(), day.Month(), day.Day(),
				hours.Start/60, hours.Start%60, 0, 0, loc)
			workEnd := time.Date(day.Year(), day.Month(), day.Day(),
				hours.End/60, hours.End%60, 0, 0, loc)
			// On a short DST day the configured end can land past the next
			// midnight; the day boundary wins so segments never overlap.
			if workEnd.After(nextDay) {
				workEnd = nextDay
			}

			appendSpan(day, workStart, SegmentOffHours)
			appendSpan(workStart, workEnd, SegmentWorking)
			appendSpan(workEnd, nextDay, SegmentOffHours)
		case hours.isWorkingDay(day.Weekday()):
			// A holiday on a working day. The day always overlaps the window —
			// the walk starts on the window's first day and stops before its
			// end — so the segment it just joined is the last one.
			appendSpan(day, nextDay, SegmentOffHours)
			last := &segments[len(segments)-1]
			last.Holidays = append(last.Holidays, holiday)
		default:
			appendSpan(day, nextDay, SegmentOffHours)
		}

		day = nextDay
	}

	return segments
}

// WorkingDuration is how much of the time between two instants fell inside
// working hours.
//
// It is measured over the same segments the axis is drawn from, so a duration
// read off this agrees with the width it occupies on the compressed chart. An
// empty or inverted range is no time at all.
func WorkingDuration(from, to time.Time, hours WorkingHours, loc *time.Location) time.Duration {
	var working time.Duration
	for _, segment := range AxisSegments(Window{Start: from, End: to}, hours, loc) {
		if segment.Kind == SegmentWorking {
			working += segment.Duration()
		}
	}
	return working
}

// clampSpan intersects [from, to) with the window.
func clampSpan(from, to time.Time, w Window) (time.Time, time.Time) {
	if from.Before(w.Start) {
		from = w.Start
	}
	if to.After(w.End) {
		to = w.End
	}
	return from, to
}
