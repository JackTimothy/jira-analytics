package domain

import (
	"fmt"
	"time"
)

// CalendarDate is a day on a calendar, with no time of day and no timezone.
//
// It exists because a due date and an instant are different kinds of thing.
// Trackers record a due date as "2026-08-26" — a day, not a moment — while a
// sprint ends at an instant. Modelling the due date as a time.Time forces an
// arbitrary time of day onto it, and any later timezone conversion can then
// silently shift it to the neighbouring day. Keeping the two types distinct
// makes that mistake unrepresentable.
type CalendarDate struct {
	Year  int
	Month time.Month
	Day   int
}

func NewCalendarDate(year int, month time.Month, day int) CalendarDate {
	return CalendarDate{Year: year, Month: month, Day: day}
}

const calendarDateLayout = "2006-01-02"

// ParseCalendarDate reads the ISO-8601 date form trackers use.
func ParseCalendarDate(s string) (CalendarDate, error) {
	parsed, err := time.Parse(calendarDateLayout, s)
	if err != nil {
		return CalendarDate{}, fmt.Errorf("parsing calendar date %q: %w", s, err)
	}
	return NewCalendarDate(parsed.Year(), parsed.Month(), parsed.Day()), nil
}

// CalendarDateIn is the calendar day an instant falls on, as observed from a
// given timezone. This is the only correct way to compare an instant with a
// date, and the timezone is required rather than assumed.
func CalendarDateIn(t time.Time, loc *time.Location) CalendarDate {
	local := t.In(loc)
	return NewCalendarDate(local.Year(), local.Month(), local.Day())
}

// After reports whether d falls strictly later on the calendar than other.
func (d CalendarDate) After(other CalendarDate) bool {
	if d.Year != other.Year {
		return d.Year > other.Year
	}
	if d.Month != other.Month {
		return d.Month > other.Month
	}
	return d.Day > other.Day
}

func (d CalendarDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

func (d CalendarDate) MarshalText() ([]byte, error) { return []byte(d.String()), nil }
