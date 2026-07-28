package daemon

// Native implementations of the two export boundaries.  The clipboard
// destination intentionally returns the text to the browser/CLI; a daemon
// must never attempt to reach into a user's desktop clipboard.  The cmux
// destination uses cmux's documented local NDJSON socket protocol.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
	"github.com/uhvesta/cmux-localreview/internal/snapshot"
)

const (
	cmuxSocketPathFile = "/tmp/cmux-last-socket-path"
	cmuxFallbackSocket = "/tmp/cmux.sock"
)

func defaultCmuxSocket() string {
	if raw, err := os.ReadFile(cmuxSocketPathFile); err == nil && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw))
	}
	return cmuxFallbackSocket
}

// sendCmuxText performs one small request/response exchange.  Keeping this
// client here avoids retaining any Node dependency in the daemon while making
// the explicit "send to cmux" export action work with the same socket used by
// cmux's own integrations.
func (d *Daemon) sendCmuxText(text string) error {
	path := d.cmuxSocketPath
	if path == "" {
		path = defaultCmuxSocket()
	}
	connection, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	requestID := make([]byte, 8)
	if _, err := rand.Read(requestID); err != nil {
		return err
	}
	id := "localreview-" + hex.EncodeToString(requestID)
	request := map[string]any{"id": id, "method": "surface.send_text", "params": map[string]string{"text": text}}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err = connection.Write(append(encoded, '\n')); err != nil {
		return err
	}
	scanner := bufio.NewScanner(connection)
	for scanner.Scan() {
		var response struct {
			ID    string          `json:"id"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.ID != id {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return fmt.Errorf("cmux rejected send_text: %s", response.Error)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("cmux closed the socket without acknowledging send_text")
}

func sessionForExport(review *workspaceReview, raw string) int64 {
	if review == nil {
		return 0
	}
	if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
		return parsed
	}
	return review.SessionID
}

func (d *Daemon) exportFormalFeedback(sessionID int64, destination string) (string, error) {
	if destination != "clipboard" && destination != "cmux" {
		return "", errors.New("destination must be 'clipboard' or 'cmux'")
	}
	prompt, err := d.exportPrompt(sessionID)
	if err != nil {
		return "", err
	}
	if destination == "cmux" {
		if err := d.sendCmuxText(prompt); err != nil {
			return "", err
		}
	}
	if _, err := d.db.Exec(`INSERT INTO exports (session_id,content,destination,created_at) VALUES(?,?,?,?)`, sessionID, prompt, destination, time.Now().UnixMilli()); err != nil {
		return "", err
	}
	return prompt, nil
}

// reviewPackage is deliberately credential-free.  The selected Item fields
// are copied for reproducibility, while daemon secrets and the source working
// directory itself never leave the retained snapshot package.
type reviewPackage struct {
	Version    int                   `json:"version"`
	ExportedAt string                `json:"exportedAt"`
	QueueItem  reviewPackageItem     `json:"queueItem"`
	Snapshot   snapshot.Manifest     `json:"snapshot"`
	Feedback   []queueStore.Feedback `json:"feedback"`
}

type reviewPackageItem struct {
	ID                string            `json:"id"`
	Title             string            `json:"title"`
	Body              string            `json:"body"`
	Kind              string            `json:"kind"`
	RemoteURL         *string           `json:"remoteUrl"`
	AgentID           *string           `json:"agentId"`
	AgentProvider     *string           `json:"agentProvider"`
	CopilotSessionID  *string           `json:"copilotSessionId"`
	ACPHost           *string           `json:"acpHost"`
	ACPPort           *int              `json:"acpPort"`
	ACPSessionID      *string           `json:"acpSessionId"`
	AgentKind         *string           `json:"agentKind"`
	Status            queueStore.Status `json:"status"`
	DecisionBody      *string           `json:"decisionBody"`
	Provenance        json.RawMessage   `json:"provenance"`
	SourceFingerprint *string           `json:"sourceFingerprint"`
	SupersedesID      *string           `json:"supersedesId"`
}

func packageItem(item *queueStore.Item) reviewPackageItem {
	return reviewPackageItem{ID: item.ID, Title: item.Title, Body: item.Body, Kind: item.Kind, RemoteURL: item.RemoteURL, AgentID: item.AgentID, AgentProvider: item.AgentProvider, CopilotSessionID: item.CopilotSessionID, ACPHost: item.ACPHost, ACPPort: item.ACPPort, ACPSessionID: item.ACPSessionID, AgentKind: item.AgentKind, Status: item.Status, DecisionBody: item.DecisionBody, Provenance: item.Provenance, SourceFingerprint: item.SourceFingerprint, SupersedesID: item.SupersedesID}
}

func exportDestination(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("destination is required")
	}
	output, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(output); err == nil {
		return "", fmt.Errorf("export destination already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if parent, err := os.Stat(filepath.Dir(output)); err != nil || !parent.IsDir() {
		if err != nil {
			return "", err
		}
		return "", errors.New("export destination parent must be a directory")
	}
	return output, nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// exportQueueReviewPackage mirrors the legacy atomic portable package: it
// first verifies the immutable snapshot, copies bundles into a sibling temp
// directory, then renames it into the requested destination.
func (d *Daemon) exportQueueReviewPackage(item *queueStore.Item, destination string) (string, error) {
	if item == nil {
		return "", errors.New("unknown queue item")
	}
	if item.SnapshotManifestPath == nil || strings.TrimSpace(*item.SnapshotManifestPath) == "" {
		return "", errors.New("This queue item has no retained snapshot to export")
	}
	manifest, err := snapshot.ReadVerified(*item.SnapshotManifestPath)
	if err != nil {
		return "", err
	}
	output, err := exportDestination(destination)
	if err != nil {
		return "", err
	}
	feedback, err := queueStore.FeedbackForItem(d.db, item.ID, false)
	if err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(output), "."+filepath.Base(output)+".tmp-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if err = os.Chmod(temporary, 0o700); err != nil {
		return "", err
	}
	sourceDir := filepath.Dir(*item.SnapshotManifestPath)
	for _, repo := range manifest.Repos {
		if err = copyFile(filepath.Join(sourceDir, repo.Bundle), filepath.Join(temporary, repo.Bundle)); err != nil {
			return "", err
		}
	}
	portable := manifest
	portable.WorkspacePath = "."
	manifestJSON, _ := json.MarshalIndent(portable, "", "  ")
	if err = os.WriteFile(filepath.Join(temporary, "snapshot-manifest.json"), append(manifestJSON, '\n'), 0o600); err != nil {
		return "", err
	}
	packageJSON, _ := json.MarshalIndent(reviewPackage{Version: 2, ExportedAt: time.Now().UTC().Format(time.RFC3339Nano), QueueItem: packageItem(item), Snapshot: portable, Feedback: feedback}, "", "  ")
	if err = os.WriteFile(filepath.Join(temporary, "review-package.json"), append(packageJSON, '\n'), 0o600); err != nil {
		return "", err
	}
	if err = os.Rename(temporary, output); err != nil {
		return "", err
	}
	return output, nil
}
