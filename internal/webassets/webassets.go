// Package webassets provides the daemon's embedded, single-binary web asset
// boundary. It has no JavaScript runtime dependency: Vite is used only by the
// explicit staging step before a release build.
package webassets

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Files holds a staged Vite distribution. The repository carries a tiny
// bootstrap page so ordinary Go/Bazel compilation remains reproducible before
// a frontend build; scripts/stage-ui-assets.sh replaces dist/ with the real
// Vite output before a distributable localreviewd is built.
//
//go:embed all:dist
var Files embed.FS

// FS returns the embedded Vite distribution rooted at its index.html.
func FS() (fs.FS, error) {
	return fs.Sub(Files, "dist")
}

// Handler serves static files and applies an SPA fallback only to route-like
// paths. Missing asset URLs intentionally return 404 instead of index.html,
// so a stale index cannot silently render with a missing JavaScript bundle.
func Handler() http.Handler {
	assets, err := FS()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "embedded web assets are unavailable", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServer(http.FS(assets))
	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		// Keep r.URL.Path unchanged: ServeFileFS redirects a request whose URL
		// itself ends in /index.html, which is not desirable for SPA routes.
		http.ServeFileFS(w, r, assets, "index.html")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		candidate := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if candidate == "" || candidate == "." {
			serveIndex(w, r)
			return
		}
		if _, err := fs.Stat(assets, candidate); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		if strings.Contains(path.Base(candidate), ".") {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r)
	})
}
