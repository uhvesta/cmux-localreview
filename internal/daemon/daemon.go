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
	"io"
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
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/federation"
	"github.com/uhvesta/cmux-localreview/internal/gitdiff"
	"github.com/uhvesta/cmux-localreview/internal/githubauth"
	"github.com/uhvesta/cmux-localreview/internal/githubreview"
	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
	"github.com/uhvesta/cmux-localreview/internal/store"
	"github.com/uhvesta/cmux-localreview/internal/webassets"
	"github.com/uhvesta/cmux-localreview/internal/wshub"
)

const Version = "0.3.0-go-migration"

type Options struct {
	Port       int
	DataDir    string
	UIDir      string
	GitHubAuth *githubauth.ServiceClient
	// AskRuntime is an already constructed runtime, primarily for deterministic
	// embedding/tests. It is never constructed by a read/reload route.
	AskRuntime *askruntime.Runtime
	// AskRuntimeFactory builds the official SDK runtime lazily on the first
	// explicit prompt. It owns the dedicated Copilot credential boundary.
	AskRuntimeFactory *AskRuntimeFactory
	// CmuxSocketPath is optional and is primarily useful for a controlled
	// local cmux installation or deterministic integration tests.  An empty
	// value follows cmux's standard discovery-file/fallback convention.
	CmuxSocketPath string
	// FederationDialer can be replaced only by an embedding/test host. The
	// production default is an OpenSSH loopback forwarder.
	FederationDialer FederationDialer
	// FederationSecrets stores remote daemon capabilities under a separate
	// service/account from GitHub credentials. Nil uses the platform store.
	FederationSecrets githubauth.SecretStore
}

type discovery struct {
	Port      int    `json:"port"`
	Token     string `json:"token"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	CreatedAt string `json:"createdAt"`
}

type Daemon struct {
	listener          net.Listener
	server            *http.Server
	dataDir           string
	token             string
	mu                sync.Mutex
	sessions          map[string]time.Time
	db                *sql.DB
	review            *workspaceReview
	github            *githubauth.ServiceClient
	ws                *wshub.Hub
	watchStop         context.CancelFunc
	watchMu           sync.Mutex
	watches           map[chan string]struct{}
	queueWatchMu      sync.Mutex
	queueWatches      map[string]context.CancelFunc
	authMu            sync.Mutex
	authFlows         map[githubauth.Capability]*githubauth.LoopbackFlow
	askMu             sync.Mutex
	askRuntime        *askruntime.Runtime
	askClose          func() error
	askFactory        *AskRuntimeFactory
	askWatchers       map[string]map[chan askruntime.Delta]struct{}
	queueDeliveryMu   sync.Mutex
	queueDeliveries   map[string]struct{}
	cmuxSocketPath    string
	federation        *federationTransport
	federationSecrets githubauth.SecretStore
}

func browserOpener(rawURL string) error {
	command := "open"
	if runtime.GOOS == "linux" {
		command = "xdg-open"
	}
	return exec.Command(command, rawURL).Start()
}

func githubCapability(raw string) (githubauth.Capability, bool) {
	value := githubauth.Capability(raw)
	switch value {
	case githubauth.Read, githubauth.Write, githubauth.Copilot:
		return value, true
	default:
		return "", false
	}
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
	// Base is a remote PR's immutable base SHA. Empty means the ordinary
	// working-tree comparison selected by the reviewer UI.
	Base string
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
			// This is an API response, including when no capability was supplied.
			// Keep the frozen TypeScript JSON media contract rather than letting
			// http.Error silently downgrade it to text/plain.
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "A local browser session or daemon capability is required"})
			return
		}
		if !headerOK && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			expectedOrigin := "http://" + r.Host
			fetchSite := r.Header.Get("Sec-Fetch-Site")
			if r.Header.Get("Origin") != expectedOrigin || fetchSite == "cross-site" || fetchSite == "same-site" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "Browser mutations must originate from this daemon's exact loopback origin"})
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

// activateWorkspace is the single stateful operation behind an explicit
// workspace open. Queue opening calls it as well: changing queue lifecycle
// alone is not enough for the reviewer UI to resolve /api/repos.
func (d *Daemon) activateWorkspace(workspace, label string) ([]reviewRepo, error) {
	return d.activateWorkspaceWithBase(workspace, label, "")
}

func (d *Daemon) activateWorkspaceWithBase(workspace, label, base string) ([]reviewRepo, error) {
	workspace, err := safeWorkspacePath(workspace)
	if err != nil {
		return nil, err
	}
	repos, err := discoverReviewRepos(workspace)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE workspace_registry SET active=0`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO workspace_registry(root_path,label,last_opened_at,active) VALUES(?,?,?,1) ON CONFLICT(root_path) DO UPDATE SET label=COALESCE(excluded.label,label),last_opened_at=excluded.last_opened_at,active=1`, workspace, nullable(label), now); err != nil {
		return nil, err
	}
	result, err := tx.Exec(`INSERT INTO sessions(label,started_at) VALUES(?,?)`, nullable(label), now)
	if err != nil {
		return nil, err
	}
	sessionID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	for index := range repos {
		if _, err = tx.Exec(`INSERT INTO repos(workspace_relative_path,git_dir,created_at) VALUES(?,?,?) ON CONFLICT(git_dir) DO UPDATE SET workspace_relative_path=excluded.workspace_relative_path`, repos[index].WorkspaceRelativePath, repos[index].AbsolutePath, now); err != nil {
			return nil, err
		}
		if err = tx.QueryRow(`SELECT id FROM repos WHERE git_dir=?`, repos[index].AbsolutePath).Scan(&repos[index].DBID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.review = &workspaceReview{Root: workspace, SessionID: sessionID, Repos: repos, Base: base}
	d.mu.Unlock()
	d.startDiffWatcher(repos)
	return repos, nil
}

// restoreActiveWorkspace rebuilds the in-memory reviewer projection after a
// daemon restart without creating a new review session.  Comments, /ask
// transcripts, and queue provenance are keyed to that durable session, so
// calling activateWorkspace here would silently make a restart look like a
// new review round. A missing/moved workspace is deliberately non-fatal: Queue
// Home remains usable and tells the user to open an available item instead.
func (d *Daemon) restoreActiveWorkspace() error {
	var workspace string
	var label sql.NullString
	err := d.db.QueryRow(`SELECT root_path,label FROM workspace_registry WHERE active=1 ORDER BY last_opened_at DESC LIMIT 1`).Scan(&workspace, &label)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	workspace, err = safeWorkspacePath(workspace)
	if err != nil {
		return err
	}
	repos, err := discoverReviewRepos(workspace)
	if err != nil {
		return err
	}
	for index := range repos {
		if err := d.db.QueryRow(`SELECT id FROM repos WHERE git_dir=?`, repos[index].AbsolutePath).Scan(&repos[index].DBID); err != nil {
			return err
		}
	}
	var sessionID int64
	if err := d.db.QueryRow(`SELECT id FROM sessions ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&sessionID); err != nil {
		return err
	}
	d.mu.Lock()
	d.review = &workspaceReview{Root: workspace, SessionID: sessionID, Repos: repos}
	d.mu.Unlock()
	d.startDiffWatcher(repos)
	return nil
}

// startDiffWatcher treats the Git working tree as the source of truth for a
// rendered review.  It never pushes a diff (or any prompt) to Copilot: the
// browser simply receives an invalidation and decides whether to refetch.
// Polling is deliberate here—Git state can change through IDEs, hooks, and
// remote worktrees, none of which are reliably covered by filesystem events.
func (d *Daemon) startDiffWatcher(repos []reviewRepo) {
	d.mu.Lock()
	if d.watchStop != nil {
		d.watchStop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.watchStop = cancel
	d.mu.Unlock()

	go func() {
		fingerprints := make(map[string]string, len(repos))
		for _, repo := range repos {
			value, err := repoFingerprint(repo.AbsolutePath)
			if err == nil {
				fingerprints[repo.ID] = value
			}
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, repo := range repos {
					value, err := repoFingerprint(repo.AbsolutePath)
					if err != nil || value == fingerprints[repo.ID] {
						continue
					}
					fingerprints[repo.ID] = value
					d.ws.BroadcastDiffUpdated(repo.ID)
					d.broadcastWatch("reload")
				}
			}
		}
	}()
}

// repoFingerprint includes the diff payload, not only porcelain status. A
// tracked file remains `M` while its content changes, which is precisely the
// change a reviewer needs to reload. The output is never persisted or sent to
// a browser; it is used only as a watcher comparison value.
func repoFingerprint(path string) (string, error) {
	status, err := gitOutput(path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	diff, err := gitOutput(path, "diff", "--no-ext-diff", "--binary")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(status + "\x00" + diff))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func (d *Daemon) broadcastWatch(kind string) {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	for watcher := range d.watches {
		select {
		case watcher <- kind:
		default:
		}
	}
}

func (d *Daemon) serveWatch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	watcher := make(chan string, 4)
	d.watchMu.Lock()
	d.watches[watcher] = struct{}{}
	d.watchMu.Unlock()
	defer func() {
		d.watchMu.Lock()
		delete(d.watches, watcher)
		d.watchMu.Unlock()
	}()
	_, _ = fmt.Fprint(w, "data: {\"type\":\"connected\",\"diffMode\":\"default\"}\n\n")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case kind := <-watcher:
			_, _ = fmt.Fprintf(w, "data: {\"type\":%q,\"changeType\":\"git\"}\n\n", kind)
			flusher.Flush()
		}
	}
}

// apiHandler ports the queue control plane first. Unported routes fail
// explicitly; they never fall back to a Node process behind the caller.
func (d *Daemon) apiHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api")
	remoteParts := strings.Split(strings.Trim(path, "/"), "/")
	if d.handleRemoteLifecycle(w, r, remoteParts) {
		return
	}
	if path == "/watch" && r.Method == http.MethodGet {
		d.serveWatch(w, r)
		return
	}
	if strings.HasPrefix(path, "/ask/") {
		if d.handleAsk(w, r, path) {
			return
		}
	}
	if strings.HasPrefix(path, "/btw/") {
		if d.handleBtw(w, r, path) {
			return
		}
	}
	if path == "/github/auth/status" && r.Method == http.MethodGet {
		status, err := (githubauth.API{Service: d.github}).Status(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "GitHub authentication status unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	if path == "/github/auth/configure" && r.Method == http.MethodPost {
		var input githubauth.ConfigureRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid GitHub App configuration"})
			return
		}
		if _, ok := githubCapability(string(input.Capability)); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unknown GitHub App capability"})
			return
		}
		if err := (githubauth.API{Service: d.github}).Configure(r.Context(), input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.HasPrefix(path, "/github/auth/") {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 4 && parts[0] == "github" && parts[1] == "auth" {
			capability, ok := githubCapability(parts[2])
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unknown GitHub App capability"})
				return
			}
			switch {
			case parts[3] == "start" && r.Method == http.MethodPost:
				var input struct {
					Flow string `json:"flow"`
				}
				_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input)
				// A loopback OAuth listener must survive this short-lived API request
				// until GitHub redirects the browser back. Daemon shutdown owns the
				// process lifetime; request cancellation must not cancel login.
				start, flow, err := (githubauth.API{Service: d.github}).Start(context.Background(), githubauth.StartRequest{Capability: capability, Flow: input.Flow})
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
				if flow != nil {
					d.authMu.Lock()
					if prior := d.authFlows[capability]; prior != nil {
						prior.Close()
					}
					d.authFlows[capability] = flow
					d.authMu.Unlock()
					go func(capability githubauth.Capability, flow *githubauth.LoopbackFlow) {
						_ = flow.Wait(context.Background())
						d.authMu.Lock()
						if d.authFlows[capability] == flow {
							delete(d.authFlows, capability)
						}
						d.authMu.Unlock()
					}(capability, flow)
				}
				writeJSON(w, http.StatusAccepted, start)
				return
			case parts[3] == "poll" && r.Method == http.MethodPost:
				status, err := (githubauth.API{Service: d.github}).Poll(r.Context(), capability)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, status)
				return
			}
		}
		if len(parts) == 3 && parts[0] == "github" && parts[1] == "auth" && r.Method == http.MethodDelete {
			capability, ok := githubCapability(parts[2])
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unknown GitHub App capability"})
				return
			}
			if err := (githubauth.API{Service: d.github}).Logout(r.Context(), capability); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "GitHub authentication disconnect failed"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
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
		// `path` is retained for the frozen reviewer bootstrap contract while
		// `workspaceRelativePath` is the clearer native field used by Queue Home.
		// Returning both makes activation forward-compatible without forcing the
		// built UI to infer a path from an implementation detail.
		responseRepos := make([]map[string]any, 0, len(repos))
		for _, repo := range repos {
			responseRepos = append(responseRepos, map[string]any{"id": repo.ID, "path": repo.WorkspaceRelativePath, "workspaceRelativePath": repo.WorkspaceRelativePath})
		}
		writeJSON(w, http.StatusOK, map[string]any{"workspacePath": workspace, "repos": responseRepos, "reviewUrl": "/review"})
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
			files := []string{}
			if diff, err := gitdiff.Parse(repo.AbsolutePath, gitdiff.Selection{}); err == nil {
				for _, file := range diff.Files {
					files = append(files, file.Path)
				}
			}
			var remoteURL any
			if output, err := exec.Command("git", "-C", repo.AbsolutePath, "remote", "get-url", "origin").Output(); err == nil {
				if value := strings.TrimSpace(string(output)); value != "" {
					remoteURL = value
				}
			}
			items = append(items, map[string]any{"id": repo.ID, "workspaceRelativePath": repo.WorkspaceRelativePath, "remoteUrl": remoteURL, "changeCount": len(files), "files": files})
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
	if path == "/export" && r.Method == http.MethodPost {
		d.mu.Lock()
		review := d.review
		d.mu.Unlock()
		if review == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "No workspace is open"})
			return
		}
		var input struct {
			Destination string `json:"destination"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid export input"})
			return
		}
		sessionID := sessionForExport(review, r.URL.Query().Get("sessionId"))
		content, err := d.exportFormalFeedback(sessionID, input.Destination)
		if err != nil {
			status := http.StatusBadRequest
			if input.Destination == "cmux" {
				status = http.StatusBadGateway
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "content": content})
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
			var commentCount int
			if err := d.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE session_id=?`, id).Scan(&commentCount); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			// /btw is SDK-native and no longer persists legacy ACP thread rows, but
			// retaining the zero count keeps older reviewer clients from treating a
			// missing key as an unknown session state.
			sessions = append(sessions, map[string]any{"id": id, "label": nullableString(label), "startedAt": started, "frozenAt": func() any {
				if frozen.Valid {
					return frozen.Int64
				}
				return nil
			}(), "commentCount": commentCount, "btwThreadCount": 0})
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
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "comment-imports" && r.Method == http.MethodPost {
			review, repo, ok := d.reviewContext(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			var raw json.RawMessage
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&raw); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid comment import data"})
				return
			}
			var entries []commentImport
			if len(raw) > 0 && raw[0] == '[' {
				_ = json.Unmarshal(raw, &entries)
			} else {
				var entry commentImport
				if json.Unmarshal(raw, &entry) == nil {
					entries = []commentImport{entry}
				}
			}
			if len(entries) == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid comment import data"})
				return
			}
			changed, warnings, err := d.importComments(&review, repo, entries)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "changed": changed > 0, "count": len(entries), "warnings": warnings})
			return
		}
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
			// Blobs are source bytes, not rendered text.  Retain the frozen API's
			// byte-oriented media type so callers do not accidentally charset-decode
			// binary or non-UTF-8 source files.
			w.Header().Set("Content-Type", "application/octet-stream")
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
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && (parts[3] == "comments-json" || parts[3] == "comments") && r.Method == http.MethodGet {
			review, repo, ok := d.reviewContext(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			threads, err := d.commentThreads(review, repo)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			version, err := readCommentRevision(d.db, review.SessionID, repo.DBID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"version": version, "threads": threads})
			return
		}
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "comments" && r.Method == http.MethodPost {
			review, repo, ok := d.reviewContext(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			var payload struct {
				BaseVersion *int `json:"baseVersion"`
				Threads     []struct {
					ID       string `json:"id"`
					FilePath string `json:"filePath"`
					Channel  string `json:"channel"`
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
			version, stale, err := advanceCommentRevision(tx, review.SessionID, repo.DBID, payload.BaseVersion)
			if err != nil {
				_ = tx.Rollback()
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if stale {
				_ = tx.Rollback()
				threads, listErr := d.commentThreads(review, repo)
				if listErr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": listErr.Error()})
					return
				}
				writeJSON(w, http.StatusConflict, map[string]any{"error": "Comments changed in another request; refresh the local state.", "merged": true, "version": version, "threads": threads})
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
				var messages []durableCommentMessage
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
				channel := commentChannel(thread.Channel, messages)
				_, err = tx.Exec(`INSERT INTO comments(session_id,repo_id,file_path,side,start_line,end_line,body,anchor_content_hash,created_at,updated_at,thread_id,messages_json,anchor_content,channel) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,repo_id,thread_id) DO UPDATE SET file_path=excluded.file_path,side=excluded.side,start_line=excluded.start_line,end_line=excluded.end_line,body=excluded.body,anchor_content_hash=excluded.anchor_content_hash,updated_at=excluded.updated_at,messages_json=excluded.messages_json,anchor_content=excluded.anchor_content,channel=excluded.channel`, review.SessionID, repo.DBID, thread.FilePath, thread.Position.Side, start, end, body, fmt.Sprintf("%x", hash[:]), now, now, thread.ID, string(thread.Messages), nullable(thread.CodeSnapshot.Content), channel)
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
			threads, err := d.commentThreads(review, repo)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "merged": false, "version": version, "threads": threads})
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
			version, stale, err := advanceCommentRevision(tx, review.SessionID, repo.DBID, nil)
			if err != nil || stale {
				_ = tx.Rollback()
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to advance comment revision"})
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
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": deleted > 0, "threadId": threadID, "version": version})
			return
		}
		if len(parts) == 4 && parts[0] == "repos" && parts[2] == "api" && parts[3] == "diff" && r.Method == http.MethodGet {
			repo, ok := d.reviewRepo(parts[1])
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown review repository"})
				return
			}
			base := r.URL.Query().Get("base")
			if base == "" {
				d.mu.Lock()
				if d.review != nil {
					base = d.review.Base
				}
				d.mu.Unlock()
			}
			selection := gitdiff.Selection{BaseCommitish: base, TargetCommitish: r.URL.Query().Get("target"), IgnoreWhitespace: r.URL.Query().Get("ignoreWhitespace") == "true"}
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
			for index := range response.Files {
				contents, readErr := readRepoFile(repo, response.Files[index].Path, response.TargetCommitish)
				if readErr == nil {
					response.Files[index].IsGenerated, _ = generatedStatus(response.Files[index].Path, contents)
				}
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
	if d.handleFederation(w, r, path) {
		return
	}
	if strings.HasPrefix(path, "/agents/") {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 3 && parts[0] == "agents" && r.Method == http.MethodPost {
			id, operation := parts[1], parts[2]
			switch operation {
			case "heartbeat":
				var input agent.Record
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent heartbeat"})
					return
				}
				item, err := agent.Heartbeat(d.db, id, input)
				if errors.Is(err, sql.ErrNoRows) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown agent"})
					return
				}
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"agent": item})
				return
			case "reconnect":
				// Consume a small JSON body for API compatibility (including the
				// legacy dryRun flag). Native reconnect is a safe rendezvous: it
				// never attempts terminal keystroke injection.
				var ignored struct {
					DryRun bool `json:"dryRun"`
				}
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ignored); err != nil && !errors.Is(err, io.EOF) {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid reconnect request"})
					return
				}
				item, err := agent.Reconnect(d.db, id)
				if errors.Is(err, sql.ErrNoRows) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown agent"})
					return
				}
				if err != nil {
					writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"agent": item})
				return
			}
		}
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
			var request struct {
				queueStore.EnqueueInput
				Snapshot *bool `json:"snapshot"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid queue item"})
				return
			}
			input := request.EnqueueInput
			var item *queueStore.Item
			var created bool
			var err error
			if strings.TrimSpace(input.RemoteURL) != "" || input.Kind == "remote" {
				item, created, err = d.enqueueRemotePullRequest(r.Context(), input, request.Snapshot == nil || *request.Snapshot)
			} else {
				item, created, err = queueStore.Enqueue(d.db, input)
			}
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
	if path == "/queue/watch" && r.Method == http.MethodPost {
		d.handleQueueWatch(w, r)
		return
	}
	if path == "/queue/hook" && r.Method == http.MethodPost {
		d.handleQueueHook(w, r)
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
		// Queue contracts also represent remote/unavailable workspaces. Keep the
		// lifecycle transition durable in that case; a local existing path is
		// activated for the reviewer UI below.
		if _, err := os.Stat(item.WorkspacePath); err == nil {
			if _, err := d.activateWorkspace(item.WorkspacePath, item.Title); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}
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
	// Publish a non-blocking GitHub COMMENT review without changing the local
	// lifecycle state. This is useful for a reviewer who wants to publish
	// collected inline feedback while keeping the queue item open.
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost && parts[2] == "publish-comment" {
		var input struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid publish-comment input"})
			return
		}
		item, err := queueStore.Get(d.db, parts[1])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil || item.RemovedAt != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown or removed queue item"})
			return
		}
		if item.Kind != "remote" || item.RemoteURL == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "GitHub comments can only be published for a remote pull-request queue item.", "code": "github_review_publish_not_remote", "localDecisionSaved": false})
			return
		}
		remoteReview, publishErr := d.publishRemoteReview(r.Context(), item, githubreview.Comment, input.Body)
		if publishErr != nil {
			status, code := http.StatusBadGateway, "github_review_publish_failed"
			var stale *githubreview.StaleHeadError
			if errors.As(publishErr, &stale) {
				status, code = http.StatusConflict, "github_review_publish_stale_head"
			}
			writeJSON(w, status, map[string]any{"error": publishErr.Error(), "code": code, "published": false, "localDecisionSaved": false, "item": item})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "remoteReview": remoteReview, "published": true, "localDecisionSaved": false})
		return
	}
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost && parts[2] == "decision" {
		var input struct {
			Decision string `json:"decision"`
			Body     string `json:"body"`
			// Publish is deliberately opt-in.  A remote queue item is still a
			// useful local review artifact without GitHub write authority, so it
			// must never turn a requested publish into a misleading local-only
			// success.
			Publish bool `json:"publish"`
			// PublishGitHub is the frozen TypeScript spelling. Keep it as a
			// compatibility alias while the native UI uses the shorter Publish.
			PublishGitHub bool `json:"publishGitHub"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid decision"})
			return
		}
		if input.Publish || input.PublishGitHub {
			item, getErr := queueStore.Get(d.db, parts[1])
			if getErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": getErr.Error()})
				return
			}
			if item == nil || item.RemovedAt != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown or removed queue item"})
				return
			}
			if item.Kind != "remote" || item.RemoteURL == nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":              "GitHub publishing can only be requested for a remote pull-request queue item. No local decision was saved.",
					"code":               "github_review_publish_not_remote",
					"publishRequested":   true,
					"localDecisionSaved": false,
				})
				return
			}
			if input.Decision != string(queueStore.Approved) && input.Decision != string(queueStore.ChangesRequested) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Only approve or request changes can be published to GitHub. No local decision was saved.", "code": "github_review_publish_invalid_decision", "publishRequested": true, "localDecisionSaved": false})
				return
			}
			kind := githubreview.Approve
			if input.Decision == string(queueStore.ChangesRequested) {
				kind = githubreview.RequestChanges
			}
			remoteReview, publishErr := d.publishRemoteReview(r.Context(), item, kind, input.Body)
			if publishErr != nil {
				status, code := http.StatusBadGateway, "github_review_publish_failed"
				var stale *githubreview.StaleHeadError
				if errors.As(publishErr, &stale) {
					status, code = http.StatusConflict, "github_review_publish_stale_head"
				}
				writeJSON(w, status, map[string]any{"error": publishErr.Error(), "code": code, "publishRequested": true, "localDecisionSaved": false, "item": item})
				return
			}
			updated, decideErr := queueStore.Decide(d.db, parts[1], queueStore.Status(input.Decision), input.Body)
			if decideErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": decideErr.Error()})
				return
			}
			if updated == nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "Queue item changed before its published decision could be saved; GitHub review was published, refresh Queue Home."})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"item": updated, "remoteReview": remoteReview, "published": true, "localDecisionSaved": true})
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
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost && parts[2] == "export" {
		item, err := queueStore.Get(d.db, parts[1])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if item == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown queue item"})
			return
		}
		var input struct {
			Destination string `json:"destination"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid review package export input"})
			return
		}
		packagePath, err := d.exportQueueReviewPackage(item, input.Destination)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"packagePath": packagePath})
		return
	}
	if len(parts) == 3 && parts[0] == "queue" && r.Method == http.MethodPost && parts[2] == "deliver-feedback" {
		d.handleQueueFeedbackDelivery(w, r, parts[1])
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
		var commands any
		if hasSnapshot {
			commands = map[string]string{"reproduceSnapshot": "localreview reproduce " + shellQuote(*item.SnapshotManifestPath) + " <empty-destination>", "openReviewer": "localreview open <empty-destination>"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"itemId": item.ID, "workspacePath": item.WorkspacePath, "snapshot": snapshot, "copilotSessionId": item.CopilotSessionID, "commands": commands, "notes": []string{"Materialization requires an explicit empty destination; viewing this plan never overwrites a workspace.", "Opening the reproduced workspace starts a fresh SDK-native /ask conversation. Historic transcripts remain readable, but remote or ACP sessions are never resumed."}})
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
		if parts[2] == "open" {
			if _, err := os.Stat(item.WorkspacePath); err == nil {
				if _, err := d.activateWorkspace(item.WorkspacePath, item.Title); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"item": item, "reviewUrl": "/review?queueItem=" + item.ID})
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
		return webassets.Handler()
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
	github := options.GitHubAuth
	if github == nil {
		secrets, secretErr := githubauth.NewOSSecretStore()
		if secretErr != nil {
			_ = listener.Close()
			_ = db.Close()
			return nil, secretErr
		}
		github = githubauth.New(secrets, githubauth.NewFileConfigStore(filepath.Join(dir, "github-apps.json")), http.DefaultClient, browserOpener)
	}
	// The native daemon always owns a real SDK runtime factory in production.
	// It remains lazy: no credential is read and no Copilot child process is
	// started until the reviewer explicitly loads models or sends a turn. Tests
	// and embedders can still inject a deterministic runtime/factory.
	askFactory := options.AskRuntimeFactory
	if options.AskRuntime == nil && askFactory == nil {
		copilotHome := filepath.Join(dir, "copilot-sdk")
		if err := os.MkdirAll(copilotHome, 0o700); err != nil {
			_ = listener.Close()
			_ = db.Close()
			return nil, err
		}
		askFactory = NewProductionAskRuntimeFactory(github, dir)
	}
	federationSecrets := options.FederationSecrets
	if federationSecrets == nil {
		federationSecrets = github.Secrets
	}
	if err := federation.MigrateLegacyTokens(db, func(id, token string) error {
		return federationSecrets.Set(federationSecretService, federationSecretAccount(id), token)
	}); err != nil {
		_ = listener.Close()
		_ = db.Close()
		return nil, fmt.Errorf("migrate federation credentials to system secret store: %w", err)
	}
	d := &Daemon{listener: listener, dataDir: dir, token: token, sessions: make(map[string]time.Time), watches: make(map[chan string]struct{}), queueWatches: make(map[string]context.CancelFunc), authFlows: make(map[githubauth.Capability]*githubauth.LoopbackFlow), askRuntime: options.AskRuntime, askFactory: askFactory, askWatchers: make(map[string]map[chan askruntime.Delta]struct{}), queueDeliveries: make(map[string]struct{}), cmuxSocketPath: options.CmuxSocketPath, federation: newFederationTransport(options.FederationDialer), federationSecrets: federationSecrets, db: db, github: github, ws: wshub.New(wshub.Options{Path: "/ws"})}
	if err := d.restoreActiveWorkspace(); err != nil {
		// Persisted active workspace state is best-effort at startup. An absent
		// worktree must not prevent Queue Home from recovering it or opening a
		// different queue item.
		fmt.Fprintln(os.Stderr, "localreviewd: restore active workspace:", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version, "pid": os.Getpid(), "runtime": runtime.Version()})
	})
	mux.HandleFunc("/api/browser/session", d.sessionExchange)
	mux.Handle("/ws", d.ws)
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
	d.resumeQueueWatchers()
	go func() { <-ctx.Done(); _ = d.Close() }()
	return d, nil
}

func (d *Daemon) Port() int { return d.listener.Addr().(*net.TCPAddr).Port }

func (d *Daemon) Close() error {
	if d.server == nil {
		return nil
	}
	d.mu.Lock()
	if d.watchStop != nil {
		d.watchStop()
		d.watchStop = nil
	}
	d.mu.Unlock()
	d.stopAllQueueWatchers()
	if d.federation != nil {
		d.federation.close()
	}
	d.authMu.Lock()
	for capability, flow := range d.authFlows {
		flow.Close()
		delete(d.authFlows, capability)
	}
	d.authMu.Unlock()
	err := error(nil)
	d.askMu.Lock()
	askClose := d.askClose
	d.askClose = nil
	d.askRuntime = nil
	d.askWatchers = make(map[string]map[chan askruntime.Delta]struct{})
	d.askMu.Unlock()
	if askClose != nil {
		if closeErr := askClose(); err == nil {
			err = closeErr
		}
	}
	d.ws.Close()
	if shutdownErr := d.server.Shutdown(context.Background()); err == nil {
		err = shutdownErr
	}
	if d.db != nil {
		if closeErr := d.db.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}
