// Package httpapi exposes the use cases over HTTP.
//
// Handlers here do three things and nothing else: decode the request, call a
// use case, and hand the result to a presenter. Business rules live inward of
// this package, which is why no handler contains a conditional about states,
// scope, or timezones.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so the response cannot be corrected.
		// Log it so a truncated body is diagnosable rather than mysterious.
		logger.Error("encoding response body", slog.Any("error", err))
	}
}

func writeError(w http.ResponseWriter, logger *slog.Logger, status int, message string) {
	writeJSON(w, logger, status, errorBody{Error: message})
}
