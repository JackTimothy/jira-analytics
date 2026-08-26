// Package httpclient holds transport concerns shared by the outward adapters:
// retries, rate-limit backoff, and a consistent way to turn a non-2xx response
// into an error that says what actually went wrong.
package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Doer is the seam tests substitute. It is satisfied by *http.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client wraps a Doer with retry behaviour suited to the read-only, bursty
// traffic these adapters generate: assembling one retrospective issues a few
// hundred requests, and a single transient failure should not lose the lot.
type Client struct {
	doer       Doer
	maxRetries int
	sleep      func(context.Context, time.Duration) error

	// requests counts every attempt that reached the network, retries
	// included. It lives here because this is the one place every outward call
	// passes through, so a count taken anywhere else would be a guess.
	requests atomic.Int64
}

// Requests reports how many attempts this client has made since it was built.
// It is monotonic and process-wide; callers interested in the cost of one
// operation should sample it either side and subtract.
func (c *Client) Requests() int64 {
	return c.requests.Load()
}

type Option func(*Client)

// WithMaxRetries overrides the default retry budget.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// WithSleep replaces the delay function so tests need not actually wait.
func WithSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

func New(doer Doer, opts ...Option) *Client {
	client := &Client{doer: doer, maxRetries: 3, sleep: sleepContext}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// StatusError reports a non-2xx response. The body is truncated because these
// APIs return verbose HTML on some failures and the first part is the useful
// part.
type StatusError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: unexpected status %d: %s", e.URL, e.StatusCode, e.Body)
}

const maxErrorBody = 512

// DoJSON performs the request, retrying transient failures, and decodes a JSON
// body into out. Passing a nil out discards the body.
//
// build is a factory rather than a request because a request body can only be
// read once, so a retry needs a fresh request.
func (c *Client) DoJSON(ctx context.Context, build func() (*http.Request, error), out any) error {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
		}

		req, err := build()
		if err != nil {
			return fmt.Errorf("building request: %w", err)
		}
		req = req.WithContext(ctx)

		c.requests.Add(1)
		resp, err := c.doer.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", req.URL, err)
			continue
		}

		if retryAfter, ok := shouldRetry(resp); ok {
			body := readTruncated(resp.Body)
			resp.Body.Close()
			lastErr = &StatusError{StatusCode: resp.StatusCode, URL: req.URL.String(), Body: body}
			if retryAfter > 0 {
				if err := c.sleep(ctx, retryAfter); err != nil {
					return err
				}
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			body := readTruncated(resp.Body)
			resp.Body.Close()
			return &StatusError{StatusCode: resp.StatusCode, URL: req.URL.String(), Body: body}
		}

		defer resp.Body.Close()
		if out == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("%s: decoding response: %w", req.URL, err)
		}
		return nil
	}

	return fmt.Errorf("giving up after %d attempts: %w", c.maxRetries+1, lastErr)
}

// shouldRetry reports whether a response is worth another attempt, and how long
// the server asked us to wait. Rate limiting and 5xx are transient; a 4xx is
// the caller's problem and retrying only wastes the remaining quota.
func shouldRetry(resp *http.Response) (time.Duration, bool) {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAfter(resp), true
	case resp.StatusCode >= 500:
		return retryAfter(resp), true
	default:
		return 0, false
	}
}

func retryAfter(resp *http.Response) time.Duration {
	value := resp.Header.Get("Retry-After")
	if value == "" {
		return 0
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<(attempt-1)) * 200 * time.Millisecond
}

func readTruncated(r io.Reader) string {
	body, _ := io.ReadAll(io.LimitReader(r, maxErrorBody))
	return string(body)
}
