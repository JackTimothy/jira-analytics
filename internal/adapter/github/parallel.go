package github

import (
	"context"
	"sync"
)

// forEachBounded runs fn over every item with at most limit running at once,
// returning the first error. Bounding matters more than raw parallelism here:
// GitHub's secondary rate limits punish bursts far more than they reward
// concurrency.
func forEachBounded[T any](ctx context.Context, items []T, limit int, fn func(context.Context, T) error) error {
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
