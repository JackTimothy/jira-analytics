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

// Validate reports whether the schedule can bound an axis at all.
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
}

func (s AxisSegment) Duration() time.Duration { return s.To.Sub(s.From) }

// AxisSegments splits a window into alternating working and off-hours spans,
// in the given timezone. Adjacent spans of the same kind are merged, so a
// weekend arrives as one off-hours segment rather than several.
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

	local := w.Start.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	for day.Before(w.End) {
		nextDay := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc)

		if hours.isWorkingDay(day.Weekday()) {
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
		} else {
			appendSpan(day, nextDay, SegmentOffHours)
		}

		day = nextDay
	}

	return segments
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
