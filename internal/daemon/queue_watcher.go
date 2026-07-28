package daemon

// Queue watcher endpoints are daemon-owned: they create immutable snapshots
// only when Git state moves, and persist their configuration across restarts.
// They never deliver prompts or review feedback.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
	"github.com/uhvesta/cmux-localreview/internal/snapshot"
)

const (
	minQueueWatchInterval = time.Second
	maxQueueWatchInterval = 10 * time.Minute
)

type queueWatchInput struct {
	WorkspacePath    string          `json:"workspacePath"`
	Path             string          `json:"path"`
	Enabled          *bool           `json:"enabled"`
	PollIntervalMS   int             `json:"pollIntervalMs"`
	Title            string          `json:"title"`
	Body             string          `json:"body"`
	Base             string          `json:"base"`
	AgentID          string          `json:"agentId"`
	AgentProvider    string          `json:"agentProvider"`
	CopilotSessionID string          `json:"copilotSessionId"`
	FeedbackTarget   string          `json:"feedbackTarget"`
	Provenance       json.RawMessage `json:"provenance"`
	LastQueueItemID  string          `json:"lastQueueItemId"`
	LastFingerprint  string          `json:"lastFingerprint"`
}

type queueWatcher struct {
	WorkspacePath    string
	Enabled          bool
	PollIntervalMS   int
	Title            sql.NullString
	Body             sql.NullString
	Base             sql.NullString
	AgentID          sql.NullString
	AgentProvider    sql.NullString
	CopilotSessionID sql.NullString
	FeedbackTarget   sql.NullString
	Provenance       sql.NullString
	LastFingerprint  sql.NullString
	LastQueueItemID  sql.NullString
}

func watcherInterval(value int) time.Duration {
	if value <= 0 {
		return 5 * time.Second
	}
	duration := time.Duration(value) * time.Millisecond
	if duration < minQueueWatchInterval {
		return minQueueWatchInterval
	}
	if duration > maxQueueWatchInterval {
		return maxQueueWatchInterval
	}
	return duration
}

func (d *Daemon) handleQueueWatch(w http.ResponseWriter, r *http.Request) {
	var input queueWatchInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid queue watcher"})
		return
	}
	workspace, err := safeWorkspacePath(firstNonEmpty(input.WorkspacePath, input.Path))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	enabled := input.Enabled == nil || *input.Enabled
	if !enabled {
		if _, err := d.db.Exec(`UPDATE queue_watchers SET enabled=0,updated_at=? WHERE workspace_path=?`, time.Now().UnixMilli(), workspace); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		d.stopQueueWatcher(workspace)
		writeJSON(w, http.StatusOK, map[string]any{"workspacePath": workspace, "enabled": false})
		return
	}
	interval := watcherInterval(input.PollIntervalMS)
	lastFingerprint, lastQueueID := strings.TrimSpace(input.LastFingerprint), strings.TrimSpace(input.LastQueueItemID)
	if lastQueueID != "" {
		item, getErr := queueStore.Get(d.db, lastQueueID)
		if getErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": getErr.Error()})
			return
		}
		if item == nil || item.WorkspacePath != workspace {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lastQueueItemId belongs to another workspace"})
			return
		}
		if item.SourceFingerprint != nil {
			lastFingerprint = *item.SourceFingerprint
		}
	}
	_, err = d.db.Exec(`INSERT INTO queue_watchers(workspace_path,enabled,poll_interval_ms,title,body,base_ref,agent_id,agent_provider,feedback_target,provenance_json,copilot_session_id,last_fingerprint,last_queue_item_id,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_path) DO UPDATE SET enabled=1,poll_interval_ms=excluded.poll_interval_ms,title=COALESCE(excluded.title,queue_watchers.title),body=COALESCE(excluded.body,queue_watchers.body),base_ref=COALESCE(excluded.base_ref,queue_watchers.base_ref),agent_id=COALESCE(excluded.agent_id,queue_watchers.agent_id),agent_provider=COALESCE(excluded.agent_provider,queue_watchers.agent_provider),feedback_target=COALESCE(excluded.feedback_target,queue_watchers.feedback_target),provenance_json=COALESCE(excluded.provenance_json,queue_watchers.provenance_json),copilot_session_id=COALESCE(excluded.copilot_session_id,queue_watchers.copilot_session_id),last_fingerprint=COALESCE(excluded.last_fingerprint,queue_watchers.last_fingerprint),last_queue_item_id=COALESCE(excluded.last_queue_item_id,queue_watchers.last_queue_item_id),updated_at=excluded.updated_at`,
		workspace, 1, int(interval/time.Millisecond), nullable(input.Title), nullable(input.Body), nullable(input.Base), nullable(input.AgentID), nullable(input.AgentProvider), nullable(input.FeedbackTarget), nullableRaw(input.Provenance), nullable(input.CopilotSessionID), nullable(lastFingerprint), nullable(lastQueueID), time.Now().UnixMilli())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	d.startQueueWatcher(workspace, interval)
	writeJSON(w, http.StatusCreated, map[string]any{"workspacePath": workspace, "enabled": true, "pollIntervalMs": int(interval / time.Millisecond)})
}

func (d *Daemon) handleQueueHook(w http.ResponseWriter, r *http.Request) {
	var input queueWatchInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid queue hook"})
		return
	}
	workspace, err := safeWorkspacePath(firstNonEmpty(input.WorkspacePath, input.Path))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, created, err := d.enqueueQueueSnapshot(workspace, input, "hook")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"created": created, "item": item})
}

func (d *Daemon) enqueueQueueSnapshot(workspace string, input queueWatchInput, mechanism string) (*queueStore.Item, bool, error) {
	fingerprint, err := workspaceFingerprint(workspace)
	if err != nil {
		return nil, false, err
	}
	var existingID string
	err = d.db.QueryRow(`SELECT id FROM queue_items WHERE workspace_path=? AND source_fingerprint=? ORDER BY created_at DESC LIMIT 1`, workspace, fingerprint).Scan(&existingID)
	if err == nil {
		item, getErr := queueStore.Get(d.db, existingID)
		return item, false, getErr
	}
	if err != sql.ErrNoRows {
		return nil, false, err
	}
	var priorID string
	var priorStatus queueStore.Status
	err = d.db.QueryRow(`SELECT id,status FROM queue_items WHERE workspace_path=? AND removed_at IS NULL ORDER BY created_at DESC LIMIT 1`, workspace).Scan(&priorID, &priorStatus)
	if err != nil && err != sql.ErrNoRows {
		return nil, false, err
	}
	if err == nil && (priorStatus == queueStore.Queued || priorStatus == queueStore.ChangesRequested) {
		if _, err := queueStore.Decide(d.db, priorID, queueStore.Completed, "Superseded by a newer Git "+mechanism+" snapshot."); err != nil {
			return nil, false, err
		}
	}
	manifest, manifestPath, err := snapshot.Capture(workspace, d.dataDir+"/artifacts", input.Base)
	if err != nil {
		return nil, false, err
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return nil, false, err
	}
	provenance := nullableRaw(input.Provenance)
	if provenance == nil {
		provenance, _ = json.Marshal(map[string]any{"version": 1, "workspacePath": workspace, "autoQueue": map[string]string{"mechanism": mechanism}})
	}
	workspaceHash := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))
	return queueStore.Enqueue(d.db, queueStore.EnqueueInput{Title: firstNonEmpty(input.Title, "Review "+workspace), Body: input.Body, WorkspacePath: workspace, IdempotentKey: mechanism + ":" + workspaceHash + ":" + fingerprint, AgentID: input.AgentID, AgentProvider: input.AgentProvider, CopilotSessionID: input.CopilotSessionID, FeedbackTarget: input.FeedbackTarget, BaseRef: input.Base, Provenance: provenance, SourceFingerprint: fingerprint, SupersedesID: priorID, SnapshotManifestPath: manifestPath, SnapshotManifest: encodedManifest})
}

func (d *Daemon) startQueueWatcher(workspace string, interval time.Duration) {
	d.stopQueueWatcher(workspace)
	ctx, cancel := context.WithCancel(context.Background())
	d.queueWatchMu.Lock()
	d.queueWatches[workspace] = cancel
	d.queueWatchMu.Unlock()
	go func() {
		poll := func() {
			watcher, err := d.loadQueueWatcher(workspace)
			if err != nil || watcher == nil || !watcher.Enabled {
				return
			}
			fingerprint, err := workspaceFingerprint(workspace)
			if err != nil || (watcher.LastFingerprint.Valid && watcher.LastFingerprint.String == fingerprint) {
				return
			}
			item, created, err := d.enqueueQueueSnapshot(workspace, watcher.input(), "git-poll")
			if err != nil || item == nil {
				return
			}
			if created || !watcher.LastFingerprint.Valid {
				_, _ = d.db.Exec(`UPDATE queue_watchers SET last_fingerprint=?,last_queue_item_id=?,updated_at=? WHERE workspace_path=?`, fingerprint, item.ID, time.Now().UnixMilli(), workspace)
			}
		}
		poll()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				poll()
			}
		}
	}()
}

func (d *Daemon) stopQueueWatcher(workspace string) {
	d.queueWatchMu.Lock()
	cancel := d.queueWatches[workspace]
	delete(d.queueWatches, workspace)
	d.queueWatchMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (d *Daemon) stopAllQueueWatchers() {
	d.queueWatchMu.Lock()
	watchers := d.queueWatches
	d.queueWatches = make(map[string]context.CancelFunc)
	d.queueWatchMu.Unlock()
	for _, cancel := range watchers {
		cancel()
	}
}
func (d *Daemon) resumeQueueWatchers() {
	rows, err := d.db.Query(`SELECT workspace_path,poll_interval_ms FROM queue_watchers WHERE enabled=1`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var workspace string
		var interval int
		if rows.Scan(&workspace, &interval) == nil {
			d.startQueueWatcher(workspace, watcherInterval(interval))
		}
	}
}

func (d *Daemon) loadQueueWatcher(workspace string) (*queueWatcher, error) {
	watcher := &queueWatcher{}
	err := d.db.QueryRow(`SELECT workspace_path,enabled,poll_interval_ms,title,body,base_ref,agent_id,agent_provider,copilot_session_id,feedback_target,provenance_json,last_fingerprint,last_queue_item_id FROM queue_watchers WHERE workspace_path=?`, workspace).Scan(&watcher.WorkspacePath, &watcher.Enabled, &watcher.PollIntervalMS, &watcher.Title, &watcher.Body, &watcher.Base, &watcher.AgentID, &watcher.AgentProvider, &watcher.CopilotSessionID, &watcher.FeedbackTarget, &watcher.Provenance, &watcher.LastFingerprint, &watcher.LastQueueItemID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return watcher, err
}
func (w queueWatcher) input() queueWatchInput {
	return queueWatchInput{Title: nullableStringValue(w.Title), Body: nullableStringValue(w.Body), Base: nullableStringValue(w.Base), AgentID: nullableStringValue(w.AgentID), AgentProvider: nullableStringValue(w.AgentProvider), CopilotSessionID: nullableStringValue(w.CopilotSessionID), FeedbackTarget: nullableStringValue(w.FeedbackTarget), Provenance: json.RawMessage(nullableStringValue(w.Provenance))}
}

func workspaceFingerprint(workspace string) (string, error) {
	repos, err := discoverReviewRepos(workspace)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(repos))
	for _, repo := range repos {
		fingerprint, err := repoFingerprint(repo.AbsolutePath)
		if err != nil {
			return "", err
		}
		parts = append(parts, repo.WorkspaceRelativePath+"="+fingerprint)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%x", sum[:]), nil
}
func nullableRaw(value json.RawMessage) []byte {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	return value
}
func nullableStringValue(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
