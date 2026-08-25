package domain

import (
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("loading %s: %v", name, err)
	}
	return loc
}

func date(y int, m time.Month, d int) *CalendarDate {
	v := NewCalendarDate(y, m, d)
	return &v
}

func TestInScope(t *testing.T) {
	eastern := mustLoad(t, "America/New_York")

	// A sprint ending 26 Aug 2026 at 18:00 UTC, i.e. 2pm Eastern.
	sprint := Sprint{
		ID:    "7355",
		Name:  "Sprint 26-33",
		Start: time.Date(2026, 8, 12, 15, 24, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name   string
		due    *CalendarDate
		expect bool
	}{
		{"committed for this sprint: due date equals sprint end", date(2026, time.August, 26), true},
		{"carried over: due date preserved from an earlier sprint", date(2026, time.August, 12), true},
		{"carried several sprints: due date well in the past", date(2026, time.July, 15), true},
		{"pulled forward for an emergency: due date before sprint end", date(2026, time.August, 20), true},
		{"pulled in opportunistically: no due date at all", nil, false},
		{"belongs to a later sprint: due date beyond sprint end", date(2026, time.September, 9), false},
		{"one day past the end is out of scope", date(2026, time.August, 27), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := InScope(tc.due, sprint, eastern); got != tc.expect {
				t.Errorf("InScope() = %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestInScopeComparesCalendarDaysInProjectTimezone(t *testing.T) {
	// A sprint ending just after midnight UTC is still the previous calendar
	// day in Eastern. A due date of that Eastern day must count as in scope,
	// which a naive instant comparison against the raw timestamp would get
	// right by accident but a UTC-day comparison would get wrong.
	sprint := Sprint{
		End: time.Date(2026, 8, 27, 0, 30, 0, 0, time.UTC), // 26 Aug 20:30 Eastern
	}

	eastern := mustLoad(t, "America/New_York")
	if !InScope(date(2026, time.August, 26), sprint, eastern) {
		t.Error("due 26 Aug should be in scope for a sprint ending 26 Aug Eastern")
	}

	// A due date of the 27th is beyond the sprint when read in Eastern, but the
	// very same sprint end lands on the 27th in UTC. The timezone must genuinely
	// change the answer.
	if InScope(date(2026, time.August, 27), sprint, eastern) {
		t.Error("due 27 Aug should be out of scope in Eastern")
	}
	if !InScope(date(2026, time.August, 27), sprint, time.UTC) {
		t.Error("due 27 Aug should be in scope when the project runs on UTC")
	}
}

func TestProjectSettingsLocation(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		loc, err := ProjectSettings{}.Location()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loc.String() != DefaultTimezone {
			t.Errorf("got %s, want %s", loc, DefaultTimezone)
		}
	})

	t.Run("rejects an unknown zone rather than falling back", func(t *testing.T) {
		if _, err := (ProjectSettings{Timezone: "Mars/Olympus_Mons"}).Location(); err == nil {
			t.Error("expected an error for an unrecognised timezone")
		}
	})

	t.Run("honours a configured zone", func(t *testing.T) {
		loc, err := ProjectSettings{Timezone: "Europe/Berlin"}.Location()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if loc.String() != "Europe/Berlin" {
			t.Errorf("got %s, want Europe/Berlin", loc)
		}
	})
}

func TestParseCalendarDate(t *testing.T) {
	got, err := ParseCalendarDate("2026-08-26")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := NewCalendarDate(2026, time.August, 26); got != want {
		t.Errorf("got %s, want %s", got, want)
	}

	if _, err := ParseCalendarDate("26/08/2026"); err == nil {
		t.Error("expected an error for a non-ISO date")
	}
}

func TestCalendarDateAfter(t *testing.T) {
	base := NewCalendarDate(2026, time.August, 26)
	tests := []struct {
		name   string
		other  CalendarDate
		expect bool
	}{
		{"same day", NewCalendarDate(2026, time.August, 26), false},
		{"next day", NewCalendarDate(2026, time.August, 27), false},
		{"previous day", NewCalendarDate(2026, time.August, 25), true},
		{"previous month", NewCalendarDate(2026, time.July, 31), true},
		{"next month", NewCalendarDate(2026, time.September, 1), false},
		{"previous year", NewCalendarDate(2025, time.December, 31), true},
		{"next year", NewCalendarDate(2027, time.January, 1), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := base.After(tc.other); got != tc.expect {
				t.Errorf("%s.After(%s) = %v, want %v", base, tc.other, got, tc.expect)
			}
		})
	}
}
