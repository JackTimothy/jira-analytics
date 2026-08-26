package tracelog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock advances only when told to, so a test can assert exact durations
// instead of asserting that something took "roughly" a while.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func capture(t *testing.T, opts ...Option) (*Logger, func() map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := New(slog.New(handler), opts...)

	return logger, func() map[string]any {
		t.Helper()
		var record map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
			t.Fatalf("decoding log line %q: %v", buf.String(), err)
		}
		return record
	}
}

func TestEndReportsPhasesAttributesAndTotal(t *testing.T) {
	tick := newClock()
	logger, read := capture(t, WithClock(tick.Now))

	trace := logger.Begin("retrospective built", map[string]string{"project": "otco", "sprint": "90"})

	stop := trace.Phase("history")
	tick.advance(3 * time.Second)
	stop()

	tick.advance(1 * time.Second)
	trace.End()

	record := read()
	if got := record["msg"]; got != "retrospective built" {
		t.Errorf("msg = %v, want the operation name", got)
	}
	if got := record["project"]; got != "otco" {
		t.Errorf("project = %v, want otco", got)
	}
	if got := record["sprint"]; got != "90" {
		t.Errorf("sprint = %v, want 90", got)
	}
	// slog encodes durations as nanoseconds.
	if got := record["history"]; got != float64(3*time.Second) {
		t.Errorf("history = %v, want %v", got, float64(3*time.Second))
	}
	if got := record["total"]; got != float64(4*time.Second) {
		t.Errorf("total = %v, want %v", got, float64(4*time.Second))
	}
}

func TestRequestsReportsTheDeltaAcrossTheOperation(t *testing.T) {
	var counter atomic.Int64
	counter.Store(1000) // A monotonic process-wide counter, not starting at zero.

	logger, read := capture(t, WithRequestCounter(counter.Load))

	trace := logger.Begin("build", nil)
	counter.Add(218)
	trace.End()

	if got := read()["requests"]; got != float64(218) {
		t.Errorf("requests = %v, want the delta 218 rather than the absolute count", got)
	}
}

func TestRequestsIsOmittedWithoutACounter(t *testing.T) {
	logger, read := capture(t)

	logger.Begin("build", nil).End()

	if _, ok := read()["requests"]; ok {
		t.Error("requests reported with no counter configured; a fabricated zero is worse than silence")
	}
}

// Once the fetches overlap, two phases each report most of the total. That is
// the point of the report — it is how you see the overlap working — so the
// implementation must not assume phases partition the elapsed time.
func TestOverlappingPhasesAreBothReportedInFull(t *testing.T) {
	tick := newClock()
	logger, read := capture(t, WithClock(tick.Now))

	trace := logger.Begin("build", nil)
	stopHistory := trace.Phase("history")
	stopCode := trace.Phase("code")
	tick.advance(10 * time.Second)
	stopHistory()
	stopCode()
	trace.End()

	record := read()
	if record["history"] != float64(10*time.Second) || record["code"] != float64(10*time.Second) {
		t.Errorf("history = %v, code = %v; both ran concurrently for the full 10s", record["history"], record["code"])
	}
	if record["total"] != float64(10*time.Second) {
		t.Errorf("total = %v, want 10s — concurrent phases must not be summed", record["total"])
	}
}

func TestRepeatedPhaseNamesAccumulate(t *testing.T) {
	tick := newClock()
	logger, read := capture(t, WithClock(tick.Now))

	trace := logger.Begin("build", nil)
	for i := 0; i < 3; i++ {
		stop := trace.Phase("changelog")
		tick.advance(2 * time.Second)
		stop()
	}
	trace.End()

	if got := read()["changelog"]; got != float64(6*time.Second) {
		t.Errorf("changelog = %v, want the sum 6s across three runs", got)
	}
}

func TestConcurrentPhasesAreRaceFree(t *testing.T) {
	logger, _ := capture(t)
	trace := logger.Begin("build", nil)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer trace.Phase("fanout")()
		}()
	}
	wg.Wait()
	trace.End()
}
