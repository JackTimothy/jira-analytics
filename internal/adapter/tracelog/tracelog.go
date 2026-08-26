// Package tracelog implements the Tracer port by writing one structured log
// line per traced operation.
//
// It is an adapter rather than a helper inside the use case because where
// timings go is a deployment concern: the same interactor should be able to
// report into a log here and into a metrics backend elsewhere without changing.
package tracelog

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jacktimothy/jira-analytics/internal/usecase"
)

// Logger reports traced operations to a slog handler.
type Logger struct {
	log *slog.Logger

	// requests reads a monotonic count of outward HTTP requests. Sampling it
	// either side of an operation is what turns "this was slow" into "this cost
	// 218 round trips", which is the number that actually points at a fix.
	// Nil is allowed, for a deployment that does not care.
	requests func() int64

	// now is injectable so tests need not sleep.
	now func() time.Time
}

type Option func(*Logger)

// WithRequestCounter supplies the outward-request counter to sample.
func WithRequestCounter(f func() int64) Option {
	return func(l *Logger) { l.requests = f }
}

// WithClock replaces the time source.
func WithClock(f func() time.Time) Option {
	return func(l *Logger) { l.now = f }
}

func New(log *slog.Logger, opts ...Option) *Logger {
	logger := &Logger{log: log, now: time.Now}
	for _, opt := range opts {
		opt(logger)
	}
	return logger
}

// Begin starts a trace. The returned Trace is safe for concurrent use.
func (l *Logger) Begin(operation string, attrs map[string]string) usecase.Trace {
	return &span{
		logger:    l,
		operation: operation,
		attrs:     attrs,
		startedAt: l.now(),
		requests:  l.sample(),
		phases:    map[string]time.Duration{},
	}
}

func (l *Logger) sample() int64 {
	if l.requests == nil {
		return 0
	}
	return l.requests()
}

type span struct {
	logger    *Logger
	operation string
	attrs     map[string]string
	startedAt time.Time
	requests  int64

	mu     sync.Mutex
	phases map[string]time.Duration
}

// Phase records elapsed time under name. Repeating a name accumulates, which is
// what a phase run once per item should do.
func (s *span) Phase(name string) func() {
	start := s.logger.now()
	return func() {
		elapsed := s.logger.now().Sub(start)
		s.mu.Lock()
		s.phases[name] += elapsed
		s.mu.Unlock()
	}
}

func (s *span) End() {
	total := s.logger.now().Sub(s.startedAt)

	s.mu.Lock()
	names := make([]string, 0, len(s.phases))
	for name := range s.phases {
		names = append(names, name)
	}
	sort.Strings(names)
	attrs := make([]any, 0, len(names)+len(s.attrs)+2)
	for _, name := range names {
		attrs = append(attrs, slog.Duration(name, s.phases[name].Round(time.Millisecond)))
	}
	s.mu.Unlock()

	// Configuration attributes first, then phases, then the totals: the line is
	// read left to right and the identity of what was built matters most.
	head := make([]any, 0, len(s.attrs))
	for _, key := range sortedKeys(s.attrs) {
		head = append(head, slog.String(key, s.attrs[key]))
	}

	tail := []any{slog.Duration("total", total.Round(time.Millisecond))}
	if s.logger.requests != nil {
		tail = append(tail, slog.Int64("requests", s.logger.sample()-s.requests))
	}

	s.logger.log.Info(s.operation, append(append(head, attrs...), tail...)...)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
