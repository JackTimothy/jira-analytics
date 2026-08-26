package domain

import (
	"testing"
	"time"
)

func assertSegments(t *testing.T, got []AxisSegment, want []AxisSegment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d segments, want %d:\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || !got[i].From.Equal(want[i].From) || !got[i].To.Equal(want[i].To) {
			t.Errorf("segment %d = %s %s..%s, want %s %s..%s", i,
				got[i].Kind, got[i].From, got[i].To,
				want[i].Kind, want[i].From, want[i].To)
		}
	}
}

// assertTiling checks the invariants every segmentation must satisfy: the
// segments cover the window exactly, in order, without gaps or overlaps, and
// adjacent segments never share a kind (or they should have been merged).
func assertTiling(t *testing.T, w Window, segments []AxisSegment) {
	t.Helper()
	if len(segments) == 0 {
		t.Fatal("no segments")
	}
	if !segments[0].From.Equal(w.Start) {
		t.Errorf("first segment starts at %s, want the window start %s", segments[0].From, w.Start)
	}
	if !segments[len(segments)-1].To.Equal(w.End) {
		t.Errorf("last segment ends at %s, want the window end %s", segments[len(segments)-1].To, w.End)
	}
	for i, s := range segments {
		if !s.To.After(s.From) {
			t.Errorf("segment %d is empty or inverted: %s..%s", i, s.From, s.To)
		}
		if i > 0 {
			if !segments[i-1].To.Equal(s.From) {
				t.Errorf("gap or overlap between segment %d and %d: %s vs %s", i-1, i, segments[i-1].To, s.From)
			}
			if segments[i-1].Kind == s.Kind {
				t.Errorf("segments %d and %d share kind %s and were not merged", i-1, i, s.Kind)
			}
		}
	}
}

func eastern(t *testing.T) *time.Location { return mustLoad(t, "America/New_York") }

func inLoc(loc *time.Location, y int, m time.Month, d, hour, minute int) time.Time {
	return time.Date(y, m, d, hour, minute, 0, 0, loc)
}

func TestAxisSegmentsOrdinaryWeek(t *testing.T) {
	loc := eastern(t)
	// Monday 10 Aug 2026 09:00 to Wednesday 12 Aug 14:00 local.
	w := Window{Start: inLoc(loc, 2026, 8, 10, 9, 0), End: inLoc(loc, 2026, 8, 12, 14, 0)}

	got := AxisSegments(w, DefaultWorkingHours(), loc)
	assertTiling(t, w, got)

	assertSegments(t, got, []AxisSegment{
		{From: w.Start, To: inLoc(loc, 2026, 8, 10, 18, 0), Kind: SegmentWorking},
		{From: inLoc(loc, 2026, 8, 10, 18, 0), To: inLoc(loc, 2026, 8, 11, 8, 0), Kind: SegmentOffHours},
		{From: inLoc(loc, 2026, 8, 11, 8, 0), To: inLoc(loc, 2026, 8, 11, 18, 0), Kind: SegmentWorking},
		{From: inLoc(loc, 2026, 8, 11, 18, 0), To: inLoc(loc, 2026, 8, 12, 8, 0), Kind: SegmentOffHours},
		{From: inLoc(loc, 2026, 8, 12, 8, 0), To: w.End, Kind: SegmentWorking},
	})
}

func TestAxisSegmentsMergesTheWeekendIntoOneBand(t *testing.T) {
	loc := eastern(t)
	// Friday 7 Aug 2026 12:00 to Monday 10 Aug 12:00.
	w := Window{Start: inLoc(loc, 2026, 8, 7, 12, 0), End: inLoc(loc, 2026, 8, 10, 12, 0)}

	got := AxisSegments(w, DefaultWorkingHours(), loc)
	assertTiling(t, w, got)

	// Friday evening + all Saturday + all Sunday + Monday early morning must be
	// one off-hours segment, not four.
	assertSegments(t, got, []AxisSegment{
		{From: w.Start, To: inLoc(loc, 2026, 8, 7, 18, 0), Kind: SegmentWorking},
		{From: inLoc(loc, 2026, 8, 7, 18, 0), To: inLoc(loc, 2026, 8, 10, 8, 0), Kind: SegmentOffHours},
		{From: inLoc(loc, 2026, 8, 10, 8, 0), To: w.End, Kind: SegmentWorking},
	})
}

// The US transitions happen at 02:00 local, inside the off-hours band. What
// DST correctness means here: the working span stays anchored to the local
// clock (08:00..18:00, 10 real hours on every day of the year), while the
// morning off-hours span absorbs the anomaly — 7 real hours on the 23-hour
// day, 9 on the 25-hour one. Adding a duration to midnight instead of
// constructing clock times gets the anchoring wrong by an hour in each
// direction, which is the bug these two tests exist to catch.
func TestAxisSegmentsSpringForward(t *testing.T) {
	loc := eastern(t)
	// US DST begins Sunday 8 Mar 2026; the day is 23 real hours long.
	hours := WorkingHours{Days: []time.Weekday{time.Sunday}, Start: 8 * 60, End: 18 * 60}
	w := Window{Start: inLoc(loc, 2026, 3, 8, 0, 0), End: inLoc(loc, 2026, 3, 9, 0, 0)}

	got := AxisSegments(w, hours, loc)
	assertTiling(t, w, got)

	if len(got) != 3 {
		t.Fatalf("got %d segments, want 3: %v", len(got), got)
	}
	morning, working, evening := got[0], got[1], got[2]
	if working.From.In(loc).Format("15:04") != "08:00" || working.To.In(loc).Format("15:04") != "18:00" {
		t.Errorf("working span is %s..%s local, want 08:00..18:00",
			working.From.In(loc).Format("15:04"), working.To.In(loc).Format("15:04"))
	}
	if working.Duration() != 10*time.Hour {
		t.Errorf("working span lasted %s, want 10h — the skipped hour is at 02:00, not in it", working.Duration())
	}
	if morning.Duration() != 7*time.Hour {
		t.Errorf("morning off-hours lasted %s, want 7h — it absorbs the skipped hour", morning.Duration())
	}
	if total := morning.Duration() + working.Duration() + evening.Duration(); total != 23*time.Hour {
		t.Errorf("day sums to %s, want the 23-hour spring-forward day", total)
	}
}

func TestAxisSegmentsFallBack(t *testing.T) {
	loc := eastern(t)
	// US DST ends Sunday 1 Nov 2026; the day is 25 real hours long.
	hours := WorkingHours{Days: []time.Weekday{time.Sunday}, Start: 8 * 60, End: 18 * 60}
	w := Window{Start: inLoc(loc, 2026, 11, 1, 0, 0), End: inLoc(loc, 2026, 11, 2, 0, 0)}

	got := AxisSegments(w, hours, loc)
	assertTiling(t, w, got)

	if len(got) != 3 {
		t.Fatalf("got %d segments, want 3: %v", len(got), got)
	}
	morning, working, evening := got[0], got[1], got[2]
	if working.From.In(loc).Format("15:04") != "08:00" || working.To.In(loc).Format("15:04") != "18:00" {
		t.Errorf("working span is %s..%s local, want 08:00..18:00",
			working.From.In(loc).Format("15:04"), working.To.In(loc).Format("15:04"))
	}
	if working.Duration() != 10*time.Hour {
		t.Errorf("working span lasted %s, want 10h — the repeated hour is at 01:00, not in it", working.Duration())
	}
	if morning.Duration() != 9*time.Hour {
		t.Errorf("morning off-hours lasted %s, want 9h — it absorbs the repeated hour", morning.Duration())
	}
	if total := morning.Duration() + working.Duration() + evening.Duration(); total != 25*time.Hour {
		t.Errorf("day sums to %s, want the 25-hour fall-back day", total)
	}
}

func TestAxisSegmentsWindowEntirelyInsideOneWorkingDay(t *testing.T) {
	loc := eastern(t)
	w := Window{Start: inLoc(loc, 2026, 8, 11, 10, 0), End: inLoc(loc, 2026, 8, 11, 15, 0)}

	got := AxisSegments(w, DefaultWorkingHours(), loc)
	assertTiling(t, w, got)
	assertSegments(t, got, []AxisSegment{{From: w.Start, To: w.End, Kind: SegmentWorking}})
}

func TestAxisSegmentsWindowEntirelyInsideAWeekend(t *testing.T) {
	loc := eastern(t)
	w := Window{Start: inLoc(loc, 2026, 8, 8, 10, 0), End: inLoc(loc, 2026, 8, 9, 15, 0)}

	got := AxisSegments(w, DefaultWorkingHours(), loc)
	assertTiling(t, w, got)
	assertSegments(t, got, []AxisSegment{{From: w.Start, To: w.End, Kind: SegmentOffHours}})
}

func TestAxisSegmentsRealSprintShape(t *testing.T) {
	loc := eastern(t)
	// The shape of an actual sprint: two weeks, UTC instants, ending mid-day.
	w := Window{
		Start: time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 17, 18, 0, 0, 0, time.UTC),
	}

	got := AxisSegments(w, DefaultWorkingHours(), loc)
	assertTiling(t, w, got)

	var working, off time.Duration
	var workingCount int
	for _, s := range got {
		if s.Kind == SegmentWorking {
			working += s.Duration()
			workingCount++
		} else {
			off += s.Duration()
		}
	}
	// 3-7 Aug (Mon-Fri), 10-14 Aug (Mon-Fri), 17 Aug (Mon): 11 working days.
	// The first starts 09:00 local (window start), the last ends 14:00 local.
	if workingCount != 11 {
		t.Errorf("got %d working segments, want 11", workingCount)
	}
	want := 9*time.Hour + 9*10*time.Hour + 6*time.Hour
	if working != want {
		t.Errorf("working time = %s, want %s", working, want)
	}
	if working+off != w.End.Sub(w.Start) {
		t.Errorf("segments do not sum to the window: %s + %s != %s", working, off, w.End.Sub(w.Start))
	}
}

func TestAxisSegmentsDegradesToLinearOnAnInvalidSchedule(t *testing.T) {
	loc := eastern(t)
	w := Window{Start: inLoc(loc, 2026, 8, 10, 9, 0), End: inLoc(loc, 2026, 8, 12, 14, 0)}

	got := AxisSegments(w, WorkingHours{}, loc)
	assertSegments(t, got, []AxisSegment{{From: w.Start, To: w.End, Kind: SegmentWorking}})
}

func TestAxisSegmentsEmptyWindow(t *testing.T) {
	loc := eastern(t)
	at := inLoc(loc, 2026, 8, 10, 9, 0)
	if got := AxisSegments(Window{Start: at, End: at}, DefaultWorkingHours(), loc); got != nil {
		t.Errorf("expected nil for an empty window, got %v", got)
	}
}

func TestWorkingHoursValidate(t *testing.T) {
	valid := DefaultWorkingHours()
	if err := valid.Validate(); err != nil {
		t.Fatalf("the default schedule must validate: %v", err)
	}

	tests := []struct {
		name  string
		hours WorkingHours
	}{
		{"no days", WorkingHours{Start: 480, End: 1080}},
		{"duplicate day", WorkingHours{Days: []time.Weekday{time.Monday, time.Monday}, Start: 480, End: 1080}},
		{"start after end", WorkingHours{Days: []time.Weekday{time.Monday}, Start: 1080, End: 480}},
		{"start equal to end", WorkingHours{Days: []time.Weekday{time.Monday}, Start: 480, End: 480}},
		{"negative start", WorkingHours{Days: []time.Weekday{time.Monday}, Start: -1, End: 480}},
		{"end past midnight", WorkingHours{Days: []time.Weekday{time.Monday}, Start: 480, End: 24*60 + 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.hours.Validate(); err == nil {
				t.Error("expected an error")
			}
		})
	}
}
