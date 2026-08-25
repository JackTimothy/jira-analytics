package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func apiStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "api")
	})
}

func TestWithStaticFilesFallsBackToTheAPIWhenThereIsNoBuild(t *testing.T) {
	handler := WithStaticFiles(apiStub(), filepath.Join(t.TempDir(), "absent"), discardLogger())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if rec.Body.String() != "api" {
		t.Errorf("got %q", rec.Body)
	}
}

func TestWithStaticFilesServesTheAppShellForClientRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("shell"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("code"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := WithStaticFiles(apiStub(), dir, discardLogger())

	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/projects", "api"},
		{"/healthz", "api"},
		{"/app.js", "code"},
		{"/projects/activation/sprints/100", "shell"},
		{"/", "shell"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Body.String() != tc.want {
				t.Errorf("GET %s served %q, want %q", tc.path, rec.Body, tc.want)
			}
		})
	}
}
