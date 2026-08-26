package parallel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errBoom = errors.New("boom")

func TestForEachRespectsTheLimit(t *testing.T) {
	items := make([]int, 50)
	var mu sync.Mutex
	var running, peak int

	err := ForEach(context.Background(), items, 4, func(context.Context, int) error {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()

		time.Sleep(time.Millisecond)

		mu.Lock()
		running--
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if peak > 4 {
		t.Errorf("peak concurrency was %d, want at most 4", peak)
	}
}

func TestForEachReturnsTheFirstError(t *testing.T) {
	err := ForEach(context.Background(), []int{1, 2, 3}, 2, func(_ context.Context, i int) error {
		if i == 2 {
			return errBoom
		}
		return nil
	})
	if !errors.Is(err, errBoom) {
		t.Errorf("got %v, want errBoom", err)
	}
}

func TestForEachDoesNothingWithNoItems(t *testing.T) {
	if err := ForEach(context.Background(), []int(nil), 4, func(context.Context, int) error {
		t.Error("fn called for an empty slice")
		return nil
	}); err != nil {
		t.Fatalf("ForEach: %v", err)
	}
}

// A limit of zero must still make progress rather than deadlocking on a
// zero-capacity slot channel.
func TestForEachTreatsANonPositiveLimitAsOne(t *testing.T) {
	var count atomic.Int64
	if err := ForEach(context.Background(), []int{1, 2, 3}, 0, func(context.Context, int) error {
		count.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if count.Load() != 3 {
		t.Errorf("ran %d items, want 3", count.Load())
	}
}

func TestForEachCancelsSiblingsOnTheFirstError(t *testing.T) {
	var cancelled atomic.Bool

	err := ForEach(context.Background(), []int{1, 2}, 2, func(ctx context.Context, i int) error {
		if i == 1 {
			return errBoom
		}
		// The sibling must be told to stop rather than run to completion for a
		// result that will be thrown away.
		select {
		case <-ctx.Done():
			cancelled.Store(true)
		case <-time.After(2 * time.Second):
		}
		return nil
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("got %v, want errBoom", err)
	}
	if !cancelled.Load() {
		t.Error("sibling was not cancelled when its peer failed")
	}
}

func TestForEachStopsOnAnAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ForEach(ctx, []int{1, 2, 3}, 2, func(context.Context, int) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

// Results must come back in input order however the work interleaved, so
// callers neither lock nor sort and output stays deterministic.
func TestMapReturnsResultsInInputOrder(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7}

	results, err := Map(context.Background(), items, 8, func(_ context.Context, i int) (int, error) {
		// Reverse the completion order relative to the input order.
		time.Sleep(time.Duration(len(items)-i) * time.Millisecond)
		return i * 10, nil
	})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	for i, want := range []int{0, 10, 20, 30, 40, 50, 60, 70} {
		if results[i] != want {
			t.Fatalf("results = %v, want input order", results)
		}
	}
}

func TestMapReturnsNilRatherThanAPartialResult(t *testing.T) {
	results, err := Map(context.Background(), []int{1, 2, 3}, 1, func(_ context.Context, i int) (int, error) {
		if i == 2 {
			return 0, errBoom
		}
		return i, nil
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("got %v, want errBoom", err)
	}
	// A partly filled slice is the most dangerous thing to hand back: it looks
	// exactly like a complete one.
	if results != nil {
		t.Errorf("results = %v, want nil alongside the error", results)
	}
}

func TestMapOnAnEmptySliceIsEmptyNotAnError(t *testing.T) {
	results, err := Map(context.Background(), []int(nil), 4, func(context.Context, int) (int, error) {
		t.Error("fn called for an empty slice")
		return 0, nil
	})
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %v, want empty", results)
	}
}
