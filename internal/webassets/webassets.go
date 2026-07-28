//go:build !bazel_ui_archive

// Package webassets provides the daemon's embedded, single-binary web asset
// boundary. This direct-Go fallback keeps `go test ./...` useful without Bun;
// Bazel builds select webassets_bazel.go, whose declared Vite build input
// prevents a stale renderer from shipping.
package webassets

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Files holds the checked-in bootstrap renderer used only for direct Go inner
// loop tests. Release and Bazel artifacts always embed the declared Vite build
// in webassets_bazel.go.
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
