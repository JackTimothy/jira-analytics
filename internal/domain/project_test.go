package domain

import (
	"errors"
	"testing"
)

// A project on the default schedule has no WorkingHours to hang holidays on,
// and must still have them.
func TestScheduleCarriesHolidaysOnTheDefaultSchedule(t *testing.T) {
	thanksgiving := Holiday{Date: NewCalendarDate(2026, 11, 26), Name: "Thanksgiving"}
	settings := ProjectSettings{Holidays: []Holiday{thanksgiving}}

	got := settings.Schedule()
	if got.Start != DefaultWorkingHours().Start || len(got.Days) != 5 {
		t.Errorf("schedule = %+v, want the default", got)
	}
	if len(got.Holidays) != 1 || got.Holidays[0] != thanksgiving {
		t.Errorf("holidays = %v, want just %v", got.Holidays, thanksgiving)
	}
}

func TestValidateRejectsAHolidayListedTwice(t *testing.T) {
	settings := ProjectSettings{Holidays: []Holiday{
		{Date: NewCalendarDate(2026, 12, 25), Name: "Christmas"},
		{Date: NewCalendarDate(2026, 12, 25)},
	}}
	if err := settings.Validate(); !errors.Is(err, ErrInvalidSettings) {
		t.Errorf("Validate = %v, want ErrInvalidSettings", err)
	}
}
