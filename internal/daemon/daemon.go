// Package daemon hosts the Go control plane. It intentionally owns the
// loopback listener, browser capability boundary, discovery record and static
// UI serving; no Node/Bun process is needed at runtime for those concerns.
package daemon

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/agent"
	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
	"github.com/uhvesta/cmux-localreview/internal/store"
)

const Version = "0.3.0-go-migration"

type Options struct {
	Port    int
	DataDir string
	UIDir   string
}

type discovery struct {
	Port      int    `json:"port"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	CreatedAt string `json:"createdAt"`
}

type Daemon struct {
	listener net.Listener
	server   *http.Server
	dataDir  string
	token    string
	mu       sync.Mutex
	sessions map[string]time.Time
	db       *sql.DB
}

func dataDirectory(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	if env := strings.TrimSpace(os.Getenv("CMUX_LOCALREVIEW_DATA_DIR")); env != "" {
		return filepath.Abs(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "cmux-localreview"), nil
}

func secureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeDiscovery(dir string, value discovery) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("daemon.json.%d.tmp", os.Getpid()))
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "daemon.json"))
}

func browserCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie("cmux_localreview_browser_session")
	if err != nil || c == nil {
		return "", false
	}
	return c.Value, c.Value != ""
}

func (d *Daemon) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		headerOK := r.Header.Get("Authorization") == "Bearer "+d.token
		cookieID, cookiePresent := browserCookie(r)
		d.mu.Lock()
		expires, cookieOK := d.sessions[cookieID]
		if cookieOK && time.Now().After(expires) {
			delete(d.sessions, cookieID)
			cookieOK = false
		}
		d.mu.Unlock()
		if !headerOK && (!cookiePresent || !cookieOK) {
			http.Error(w, `{"error":"A local browser session or daemon capability is required"}`, http.StatusUnauthorized)
			return
		}
		if !headerOK && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			expectedOrigin := "http://" + r.Host
			fetchSite := r.Header.Get("Sec-Fetch-Site")
			if r.Header.Get("Origin") != expectedOrigin || fetchSite == "cross-site" || fetchSite == "same-site" {
				http.Error(w, `{"error":"Browser mutations must originate from this daemon's exact loopback origin"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (d *Daemon) sessionExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+d.token {
		http.Error(w, `{"error":"Daemon capability required to create a browser session"}`, http.StatusUnauthorized)
		return
	}
	id, err := secureToken()
	if err != nil {
		http.Error(w, `{"error":"Could not create browser session"}`, http.StatusInternalServerError)
		return
	}
	expires := time.Now().Add(8 * time.Hour)
	d.mu.Lock()
	d.sessions[id] = expires
	d.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "cmux_localreview_browser_session", Value: id, Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int((8 * time.Hour).Seconds())})
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// apiHandler ports the queue control plane first. Unported routes fail
// explicitly; they never fall back to a Node process behind the caller.
func (d *Daemon) apiHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api")
	if path == "/agents" {
		switch r.Method {
		case http.MethodGet:
			items, err := agent.List(d.db)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"agents": items})
		case http.MethodPost:
			var input agent.Record
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent"})
				return
			}
			item, err := agent.Register(d.db, input)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"agent": item})
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if path == "/queue" {
		switch r.Method {
		case http.MethodGet:
			items, err := queueStore.List(d.db, r.URL.Query().Get("history") == "true")
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			var input queueStore.EnqueueInput
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid queue item"})
				return
			}
			item, created, err := queueStore.Enqueue(d.db, input)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, map[string]any{"item": item, "created": created})
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost {
		var item *queueStore.Item
		var err error
		switch parts[2] {
		case "open":
			item, err = queueStore.Open(d.db, parts[1])
		case "requeue":
			item, err = queueStore.Requeue(d.db, parts[1])
		case "complete":
			item, err = queueStore.Complete(d.db, parts[1], queueStore.Completed, "")
		default:
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "Go daemon API migration in progress: this route is not implemented"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown or unavailable queue item"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item})
		return
	}
	if len(parts) == 2 && parts[0] == "queue" && r.Method == http.MethodDelete {
		item, err := queueStore.Remove(d.db, parts[1], "")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown queue item"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item})
		return
	}
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "Go daemon API migration in progress: this route is not implemented"})
}

func staticHandler(uiDir string) http.Handler {
	if uiDir == "" {
		uiDir = filepath.Join("vendor", "difit", "dist", "client")
	}
	if _, err := os.Stat(uiDir); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "cmux-localreview web assets are not installed; run the web build or pass --ui-dir", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServer(http.Dir(uiDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		candidate := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if candidate != "." {
			if _, err := fs.Stat(os.DirFS(uiDir), candidate); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, filepath.Join(uiDir, "index.html"))
	})
}

// Start starts the Go daemon on loopback only and writes a compatible,
// owner-only discovery record. Route-by-route compatibility is added behind
// the same /api boundary as the TypeScript daemon during the migration.
func Start(ctx context.Context, options Options) (*Daemon, error) {
	dir, err := dataDirectory(options.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o700); err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(dir, "daemon.db"))
	if err != nil {
		return nil, err
	}
	token, err := secureToken()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", options.Port))
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	d := &Daemon{listener: listener, dataDir: dir, token: token, sessions: make(map[string]time.Time), db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": Version, "pid": os.Getpid(), "runtime": runtime.Version()})
	})
	mux.HandleFunc("/api/browser/session", d.sessionExchange)
	// The API router deliberately remains authenticated even while the static
	// single-page app is public on loopback.
	mux.Handle("/api/", d.authorized(http.HandlerFunc(d.apiHandler)))
	mux.Handle("/", staticHandler(options.UIDir))
	d.server = &http.Server{Handler: mux}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := writeDiscovery(dir, discovery{Port: port, Token: token, PID: os.Getpid(), Version: Version, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		_ = listener.Close()
		_ = db.Close()
		return nil, err
	}
	go func() {
		if err := d.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Start has returned; logging is the only safe reporting channel.
			fmt.Fprintln(os.Stderr, "localreviewd:", err)
		}
	}()
	go func() { <-ctx.Done(); _ = d.Close() }()
	return d, nil
}

func (d *Daemon) Port() int { return d.listener.Addr().(*net.TCPAddr).Port }

func (d *Daemon) Close() error {
	if d.server == nil {
		return nil
	}
	err := d.server.Shutdown(context.Background())
	if d.db != nil {
		if closeErr := d.db.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}
