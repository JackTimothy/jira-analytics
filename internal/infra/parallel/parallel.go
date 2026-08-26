// Package parallel holds the bounded fan-out both outward adapters need.
//
// It lives beside httpclient rather than inside either adapter because the
// concern is transport-shaped, not Jira- or GitHub-shaped: assembling one
// retrospective touches a few hundred endpoints, and every one of them wants
// the same answer to "how many of these at once".
package parallel

import (
	"context"
	"sync"
)

// ForEach runs fn over every item with at most limit running at once, returning
// the first error.
//
// Bounding matters more than raw parallelism here. Both hosts punish bursts
// with secondary rate limits, and the resulting backoff costs far more time
// than the extra concurrency would have saved.
//
// The first error cancels the context handed to every other invocation, so a
// failure stops the fan-out rather than paying for the remainder of a request
// whose result is already being discarded.
func ForEach[T any](ctx context.Context, items []T, limit int, fn func(context.Context, T) error) error {
	if len(items) == 0 {
		return nil
	}
	if limit < 1 {
		limit = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for _, item := range items {
		select {
		case <-ctx.Done():
			wg.Wait()
			if firstErr != nil {
				return firstErr
			}
			return ctx.Err()
		case slots <- struct{}{}:
		}

		wg.Add(1)
		go func(item T) {
			defer wg.Done()
			defer func() { <-slots }()

			if err := fn(ctx, item); err != nil {
				once.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}(item)
	}

	wg.Wait()
	return firstErr
}

// Map runs fn over every item concurrently and returns the results in input
// order, whatever order they actually completed in.
//
// Input order is the point. Without it every caller has to either lock a shared
// collection or sort afterwards, and a result order that varies between runs
// makes output non-deterministic for no reason anyone benefits from. Each
// goroutine writes one distinct index, so no lock is needed for the results
// themselves.
//
// On error the returned slice is nil: a partly filled result is the most
// dangerous thing this could hand back, because it looks exactly like a
// complete one.
func Map[T, R any](ctx context.Context, items []T, limit int, fn func(context.Context, T) (R, error)) ([]R, error) {
	results := make([]R, len(items))

	type indexed struct {
		at   int
		item T
	}
	work := make([]indexed, len(items))
	for i, item := range items {
		work[i] = indexed{at: i, item: item}
	}

	err := ForEach(ctx, work, limit, func(ctx context.Context, w indexed) error {
		result, err := fn(ctx, w.item)
		if err != nil {
			return err
		}
		results[w.at] = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
