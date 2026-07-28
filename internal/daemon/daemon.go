// Package daemon hosts the Go control plane. It intentionally owns the
// loopback listener, browser capability boundary, discovery record and static
// UI serving; no Node/Bun process is needed at runtime for those concerns.
package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/agent"
	"github.com/uhvesta/cmux-localreview/internal/gitdiff"
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
	review   *workspaceReview
}

type reviewRepo struct {
	ID                    string `json:"id"`
	AbsolutePath          string `json:"-"`
	WorkspaceRelativePath string `json:"workspaceRelativePath"`
	DBID                  int64  `json:"-"`
}

type workspaceReview struct {
	Root      string
	SessionID int64
	Repos     []reviewRepo
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
	// Keep the media type identical to the frozen TypeScript daemon contract.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func repoID(path string) string {
	hash := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", hash[:6])
}

func gitRoot(path string) (string, error) {
	command := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(output)))
}

func gitOutput(path string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", path}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// discoverReviewRepos finds nested repositories without treating .git's
// internal contents as workspace files. A workspace may itself be a repository
// or contain several independent repositories.
func discoverReviewRepos(root string) ([]reviewRepo, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	repos := []reviewRepo{}
	add := func(candidate string) error {
		gitDir, err := gitRoot(candidate)
		if err != nil || seen[gitDir] {
			return nil
		}
		relative, err := filepath.Rel(resolved, gitDir)
		if err != nil || strings.HasPrefix(relative, "..") {
			return nil
		}
		if relative == "." {
			relative = "."
		}
		seen[gitDir] = true
		repos = append(repos, reviewRepo{ID: repoID(gitDir), AbsolutePath: gitDir, WorkspaceRelativePath: filepath.ToSlash(relative)})
		return nil
	}
	if err := add(resolved); err != nil {
		return nil, err
	}
	err = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if path != resolved {
			if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
				if err := add(path); err != nil {
					return err
				}
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].WorkspaceRelativePath < repos[j].WorkspaceRelativePath })
	if len(repos) == 0 {
		return nil, errors.New("no Git repositories found under workspace")
	}
	return repos, nil
}

func safeWorkspacePath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("workspacePath is required")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workspacePath must be a directory")
	}
	return filepath.EvalSymlinks(abs)
}

func (d *Daemon) reviewRepo(id string) (reviewRepo, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.review == nil {
		return reviewRepo{}, false
	}
	for _, repo := range d.review.Repos {
		if repo.ID == id {
			return repo, true
		}
	}
	return reviewRepo{}, false
}

func (d *Daemon) reviewContext(id string) (workspaceReview, reviewRepo, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.review == nil {
		return workspaceReview{}, reviewRepo{}, false
	}
	for _, repo := range d.review.Repos {
		if repo.ID == id {
			return *d.review, repo, true
		}
	}
	return workspaceReview{}, reviewRepo{}, false
}

func repoRelativePath(value string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid repository-relative path")
	}
	return filepath.ToSlash(clean), nil
}

func readRepoFile(repo reviewRepo, path, ref string) ([]byte, error) {
	relative, err := repoRelativePath(path)
	if err != nil {
		return nil, err
	}
	if ref == "" || ref == "." || ref == "working" {
		return os.ReadFile(filepath.Join(repo.AbsolutePath, filepath.FromSlash(relative)))
	}
	command := exec.Command("git", "-C", repo.AbsolutePath, "show", ref+":"+relative)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git show: %s", strings.TrimSpace(string(output)))
	}
	return output, nil
}

func lineCount(contents []byte) int {
	if len(contents) == 0 {
		return 0
	}
	count := bytes.Count(contents, []byte{'\n'})
	if contents[len(contents)-1] != '\n' {
		count++
	}
	return count
}

// generatedStatus mirrors the three observable difit layers: cheap path
// recognition first, then standard generated-code markers in the first 20
// lines. It intentionally returns source so the UI can explain lazy checks.
func generatedStatus(path string, contents []byte) (bool, string) {
	lower := strings.ToLower(filepath.ToSlash(path))
	name := filepath.Base(lower)
	for _, exact := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "cargo.lock", "go.sum", "go.mod", "bun.lockb", "uv.lock", "package.resolved"} {
		if name == exact {
			return true, "path"
		}
	}
	for _, prefix := range []string{"vendor/", "node_modules/", "dist/", "build/", "out/"} {
		if strings.HasPrefix(lower, prefix) {
			return true, "path"
		}
	}
	for _, suffix := range []string{".min.js", ".min.css", ".map", ".pb.go", ".grpc.pb.go", ".generated.ts", ".gen.go", ".graphql.ts", ".openapi.ts"} {
		if strings.HasSuffix(name, suffix) {
			return true, "path"
		}
	}
	if strings.Contains(lower, "/generated/") || strings.Contains(lower, "/gen/") || strings.Contains(lower, "/__generated__/") || (strings.HasPrefix(name, "mock_") && strings.HasSuffix(name, ".go")) {
		return true, "path"
	}
	if strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".markdown") {
		return false, "content"
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) > 20 {
		lines = lines[:20]
	}
	header := strings.ToLower(strings.Join(lines, "\n"))
	for _, marker := range []string{"@generated", "do not edit", "do not modify", "auto-generated", "autogenerated", "this file is generated", "generated code", "machine generated", "generated by", "code generated"} {
		if strings.Contains(header, marker) {
			return true, "content"
		}
	}
	return false, "content"
}

func fullFileGates(chunks []gitdiff.Chunk, side string) []map[string]any {
	gateType := "delete"
	if side == "base" {
		gateType = "add"
	}
	gates := []map[string]any{}
	anchor := 0
	var pending map[string]any
	flush := func() {
		if pending != nil {
			pending["afterLine"] = anchor
			gates = append(gates, pending)
			pending = nil
		}
	}
	for _, chunk := range chunks {
		for _, line := range chunk.Lines {
			if line.Type == gateType {
				hidden := line.OldLineNumber
				if side == "base" {
					hidden = line.NewLineNumber
				}
				if pending == nil {
					start := 0
					if hidden != nil {
						start = *hidden
					}
					pending = map[string]any{"hiddenStart": start, "hiddenEnd": start, "lines": []string{}}
				}
				pending["lines"] = append(pending["lines"].([]string), line.Content)
				if hidden != nil {
					pending["hiddenEnd"] = *hidden
				}
				continue
			}
			flush()
			shown := line.NewLineNumber
			if side == "base" {
				shown = line.OldLineNumber
			}
			if shown != nil {
				anchor = *shown
			}
		}
	}
	flush()
	return gates
}

type storedCommentMessage struct {
	Body   string `json:"body"`
	Author string `json:"author"`
}

type exportThread struct {
	File, Body, Code string
	Start, End       int64
	Messages         []storedCommentMessage
}

func formatThreadPrompt(thread exportThread) string {
	line := fmt.Sprintf("L%d", thread.Start)
	if thread.End != thread.Start {
		line = fmt.Sprintf("L%d-L%d", thread.Start, thread.End)
	}
	sections := []string{thread.File + ":" + line}
	for index, message := range thread.Messages {
		if strings.TrimSpace(message.Body) == "" {
			continue
		}
		if index > 0 {
			author := strings.TrimSpace(message.Author)
			if author == "" {
				author = "Unknown"
			}
			sections = append(sections, fmt.Sprintf("Reply %d (%s)", index, author))
		}
		sections = append(sections, message.Body)
	}
	if len(sections) == 1 && thread.Body != "" {
		sections = append(sections, thread.Body)
	}
	return strings.Join(sections, "\n")
}

func (d *Daemon) exportThreads(sessionID int64) ([]exportThread, string, error) {
	d.mu.Lock()
	review := d.review
	d.mu.Unlock()
	if review == nil {
		return nil, "", nil
	}
	rows, err := d.db.Query(`SELECT r.workspace_relative_path,c.file_path,c.start_line,c.end_line,c.body,c.messages_json,c.anchor_content FROM comments c JOIN repos r ON r.id=c.repo_id WHERE c.session_id=? AND c.channel='formal' ORDER BY r.workspace_relative_path,c.file_path,c.start_line,c.created_at`, sessionID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	threads := []exportThread{}
	for rows.Next() {
		var root, file, body string
		var start, end int64
		var messages, code sql.NullString
		if err := rows.Scan(&root, &file, &start, &end, &body, &messages, &code); err != nil {
			return nil, "", err
		}
		path := file
		if root != "" && root != "." {
			path = filepath.ToSlash(filepath.Join(root, file))
		}
		thread := exportThread{File: path, Body: body, Start: start, End: end}
		if code.Valid {
			thread.Code = code.String
		}
		if messages.Valid {
			_ = json.Unmarshal([]byte(messages.String), &thread.Messages)
		}
		threads = append(threads, thread)
	}
	return threads, review.Root, rows.Err()
}

func (d *Daemon) exportPrompt(sessionID int64) (string, error) {
	threads, root, err := d.exportThreads(sessionID)
	if err != nil || len(threads) == 0 {
		return "", err
	}
	formatted := make([]string, 0, len(threads))
	for _, thread := range threads {
		formatted = append(formatted, formatThreadPrompt(thread))
	}
	return "Review feedback\nWorkspace root: " + root + "\nFile paths below are relative to this workspace root (not a repository root).\n\n" + strings.Join(formatted, "\n=====\n"), nil
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// shellQuote is display-only: reproduction plans are intended to be copied
// into a shell, and a single-quoted path avoids surprising word splitting.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// apiHandler ports the queue control plane first. Unported routes fail
// explicitly; they never fall back to a Node process behind the caller.
func (d *Daemon) apiHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api")
	// Queue Home is intentionally useful before a review workspace is open.
	// These read models let the retained React landing page render against the
	// Go daemon while workspace/diff activation is ported separately.
	if path == "/workspaces" && r.Method == http.MethodGet {
		rows, err := d.db.Query(`SELECT root_path,label,last_opened_at,active FROM workspace_registry ORDER BY active DESC,last_opened_at DESC`)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		workspaces := []map[string]any{}
		var active any
		for rows.Next() {
			var path string
			var label sql.NullString
			var opened int64
			var isActive bool
			if err := rows.Scan(&path, &label, &opened, &isActive); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			workspaces = append(workspaces, map[string]any{"workspacePath": path, "label": nullableString(label), "lastOpenedAt": opened, "active": isActive})
			if isActive {
				active = path
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"workspaces": workspaces, "activeWorkspace": active})
		return
	}
	if path == "/workspaces" && r.Method == http.MethodPost {
		var input struct {
			WorkspacePath string `json:"workspacePath"`
			Path          string `json:"path"`
			Label         string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid workspace"})
			return
		}
		workspace, err := safeWorkspacePath(input.WorkspacePath)
		if workspace == "" {
			workspace, err = safeWorkspacePath(input.Path)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if _, err := d.db.Exec(`INSERT INTO workspace_registry(root_path,label,last_opened_at,active) VALUES(?,?,?,0) ON CONFLICT(root_path) DO UPDATE SET label=COALESCE(excluded.label,label),last_opened_at=excluded.last_opened_at`, workspace, nullable(input.Label), time.Now().UnixMilli()); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"workspacePath": workspace})
		return
	}
	if path == "/workspaces/open" && r.Method == http.MethodPost {
		var input struct {
			WorkspacePath string `json:"workspacePath"`
			Path          string `json:"path"`
			Label         string `json:"label"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid workspace"})
			return
		}
		workspace, err := safeWorkspacePath(input.WorkspacePath)
		if workspace == "" {
			workspace, err = safeWorkspacePath(input.Path)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		repos, err := discoverReviewRepos(workspace)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		now := time.Now().UnixMilli()
		tx, err := d.db.Begin()
		if err == nil {
			_, err = tx.Exec(`UPDATE workspace_registry SET active=0`)
			if err == nil {
				_, err = tx.Exec(`INSERT INTO workspace_registry(root_path,label,last_opened_at,active) VALUES(?,?,?,1) ON CONFLICT(root_path) DO UPDATE SET label=COALESCE(excluded.label,label),last_opened_at=excluded.last_opened_at,active=1`, workspace, nullable(input.Label), now)
			}
			var sessionID int64
			if err == nil {
				result, insertErr := tx.Exec(`INSERT INTO sessions(label,started_at) VALUES(?,?)`, nullable(input.Label), now)
				err = insertErr
				if err == nil {
					sessionID, err = result.LastInsertId()
				}
			}
			if err == nil {
				for index := range repos {
					_, err = tx.Exec(`INSERT INTO repos(workspace_relative_path,git_dir,created_at) VALUES(?,?,?) ON CONFLICT(git_dir) DO UPDATE SET workspace_relative_path=excluded.workspace_relative_path`, repos[index].WorkspaceRelativePath, repos[index].AbsolutePath, now)
					if err != nil {
						break
					}
					err = tx.QueryRow(`SELECT id FROM repos WHERE git_dir=?`, repos[index].AbsolutePath).Scan(&repos[index].DBID)
					if err != nil {
						break
					}
				}
				if err == nil {
					d.mu.Lock()
					d.review = &workspaceReview{Root: workspace, SessionID: sessionID, Repos: repos}
					d.mu.Unlock()
				}
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"workspacePath": workspace, "repos": repos, "reviewUrl": "/review"})
		return
	}
	if path == "/repos" && r.Method == http.MethodGet {
		d.mu.Lock()
		review := d.review
		d.mu.Unlock()
		if review == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "No workspace is open. Use POST /api/workspaces/open."})
			return
		}
		items := make([]map[string]any, 0, len(review.Repos))
		for _, repo := range review.Repos {
			items = append(items, map[string]any{"id": repo.ID, "workspaceRelativePath": repo.WorkspaceRelativePath, "changeCount": 0, "files": []string{}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"workspaceRoot": review.Root, "repos": items})
		return
	}
	if path == "/ui-state" && r.Method == http.MethodGet {
		key := r.URL.Query().Get("key")
		if key == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key is required"})
			return
		}
		var value string
		var revision int
		var updated int64
		err := d.db.QueryRow(`SELECT value,revision,updated_at FROM ui_state WHERE key=?`, key).Scan(&value, &revision, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": nil, "revision": 0, "updatedAt": nil})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		var parsed any
		_ = json.Unmarshal([]byte(value), &parsed)
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": parsed, "revision": revision, "updatedAt": updated})
		return
	}
	if path == "/ui-state" && r.Method == http.MethodPut {
		var input struct {
			Key      string `json:"key"`
			Value    any    `json:"value"`
			Revision *int   `json:"revision"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil || input.Key == "" || len(input.Key) > 512 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid key is required"})
			return
		}
		encoded, _ := json.Marshal(input.Value)
		tx, err := d.db.Begin()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		var current int
		scanErr := tx.QueryRow(`SELECT revision FROM ui_state WHERE key=?`, input.Key).Scan(&current)
		if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": scanErr.Error()})
			return
		}
		if input.Revision != nil && *input.Revision != current {
			_ = tx.Rollback()
			writeJSON(w, http.StatusConflict, map[string]any{"error": "stale ui state", "revision": current})
			return
		}
		next := current + 1
		_, err = tx.Exec(`INSERT INTO ui_state(key,value,updated_at,revision) VALUES(?,?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at,revision=excluded.revision`, input.Key, string(encoded), time.Now().UnixMilli(), next)
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": input.Key, "revision": next})
		return
	}
	if path == "/export/prompt" && r.Method == http.MethodGet {
		d.mu.Lock()
		review := d.review
		d.mu.Unlock()
		if review == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			return
		}
		sessionID := review.SessionID
		if raw := r.URL.Query().Get("sessionId"); raw != "" {
			if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
				sessionID = parsed
			}
		}
		prompt, err := d.exportPrompt(sessionID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(prompt))
		return
	}
	if path == "/sessions/new" && r.Method == http.MethodPost {
		d.mu.Lock()
		review := d.review
		d.mu.Unlock()
		if review == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "No workspace is open"})
			return
		}
		var input struct {
			Label string `json:"label"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input)
		now := time.Now().UnixMilli()
		tx, err := d.db.Begin()
		if err == nil {
			_, err = tx.Exec(`UPDATE sessions SET frozen_at=? WHERE id=? AND frozen_at IS NULL`, now, review.SessionID)
			var result sql.Result
			if err == nil {
				result, err = tx.Exec(`INSERT INTO sessions(label,started_at) VALUES(?,?)`, nullable(input.Label), now)
			}
			var id int64
			if err == nil {
				id, err = result.LastInsertId()
				if err == nil {
					d.mu.Lock()
					if d.review != nil {
						d.review.SessionID = id
					}
					d.mu.Unlock()
				}
			}
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"sessionId": id})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if path == "/sessions" && r.Method == http.MethodGet {
		d.mu.Lock()
		review := d.review
		d.mu.Unlock()
		if review == nil {
			writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}, "activeSessionId": nil})
			return
		}
		rows, err := d.db.Query(`SELECT id,label,started_at,frozen_at FROM sessions ORDER BY started_at DESC`)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		sessions := []map[string]any{}
		for rows.Next() {
			var id int64
			var label sql.NullString
			var started int64
			var frozen sql.NullInt64
			if err := rows.Scan(&id, &label, &started, &frozen); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			sessions = append(sessions, map[string]any{"id": id, "label": nullableString(label), "startedAt": started, "frozenAt": func() any {
				if frozen.Valid {
					return frozen.Int64
				}
				return nil
			}()})
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "activeSessionId": review.SessionID})
		return
	}
	if path == "/review-history/comments" && r.Method == http.MethodGet {
		d.mu.Lock()
		review := d.review
		d.mu.Unlock()
		if review == nil {
			writeJSON(w, http.StatusOK, map[string]any{"comments": []any{}})
			return
		}
		rows, err := d.db.Query(`SELECT s.id,s.label,r.workspace_relative_path,c.file_path,c.side,c.start_line,c.end_line,c.messages_json,c.orphaned FROM comments c JOIN sessions s ON s.id=c.session_id JOIN repos r ON r.id=c.repo_id WHERE c.session_id<>? ORDER BY s.started_at DESC,c.created_at ASC`, review.SessionID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		comments := []map[string]any{}
		for rows.Next() {
			var session int64
			var label, file, side, messages sql.NullString
			var root string
			var start, end int64
			var orphan int
			if err := rows.Scan(&session, &label, &root, &file, &side, &start, &end, &messages, &orphan); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			var line any = start
			if start != end {
				line = map[string]int64{"start": start, "end": end}
			}
			var parsed any = []any{}
			if messages.Valid {
				_ = json.Unmarshal([]byte(messages.String), &parsed)
			}
			comments = append(comments, map[string]any{"reviewSessionId": session, "reviewLabel": nullableString(label), "workspaceRelativePath": root, "filePath": file.String, "position": map[string]any{"side": side.String, "line": line}, "messages": parsed, "orphaned": orphan != 0})
		}
		writeJSON(w, http.StatusOK, map[string]any{"comments": comments})
		return
	}
	// The React reviewer scopes all repository API calls below this prefix.
	// Start with `/api/diff`, whose response is the primary rendering contract;
	// sibling routes (comments/blobs/revisions) are ported independently.
	if strings.HasPrefix(path, "/repos/") {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "comments-output" && r.Method == http.MethodGet {
			// difit's compatibility endpoint intentionally remains empty; formal
			// feedback is exported only through the explicit workspace-level API.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			return
		}
		if len(parts) >= 5 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "line-count" && r.Method == http.MethodGet {
			repo, ok := d.reviewRepo(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			filePath := strings.Join(parts[4:], "/")
			if _, err := repoRelativePath(filePath); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "File path outside repository"})
				return
			}
			response := map[string]any{}
			if ref := r.URL.Query().Get("oldRef"); ref != "" {
				contents, err := readRepoFile(repo, filePath, ref)
				if err != nil {
					response["oldLineCount"] = 0
				} else {
					response["oldLineCount"] = lineCount(contents)
				}
			}
			if ref := r.URL.Query().Get("newRef"); ref != "" {
				contents, err := readRepoFile(repo, filePath, ref)
				if err != nil {
					response["newLineCount"] = 0
				} else {
					response["newLineCount"] = lineCount(contents)
				}
			}
			writeJSON(w, http.StatusOK, response)
			return
		}
		if len(parts) >= 5 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "generated-status" && r.Method == http.MethodGet {
			repo, ok := d.reviewRepo(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			filePath := strings.Join(parts[4:], "/")
			if _, err := repoRelativePath(filePath); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "File path outside repository"})
				return
			}
			ref := r.URL.Query().Get("ref")
			if ref == "" {
				ref = "."
			}
			contents, err := readRepoFile(repo, filePath, ref)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not found"})
				return
			}
			generated, source := generatedStatus(filePath, contents)
			writeJSON(w, http.StatusOK, map[string]any{"path": filePath, "ref": ref, "isGenerated": generated, "source": source})
			return
		}
		if len(parts) >= 5 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "blob" && r.Method == http.MethodGet {
			repo, ok := d.reviewRepo(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			contents, err := readRepoFile(repo, strings.Join(parts[4:], "/"), r.URL.Query().Get("ref"))
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(contents)
			return
		}
		if len(parts) >= 5 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "fullfile" && r.Method == http.MethodGet {
			repo, ok := d.reviewRepo(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			filePath := strings.Join(parts[4:], "/")
			diff, err := gitdiff.Parse(repo.AbsolutePath, gitdiff.Selection{})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			var selected *gitdiff.File
			for index := range diff.Files {
				if diff.Files[index].Path == filePath || (diff.Files[index].OldPath != nil && *diff.Files[index].OldPath == filePath) {
					selected = &diff.Files[index]
					break
				}
			}
			if selected == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "File not found in diff"})
				return
			}
			side := "current"
			ref := diff.TargetCommitish
			readPath := filePath
			if r.URL.Query().Get("side") == "base" {
				side = "base"
				ref = diff.BaseCommitish
				if selected.OldPath != nil {
					readPath = *selected.OldPath
				}
			}
			if (side == "current" && selected.Status == "deleted") || (side == "base" && selected.Status == "added") {
				writeJSON(w, http.StatusOK, map[string]any{"side": side, "path": filePath, "status": selected.Status, "deleted": side == "current", "added": side == "base"})
				return
			}
			contents, err := readRepoFile(repo, readPath, ref)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
			writeJSON(w, http.StatusOK, map[string]any{"side": side, "path": filePath, "oldPath": selected.OldPath, "status": selected.Status, "lines": lines, "gates": fullFileGates(selected.Chunks, side)})
			return
		}
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "comments-json" && r.Method == http.MethodGet {
			review, repo, ok := d.reviewContext(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			rows, err := d.db.Query(`SELECT thread_id,file_path,side,start_line,end_line,messages_json,created_at,updated_at,anchor_content FROM comments WHERE session_id=? AND repo_id=? ORDER BY created_at,id`, review.SessionID, repo.DBID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			defer rows.Close()
			threads := []map[string]any{}
			for rows.Next() {
				var id, file, side string
				var start, end, created, updated int64
				var messages, content sql.NullString
				if err := rows.Scan(&id, &file, &side, &start, &end, &messages, &created, &updated, &content); err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				var line any = start
				if start != end {
					line = map[string]int64{"start": start, "end": end}
				}
				var parsed any = []any{}
				if messages.Valid {
					_ = json.Unmarshal([]byte(messages.String), &parsed)
				}
				thread := map[string]any{"id": id, "filePath": file, "createdAt": time.UnixMilli(created).UTC().Format(time.RFC3339Nano), "updatedAt": time.UnixMilli(updated).UTC().Format(time.RFC3339Nano), "position": map[string]any{"side": side, "line": line}, "messages": parsed}
				if content.Valid {
					thread["codeSnapshot"] = map[string]string{"content": content.String}
				}
				threads = append(threads, thread)
			}
			writeJSON(w, http.StatusOK, map[string]any{"version": 0, "threads": threads})
			return
		}
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "comments" && r.Method == http.MethodPost {
			review, repo, ok := d.reviewContext(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			var payload struct {
				Threads []struct {
					ID       string `json:"id"`
					FilePath string `json:"filePath"`
					Position struct {
						Side string          `json:"side"`
						Line json.RawMessage `json:"line"`
					} `json:"position"`
					Messages     json.RawMessage `json:"messages"`
					CodeSnapshot struct {
						Content string `json:"content"`
					} `json:"codeSnapshot"`
				} `json:"threads"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&payload); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid comment data"})
				return
			}
			now := time.Now().UnixMilli()
			tx, err := d.db.Begin()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			for _, thread := range payload.Threads {
				if thread.ID == "" || thread.FilePath == "" || (thread.Position.Side != "old" && thread.Position.Side != "new") {
					_ = tx.Rollback()
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid comment thread"})
					return
				}
				var tombstoned int
				if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM comment_tombstones WHERE session_id=? AND repo_id=? AND thread_id=?)`, review.SessionID, repo.DBID, thread.ID).Scan(&tombstoned); err != nil {
					_ = tx.Rollback()
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				if tombstoned != 0 {
					continue
				}
				var start, end int64
				var single int64
				if json.Unmarshal(thread.Position.Line, &single) == nil {
					start, end = single, single
				} else {
					var span struct {
						Start int64 `json:"start"`
						End   int64 `json:"end"`
					}
					if json.Unmarshal(thread.Position.Line, &span) != nil || span.Start < 1 || span.End < span.Start {
						_ = tx.Rollback()
						writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid comment line"})
						return
					}
					start, end = span.Start, span.End
				}
				var messages []struct {
					Body string `json:"body"`
				}
				_ = json.Unmarshal(thread.Messages, &messages)
				body := ""
				if len(messages) > 0 {
					body = messages[0].Body
				}
				if body == "" {
					_ = tx.Rollback()
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Comment body is required"})
					return
				}
				hash := sha256.Sum256([]byte(thread.CodeSnapshot.Content))
				_, err = tx.Exec(`INSERT INTO comments(session_id,repo_id,file_path,side,start_line,end_line,body,anchor_content_hash,created_at,updated_at,thread_id,messages_json,anchor_content,channel) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?, 'formal') ON CONFLICT(session_id,repo_id,thread_id) DO UPDATE SET file_path=excluded.file_path,side=excluded.side,start_line=excluded.start_line,end_line=excluded.end_line,body=excluded.body,anchor_content_hash=excluded.anchor_content_hash,updated_at=excluded.updated_at,messages_json=excluded.messages_json,anchor_content=excluded.anchor_content`, review.SessionID, repo.DBID, thread.FilePath, thread.Position.Side, start, end, body, fmt.Sprintf("%x", hash[:]), now, now, thread.ID, string(thread.Messages), nullable(thread.CodeSnapshot.Content))
				if err != nil {
					_ = tx.Rollback()
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
			}
			if err = tx.Commit(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "merged": false, "version": now})
			return
		}
		if len(parts) == 5 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "comments" && r.Method == http.MethodDelete {
			review, repo, ok := d.reviewContext(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			threadID := parts[4]
			if threadID == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "threadId is required"})
				return
			}
			now := time.Now().UnixMilli()
			tx, err := d.db.Begin()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			result, err := tx.Exec(`DELETE FROM comments WHERE session_id=? AND repo_id=? AND thread_id=?`, review.SessionID, repo.DBID, threadID)
			if err == nil {
				_, err = tx.Exec(`INSERT INTO comment_tombstones(session_id,repo_id,thread_id,deleted_at) VALUES(?,?,?,?) ON CONFLICT(session_id,repo_id,thread_id) DO UPDATE SET deleted_at=excluded.deleted_at`, review.SessionID, repo.DBID, threadID, now)
			}
			if err != nil {
				_ = tx.Rollback()
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if err = tx.Commit(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			deleted, _ := result.RowsAffected()
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": deleted > 0, "threadId": threadID, "version": now})
			return
		}
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "diff" && r.Method == http.MethodGet {
			repo, ok := d.reviewRepo(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			selection := gitdiff.Selection{BaseCommitish: r.URL.Query().Get("base"), TargetCommitish: r.URL.Query().Get("target"), IgnoreWhitespace: r.URL.Query().Get("ignoreWhitespace") == "true"}
			if raw := r.URL.Query().Get("contextLines"); raw != "" {
				if value, err := strconv.Atoi(raw); err == nil && value >= 0 && value <= 10_000 {
					selection.ContextLines = &value
				}
			}
			response, err := gitdiff.Parse(repo.AbsolutePath, selection)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			response.RepositoryID = repo.ID
			writeJSON(w, http.StatusOK, response)
			return
		}
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "revisions" && r.Method == http.MethodGet {
			repo, ok := d.reviewRepo(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			branchOutput, err := gitOutput(repo.AbsolutePath, "for-each-ref", "--format=%(refname:short)%00%(HEAD)", "refs/heads")
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			branches := []map[string]any{}
			for _, line := range strings.Split(branchOutput, "\n") {
				fields := strings.Split(line, "\x00")
				if len(fields) == 2 && fields[0] != "" {
					branches = append(branches, map[string]any{"name": fields[0], "current": strings.TrimSpace(fields[1]) == "*"})
				}
			}
			logOutput, err := gitOutput(repo.AbsolutePath, "log", "--format=%H%x00%h%x00%s", "-n", "100")
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			commits := []map[string]string{}
			for _, line := range strings.Split(logOutput, "\n") {
				fields := strings.Split(line, "\x00")
				if len(fields) == 3 {
					commits = append(commits, map[string]string{"hash": fields[0], "shortHash": fields[1], "message": fields[2]})
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"specialOptions": []map[string]string{{"value": ".", "label": "Working Directory"}, {"value": "staged", "label": "Staging Area"}}, "branches": branches, "commits": commits})
			return
		}
	}
	if path == "/federation/queue" && r.Method == http.MethodGet {
		// The local view never fabricates remote data. Federation transport is
		// ported separately; an empty aggregate keeps Queue Home available and
		// communicates exactly that no remote queues are connected yet.
		writeJSON(w, http.StatusOK, map[string]any{"nodes": []any{}})
		return
	}
	if path == "/federation/nodes" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"nodes": []any{}})
		return
	}
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
	if path == "/queue/open-next" && r.Method == http.MethodPost {
		item, err := queueStore.OpenNext(d.db)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "No queued review items"})
			return
		}
		// Opening only changes the durable lifecycle. It never opens a
		// workspace or prompts an ACP agent as a side effect.
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "reviewUrl": "/review?queueItem=" + item.ID})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 && parts[0] == "queue" && r.Method == http.MethodGet {
		item, err := queueStore.Get(d.db, parts[1])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown queue item"})
			return
		}
		feedback, err := queueStore.FeedbackForItem(d.db, item.ID, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		decisions, err := queueStore.DecisionsForItem(d.db, item.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "feedback": feedback, "decisions": decisions})
		return
	}
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost && parts[2] == "reorder" {
		var input struct {
			Position int `json:"position"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid reorder input"})
			return
		}
		item, err := queueStore.Reorder(d.db, parts[1], input.Position)
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
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost && parts[2] == "decision" {
		var input struct {
			Decision string `json:"decision"`
			Body     string `json:"body"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid decision"})
			return
		}
		item, err := queueStore.Decide(d.db, parts[1], queueStore.Status(input.Decision), input.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown queue item"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item})
		return
	}
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost && parts[2] == "feedback" {
		var input queueStore.FeedbackInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid feedback"})
			return
		}
		feedback, err := queueStore.AddFeedback(d.db, parts[1], input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"feedback": feedback})
		return
	}
	if len(parts) == 4 && parts[0] == "queue" && parts[2] == "feedback" && parts[3] == "prompt" && r.Method == http.MethodGet {
		item, err := queueStore.Get(d.db, parts[1])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown queue item"})
			return
		}
		feedback, err := queueStore.FeedbackForItem(d.db, item.ID, r.URL.Query().Get("includeDelivered") != "true")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(queueStore.FeedbackPrompt(*item, feedback, dereference(item.DecisionBody))))
		return
	}
	if len(parts) == 3 && parts[0] == "queue" && parts[2] == "reproduce" && r.Method == http.MethodGet {
		item, err := queueStore.Get(d.db, parts[1])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown queue item"})
			return
		}
		var manifest struct {
			ID    string            `json:"id"`
			Repos []json.RawMessage `json:"repos"`
		}
		hasSnapshot := item.SnapshotManifestPath != nil && len(item.SnapshotManifest) > 0 && json.Unmarshal(item.SnapshotManifest, &manifest) == nil && manifest.ID != ""
		var snapshot any
		if hasSnapshot {
			snapshot = map[string]any{"id": manifest.ID, "manifestPath": *item.SnapshotManifestPath, "repositories": len(manifest.Repos)}
		}
		var existingACP any
		if item.ACPHost != nil && item.ACPPort != nil && item.ACPSessionID != nil {
			existingACP = map[string]any{"host": *item.ACPHost, "port": *item.ACPPort, "sessionId": *item.ACPSessionID, "state": item.ACPState, "error": item.ACPLastError, "canAttemptResume": item.ACPState != "error"}
		}
		var commands any
		if hasSnapshot {
			commands = map[string]string{"reproduceSnapshot": "localreview reproduce " + shellQuote(*item.SnapshotManifestPath) + " <empty-destination>", "reproduceCopilot": "localreview reproduce-copilot " + item.ID + " <empty-destination>", "freshAcp": "cd <empty-destination> && copilot --acp --port 4123"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"itemId": item.ID, "workspacePath": item.WorkspacePath, "snapshot": snapshot, "existingAcp": existingACP, "copilotSessionId": item.CopilotSessionID, "commands": commands, "notes": []string{"Materialization requires an explicit empty destination; viewing this plan never overwrites a workspace.", "A saved ACP endpoint is a live-session hint only; resume works only while that endpoint and session remain live."}})
		return
	}
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

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
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
