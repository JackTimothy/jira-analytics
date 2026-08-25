package httpapi

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// WithStaticFiles serves the built web client alongside the API.
//
// A missing build directory is not fatal: the API is useful on its own during
// development, and failing to start because nobody has run the front-end build
// would be a poor trade.
func WithStaticFiles(api http.Handler, dir string, logger *slog.Logger) http.Handler {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		logger.Info("serving the API only; no web build found", slog.String("dir", dir))
		return api
	}

	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			api.ServeHTTP(w, r)
			return
		}

		// Anything that is not a real file is a client-side route, so hand back
		// the app shell and let the router resolve it.
		requested := filepath.Join(dir, filepath.Clean(r.URL.Path))
		if stat, err := os.Stat(requested); err != nil || stat.IsDir() {
			http.ServeFile(w, r, index)
			return
		}
		files.ServeHTTP(w, r)
	})
}
