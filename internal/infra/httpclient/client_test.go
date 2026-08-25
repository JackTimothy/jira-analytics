package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

func get(url string) func() (*http.Request, error) {
	return func() (*http.Request, error) { return http.NewRequest(http.MethodGet, url, nil) }
}

func TestDoJSONDecodesSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"name":"ok"}`)
	}))
	defer server.Close()

	var out struct {
		Name string `json:"name"`
	}
	if err := New(server.Client(), WithSleep(noSleep)).DoJSON(context.Background(), get(server.URL), &out); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if out.Name != "ok" {
		t.Errorf("got %q", out.Name)
	}
}

func TestDoJSONRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"name":"eventually"}`)
	}))
	defer server.Close()

	var out struct {
		Name string `json:"name"`
	}
	if err := New(server.Client(), WithSleep(noSleep)).DoJSON(context.Background(), get(server.URL), &out); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if calls != 3 || out.Name != "eventually" {
		t.Errorf("calls=%d out=%q", calls, out.Name)
	}
}

func TestDoJSONHonoursRateLimiting(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()

	var waited []time.Duration
	client := New(server.Client(), WithSleep(func(_ context.Context, d time.Duration) error {
		waited = append(waited, d)
		return nil
	}))

	if err := client.DoJSON(context.Background(), get(server.URL), nil); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}

	// The server asked for two seconds; that must appear among the waits rather
	// than being replaced by our own backoff schedule.
	var honoured bool
	for _, d := range waited {
		if d == 2*time.Second {
			honoured = true
		}
	}
	if !honoured {
		t.Errorf("Retry-After was ignored; waits were %v", waited)
	}
}

func TestDoJSONDoesNotRetryClientErrors(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"errorMessages":["nope"]}`)
	}))
	defer server.Close()

	err := New(server.Client(), WithSleep(noSleep)).DoJSON(context.Background(), get(server.URL), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("a 404 was retried %d times", calls-1)
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusNotFound {
		t.Fatalf("got %v, want a StatusError with 404", err)
	}
	if !strings.Contains(statusErr.Body, "nope") {
		t.Errorf("error body lost the server's explanation: %q", statusErr.Body)
	}
}

func TestDoJSONGivesUpAfterBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := New(server.Client(), WithSleep(noSleep), WithMaxRetries(2)).
		DoJSON(context.Background(), get(server.URL), nil)
	if err == nil || !strings.Contains(err.Error(), "giving up after 3 attempts") {
		t.Fatalf("got %v", err)
	}
}

func TestDoJSONStopsOnCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := New(server.Client(), WithSleep(func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}))

	if err := client.DoJSON(ctx, get(server.URL), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
