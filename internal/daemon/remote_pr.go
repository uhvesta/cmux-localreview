package daemon

// Native remote pull-request lifecycle.  This replaces the old Bun remotePr
// helper with daemon-owned credentials and cache-owned Git worktrees.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/githubauth"
	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
	"github.com/uhvesta/cmux-localreview/internal/remotepr"
	"github.com/uhvesta/cmux-localreview/internal/snapshot"
)

type remoteQueueInput struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Base      string `json:"base"`
	Snapshot  *bool  `json:"snapshot"`
	RemoteURL string `json:"remoteUrl"`
}

func (d *Daemon) remotePRClient() remotepr.Client {
	return remotepr.Client{HTTP: d.github.HTTP, CacheRoot: filepath.Join(d.dataDir, "remote-pr-cache")}
}

func remotePRFromQueueItem(item *queueStore.Item) (remotepr.PullRequest, bool) {
	if item == nil || len(item.SnapshotManifest) == 0 {
		return remotepr.PullRequest{}, false
	}
	var envelope struct {
		PullRequest remotepr.PullRequest `json:"remotePullRequest"`
	}
	if json.Unmarshal(item.SnapshotManifest, &envelope) != nil || envelope.PullRequest.URL == "" || envelope.PullRequest.HeadSHA == "" || envelope.PullRequest.BaseSHA == "" {
		return remotepr.PullRequest{}, false
	}
	return envelope.PullRequest, true
}

func (d *Daemon) openReadOnlyPullRequest(ctx context.Context, remoteURL string) (map[string]any, error) {
	token, err := d.github.Token(ctx, githubauth.Read)
	if err != nil {
		return nil, err
	}
	client := d.remotePRClient()
	pr, err := client.Resolve(ctx, remoteURL, token)
	if err != nil {
		return nil, err
	}
	workspace, err := client.Prepare(ctx, pr, token)
	if err != nil {
		return nil, err
	}
	if _, err := d.activateWorkspaceWithBase(workspace.WorkspacePath, pr.Title, pr.BaseSHA); err != nil {
		return nil, err
	}
	d.mu.Lock()
	review := d.review
	d.mu.Unlock()
	repos := []map[string]string{}
	if review != nil {
		for _, repo := range review.Repos {
			repos = append(repos, map[string]string{"id": repo.ID, "path": repo.WorkspaceRelativePath})
		}
	}
	return map[string]any{"pullRequest": pr, "workspacePath": workspace.WorkspacePath, "repos": repos, "reviewUrl": "/review?localOnly=1"}, nil
}

func (d *Daemon) enqueueRemotePullRequest(ctx context.Context, input queueStore.EnqueueInput, captureSnapshot bool) (*queueStore.Item, bool, error) {
	if strings.TrimSpace(input.RemoteURL) == "" {
		return nil, false, errors.New("remoteUrl is required")
	}
	token, err := d.github.Token(ctx, githubauth.Read)
	if err != nil {
		return nil, false, err
	}
	client := d.remotePRClient()
	pr, err := client.Resolve(ctx, input.RemoteURL, token)
	if err != nil {
		return nil, false, err
	}
	workspace, err := client.Prepare(ctx, pr, token)
	if err != nil {
		return nil, false, err
	}
	input.Kind, input.RemoteURL, input.WorkspacePath = "remote", pr.URL, workspace.WorkspacePath
	input.Title = firstNonEmpty(input.Title, "Review #"+strconv.Itoa(pr.Number)+": "+pr.Title)
	input.Body = firstNonEmpty(input.Body, pr.Body)
	input.BaseRef = firstNonEmpty(input.BaseRef, pr.BaseSHA)
	input.SourceFingerprint = pr.HeadSHA
	if captureSnapshot && input.SnapshotManifestPath == "" {
		manifest, manifestPath, captureErr := snapshot.Capture(workspace.WorkspacePath, filepath.Join(d.dataDir, "artifacts"), input.BaseRef)
		if captureErr != nil {
			return nil, false, captureErr
		}
		encoded, marshalErr := json.Marshal(map[string]any{
			"snapshot":          manifest,
			"remotePullRequest": pr,
			"remoteWorkspace":   map[string]string{"mirrorPath": workspace.MirrorPath, "worktreePath": workspace.WorktreePath},
		})
		if marshalErr != nil {
			return nil, false, marshalErr
		}
		if writeErr := os.WriteFile(manifestPath, append(append([]byte(nil), encoded...), '\n'), 0o600); writeErr != nil {
			return nil, false, writeErr
		}
		input.SnapshotManifestPath, input.SnapshotManifest = manifestPath, encoded
	} else if len(input.SnapshotManifest) == 0 {
		encoded, marshalErr := json.Marshal(map[string]any{"remotePullRequest": pr, "remoteWorkspace": map[string]string{"mirrorPath": workspace.MirrorPath, "worktreePath": workspace.WorktreePath}})
		if marshalErr != nil {
			return nil, false, marshalErr
		}
		input.SnapshotManifest = encoded
	}
	return queueStore.Enqueue(d.db, input)
}

func (d *Daemon) handleRemoteLifecycle(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if len(parts) == 2 && parts[0] == "local-review" && parts[1] == "pr" && r.Method == http.MethodPost {
		var input struct {
			RemoteURL string `json:"remoteUrl"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil || strings.TrimSpace(input.RemoteURL) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "remoteUrl is required"})
			return true
		}
		result, err := d.openReadOnlyPullRequest(r.Context(), input.RemoteURL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return true
		}
		writeJSON(w, http.StatusOK, result)
		return true
	}
	if len(parts) != 3 || parts[0] != "queue" {
		return false
	}
	item, err := queueStore.Get(d.db, parts[1])
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return true
	}
	if item == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown queue item"})
		return true
	}
	switch {
	case parts[2] == "refresh" && r.Method == http.MethodPost:
		if item.Kind != "remote" || item.RemoteURL == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only remote PR queue items can be refreshed"})
			return true
		}
		var override remoteQueueInput
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&override)
		capture := override.Snapshot == nil || *override.Snapshot
		result, created, refreshErr := d.enqueueRemotePullRequest(r.Context(), queueStore.EnqueueInput{Title: firstNonEmpty(override.Title, item.Title), Body: firstNonEmpty(override.Body, item.Body), BaseRef: firstNonEmpty(override.Base, dereference(item.BaseRef)), RemoteURL: *item.RemoteURL, AgentID: dereference(item.AgentID), AgentProvider: dereference(item.AgentProvider), CopilotSessionID: dereference(item.CopilotSessionID), FeedbackTarget: dereference(item.FeedbackTarget), Provenance: item.Provenance}, capture)
		if refreshErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": refreshErr.Error()})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": result, "created": created})
		return true
	case parts[2] == "remote-status" && r.Method == http.MethodGet:
		if item.Kind != "remote" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only remote PR queue items have a managed mirror/worktree"})
			return true
		}
		pr, ok := remotePRFromQueueItem(item)
		if !ok {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "This remote item has no resolved PR metadata. Refresh it to create a managed cache."})
			return true
		}
		paths, pathErr := d.remotePRClient().Paths(pr)
		if pathErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": pathErr.Error()})
			return true
		}
		_, mirrorErr := os.Stat(paths.MirrorPath)
		_, worktreeErr := os.Stat(paths.WorktreePath)
		writeJSON(w, http.StatusOK, map[string]any{"pullRequest": pr, "cache": map[string]any{"mirrorPath": paths.MirrorPath, "mirrorPresent": mirrorErr == nil, "worktreePath": paths.WorktreePath, "worktreePresent": worktreeErr == nil}, "recovery": map[string]string{"refresh": "POST /api/queue/" + item.ID + "/refresh", "cleanup": "POST /api/queue/" + item.ID + "/cleanup", "reAdd": "POST /api/queue with { remoteUrl: " + strconv.Quote(dereference(item.RemoteURL)) + " }"}})
		return true
	case parts[2] == "cleanup" && r.Method == http.MethodPost:
		if item.Kind != "remote" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only remote PR queue items have managed worktrees"})
			return true
		}
		pr, ok := remotePRFromQueueItem(item)
		if !ok {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "This remote item has no resolved PR metadata. Refresh it before cleanup."})
			return true
		}
		var input struct {
			RemoveMirror bool `json:"removeMirror"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input)
		worktreeRemoved, mirrorRemoved, cleanupErr := d.remotePRClient().Cleanup(r.Context(), pr, input.RemoveMirror)
		if cleanupErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": cleanupErr.Error()})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "cleanup": map[string]bool{"worktreeRemoved": worktreeRemoved, "mirrorRemoved": mirrorRemoved}})
		return true
	}
	return false
}
