package domain

import "errors"

// Sentinel errors the outer layers translate into transport-level responses.
// They live in the domain so that no adapter has to guess which failures are
// the caller's fault, and so that translation never depends on error strings.
var (
	// ErrNotFound marks a request for something that does not exist.
	ErrNotFound = errors.New("not found")

	// ErrInvalidSettings marks user-supplied settings that cannot be applied.
	ErrInvalidSettings = errors.New("invalid settings")
)
