// Package queue implements the durable review-stream control plane used by
// Queue Home. It deliberately has no HTTP or ACP dependency: callers can
// safely inspect, copy, or deliver the durable feedback records separately.
package queue

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	Queued           Status = "queued"
	InReview         Status = "in_review"
	ChangesRequested Status = "changes_requested"
	Approved         Status = "approved"
	Completed        Status = "completed"
)

type Item struct {
	ID                   string          `json:"id"`
	Title                string          `json:"title"`
	Body                 string          `json:"body"`
	WorkspacePath        string          `json:"workspacePath"`
	Kind                 string          `json:"kind"`
	RemoteURL            *string         `json:"remoteUrl"`
	Status               Status          `json:"status"`
	Position             int             `json:"position"`
	AgentID              *string         `json:"agentId"`
	AgentProvider        *string         `json:"agentProvider"`
	CopilotSessionID     *string         `json:"copilotSessionId"`
	ACPHost              *string         `json:"acpHost"`
	ACPPort              *int            `json:"acpPort"`
	ACPSessionID         *string         `json:"acpSessionId"`
	AgentKind            *string         `json:"agentKind"`
	ACPState             string          `json:"acpState"`
	ACPLastError         *string         `json:"acpLastError"`
	ACPUpdatedAt         *int64          `json:"acpUpdatedAt"`
	SnapshotManifestPath *string         `json:"snapshotManifestPath"`
	SnapshotManifest     json.RawMessage `json:"snapshotManifest"`
	FeedbackTarget       *string         `json:"feedbackTarget"`
	DecisionBody         *string         `json:"decisionBody"`
	BaseRef              *string         `json:"baseRef"`
	Provenance           json.RawMessage `json:"provenance"`
	SourceFingerprint    *string         `json:"sourceFingerprint"`
	SupersedesID         *string         `json:"supersedesId"`
	IdentityKey          *string         `json:"identityKey"`
	ReviewTopic          *string         `json:"reviewTopic"`
	RemovedAt            *int64          `json:"removedAt"`
	RemovedReason        *string         `json:"removedReason"`
	CreatedAt            int64           `json:"createdAt"`
	UpdatedAt            int64           `json:"updatedAt"`
}

type EnqueueInput struct {
	Title                string          `json:"title"`
	Body                 string          `json:"body"`
	WorkspacePath        string          `json:"workspacePath"`
	Kind                 string          `json:"kind"`
	RemoteURL            string          `json:"remoteUrl"`
	IdempotentKey        string          `json:"idempotentKey"`
	AgentID              string          `json:"agentId"`
	AgentProvider        string          `json:"agentProvider"`
	CopilotSessionID     string          `json:"copilotSessionId"`
	ACPHost              string          `json:"acpHost"`
	ACPPort              int             `json:"acpPort"`
	ACPSessionID         string          `json:"acpSessionId"`
	AgentKind            string          `json:"agentKind"`
	SnapshotManifestPath string          `json:"snapshotManifestPath"`
	SnapshotManifest     json.RawMessage `json:"snapshotManifest"`
	FeedbackTarget       string          `json:"feedbackTarget"`
	BaseRef              string          `json:"baseRef"`
	Provenance           json.RawMessage `json:"provenance"`
	SourceFingerprint    string          `json:"sourceFingerprint"`
	SupersedesID         string          `json:"supersedesId"`
	ReviewTopic          string          `json:"topic"`
	IdentityKey          string          `json:"identityKey"`
}

type Feedback struct {
	ID          int64   `json:"id"`
	QueueItemID string  `json:"queueItemId"`
	Body        string  `json:"body"`
	Path        *string `json:"path"`
	Line        *int    `json:"line"`
	SourceKey   *string `json:"sourceKey"`
	Side        *string `json:"side"`
	EndLine     *int    `json:"endLine"`
	CreatedAt   int64   `json:"createdAt"`
	DeliveredAt *int64  `json:"deliveredAt"`
}
type FeedbackInput struct {
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	SourceKey string `json:"sourceKey"`
	Side      string `json:"side"`
	EndLine   *int   `json:"endLine"`
}
type Decision struct {
	ID          int64   `json:"id"`
	QueueItemID string  `json:"queueItemId"`
	Status      string  `json:"status"`
	Body        *string `json:"body"`
	CreatedAt   int64   `json:"createdAt"`
}

func id() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func identity(in EnqueueInput) string {
	if strings.TrimSpace(in.IdentityKey) != "" {
		return strings.TrimSpace(in.IdentityKey)
	}
	if in.Kind == "remote" && strings.TrimSpace(in.RemoteURL) != "" {
		return "pr:" + strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.RemoteURL), "/"))
	}
	topic := strings.TrimSpace(in.ReviewTopic)
	if topic == "" {
		topic = strings.TrimSpace(in.Title)
	}
	return "local:" + in.WorkspacePath + ":" + strings.ToLower(topic)
}

const columns = `id,title,body,workspace_path,kind,remote_url,status,position,agent_id,agent_provider,copilot_session_id,acp_host,acp_port,acp_session_id,agent_kind,acp_state,acp_last_error,acp_updated_at,snapshot_manifest_path,snapshot_manifest_json,feedback_target,decision_body,base_ref,provenance_json,source_fingerprint,supersedes_id,identity_key,review_topic,removed_at,removed_reason,created_at,updated_at`

func scan(row interface{ Scan(...any) error }) (*Item, error) {
	var item Item
	var status string
	var manifest, provenance *string
	err := row.Scan(&item.ID, &item.Title, &item.Body, &item.WorkspacePath, &item.Kind, &item.RemoteURL, &status, &item.Position,
		&item.AgentID, &item.AgentProvider, &item.CopilotSessionID, &item.ACPHost, &item.ACPPort, &item.ACPSessionID, &item.AgentKind,
		&item.ACPState, &item.ACPLastError, &item.ACPUpdatedAt, &item.SnapshotManifestPath, &manifest, &item.FeedbackTarget, &item.DecisionBody,
		&item.BaseRef, &provenance, &item.SourceFingerprint, &item.SupersedesID, &item.IdentityKey, &item.ReviewTopic, &item.RemovedAt, &item.RemovedReason, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.Status = Status(status)
	if manifest != nil {
		item.SnapshotManifest = json.RawMessage(*manifest)
	}
	if provenance != nil {
		item.Provenance = json.RawMessage(*provenance)
	}
	return &item, nil
}
func Get(db *sql.DB, itemID string) (*Item, error) {
	return scan(db.QueryRow(`SELECT `+columns+` FROM queue_items WHERE id=?`, itemID))
}
func List(db *sql.DB, includeHistory bool) ([]Item, error) {
	where := "removed_at IS NULL"
	if !includeHistory {
		where += " AND status IN ('queued','in_review')"
	}
	rows, err := db.Query(`SELECT ` + columns + ` FROM queue_items WHERE ` + where + ` ORDER BY position,created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if item != nil {
			items = append(items, *item)
		}
	}
	return items, rows.Err()
}
func Enqueue(db *sql.DB, in EnqueueInput) (*Item, bool, error) {
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.WorkspacePath) == "" {
		return nil, false, errors.New("title and workspacePath are required")
	}
	if in.Kind == "" {
		in.Kind = "local"
	}
	if in.Kind != "local" && in.Kind != "remote" {
		return nil, false, errors.New("kind must be local or remote")
	}
	if in.IdempotentKey != "" {
		if existing, err := scan(db.QueryRow(`SELECT `+columns+` FROM queue_items WHERE idempotent_key=?`, in.IdempotentKey)); err != nil {
			return nil, false, err
		} else if existing != nil {
			return existing, false, nil
		}
	}
	key := identity(in)
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	predecessor, err := scan(tx.QueryRow(`SELECT `+columns+` FROM queue_items WHERE identity_key=? ORDER BY created_at DESC LIMIT 1`, key))
	if err != nil {
		return nil, false, err
	}
	// CLI/manual submissions may not have a source fingerprint. In that case
	// the stream identity is the stable de-duplication key; when fingerprints
	// are available, a changed source deliberately creates a new review round.
	unchanged := predecessor != nil && ((predecessor.SourceFingerprint == nil && in.SourceFingerprint == "") || (predecessor.SourceFingerprint != nil && *predecessor.SourceFingerprint == in.SourceFingerprint))
	if predecessor != nil && predecessor.RemovedAt == nil && (predecessor.Status == Queued || predecessor.Status == InReview) && unchanged {
		return predecessor, false, nil
	}
	now := time.Now().UnixMilli()
	if predecessor != nil && predecessor.RemovedAt == nil && (predecessor.Status == Queued || predecessor.Status == InReview) {
		if _, err := tx.Exec(`UPDATE queue_items SET status=?,decision_body=?,updated_at=? WHERE id=?`, Completed, "Superseded by a newer submission for this review stream.", now, predecessor.ID); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`, predecessor.ID, Completed, "Superseded by a newer submission for this review stream.", now); err != nil {
			return nil, false, err
		}
	}
	itemID, err := id()
	if err != nil {
		return nil, false, err
	}
	var position int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM queue_items`).Scan(&position); err != nil {
		return nil, false, err
	}
	supersedes := in.SupersedesID
	if supersedes == "" && predecessor != nil {
		supersedes = predecessor.ID
	}
	acpState := "unavailable"
	var acpUpdated any
	if in.ACPHost != "" && in.ACPPort > 0 && in.ACPSessionID != "" {
		acpState = "idle"
		acpUpdated = now
	}
	_, err = tx.Exec(`INSERT INTO queue_items(id,idempotent_key,title,body,workspace_path,kind,remote_url,status,position,agent_id,agent_provider,copilot_session_id,acp_host,acp_port,acp_session_id,agent_kind,acp_state,acp_updated_at,snapshot_manifest_path,snapshot_manifest_json,feedback_target,base_ref,provenance_json,source_fingerprint,supersedes_id,identity_key,review_topic,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, itemID, nullable(in.IdempotentKey), in.Title, in.Body, in.WorkspacePath, in.Kind, nullable(in.RemoteURL), Queued, position, nullable(in.AgentID), nullable(in.AgentProvider), nullable(in.CopilotSessionID), nullable(in.ACPHost), nullableInt(in.ACPPort), nullable(in.ACPSessionID), nullable(in.AgentKind), acpState, acpUpdated, nullable(in.SnapshotManifestPath), nullableRaw(in.SnapshotManifest), nullable(in.FeedbackTarget), nullable(in.BaseRef), nullableRaw(in.Provenance), nullable(in.SourceFingerprint), nullable(supersedes), key, nullable(in.ReviewTopic), now, now)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	item, err := Get(db, itemID)
	return item, true, err
}
func Open(db *sql.DB, itemID string) (*Item, error) {
	return transition(db, itemID, Queued, InReview, "", false)
}
func OpenNext(db *sql.DB) (*Item, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM queue_items WHERE removed_at IS NULL AND status='queued' ORDER BY position,created_at LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Open(db, id)
}
func Requeue(db *sql.DB, itemID string) (*Item, error) {
	item, err := Get(db, itemID)
	if err != nil || item == nil {
		return item, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var position int
	if err = tx.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM queue_items`).Scan(&position); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	if _, err = tx.Exec(`UPDATE queue_items SET status='queued',position=?,decision_body=NULL,updated_at=? WHERE id=?`, position, now, itemID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`, itemID, "requeued", "Requeued by reviewer.", now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return Get(db, itemID)
}
func Reorder(db *sql.DB, itemID string, position int) (*Item, error) {
	target, err := Get(db, itemID)
	if err != nil || target == nil {
		return target, err
	}
	items, err := List(db, false)
	if err != nil {
		return nil, err
	}
	siblings := make([]Item, 0, len(items))
	for _, item := range items {
		if item.ID != itemID {
			siblings = append(siblings, item)
		}
	}
	if position < 1 {
		position = 1
	}
	if position > len(siblings)+1 {
		position = len(siblings) + 1
	}
	siblings = append(siblings, Item{})
	copy(siblings[position:], siblings[position-1:])
	siblings[position-1] = *target
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for n, item := range siblings {
		if _, err = tx.Exec(`UPDATE queue_items SET position=?,updated_at=? WHERE id=?`, n+1, now, item.ID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return Get(db, itemID)
}
func Decide(db *sql.DB, itemID string, decision Status, body string) (*Item, error) {
	if decision != Approved && decision != ChangesRequested && decision != Completed {
		return nil, errors.New("decision must be approved, changes_requested, or completed")
	}
	return transition(db, itemID, "", decision, body, true)
}
func Complete(db *sql.DB, itemID string, status Status, body string) (*Item, error) {
	return Decide(db, itemID, status, body)
}
func Remove(db *sql.DB, itemID, reason string) (*Item, error) {
	if reason == "" {
		reason = "Removed from queue without review."
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	result, err := tx.Exec(`UPDATE queue_items SET removed_at=?,removed_reason=?,updated_at=? WHERE id=? AND removed_at IS NULL`, now, reason, now, itemID)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	if _, err = tx.Exec(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`, itemID, Completed, reason, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return Get(db, itemID)
}
func transition(db *sql.DB, itemID string, only Status, next Status, body string, record bool) (*Item, error) {
	now := time.Now().UnixMilli()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `UPDATE queue_items SET status=?,decision_body=?,updated_at=? WHERE id=? AND removed_at IS NULL`
	args := []any{next, nullable(body), now, itemID}
	if only != "" {
		query += " AND status=?"
		args = append(args, only)
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	if record {
		if _, err = tx.Exec(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`, itemID, next, nullable(body), now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return Get(db, itemID)
}
func AddFeedback(db *sql.DB, itemID string, in FeedbackInput) ([]Feedback, error) {
	if strings.TrimSpace(in.Body) == "" {
		return nil, errors.New("body is required")
	}
	item, err := Get(db, itemID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, errors.New("unknown queue item")
	}
	now := time.Now().UnixMilli()
	if _, err = db.Exec(`INSERT INTO queue_feedback(queue_item_id,body,path,line,source_key,side,end_line,created_at) VALUES(?,?,?,?,?,?,?,?)`, itemID, in.Body, nullable(in.Path), in.Line, nullable(in.SourceKey), nullable(in.Side), in.EndLine, now); err != nil {
		return nil, err
	}
	if _, err = db.Exec(`UPDATE queue_items SET updated_at=? WHERE id=?`, now, itemID); err != nil {
		return nil, err
	}
	return FeedbackForItem(db, itemID, false)
}
func FeedbackForItem(db *sql.DB, itemID string, undeliveredOnly bool) ([]Feedback, error) {
	query := `SELECT id,queue_item_id,body,path,line,source_key,side,end_line,created_at,delivered_at FROM queue_feedback WHERE queue_item_id=?`
	if undeliveredOnly {
		query += " AND delivered_at IS NULL"
	}
	query += " ORDER BY id"
	rows, err := db.Query(query, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		var f Feedback
		if err = rows.Scan(&f.ID, &f.QueueItemID, &f.Body, &f.Path, &f.Line, &f.SourceKey, &f.Side, &f.EndLine, &f.CreatedAt, &f.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func DecisionsForItem(db *sql.DB, itemID string) ([]Decision, error) {
	rows, err := db.Query(`SELECT id,queue_item_id,status,body,created_at FROM queue_decisions WHERE queue_item_id=? ORDER BY created_at,id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Decision{}
	for rows.Next() {
		var d Decision
		if err = rows.Scan(&d.ID, &d.QueueItemID, &d.Status, &d.Body, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func MarkFeedbackDelivered(db *sql.DB, itemID string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	values := make([]string, len(ids))
	args := make([]any, 0, len(ids)+2)
	now := time.Now().UnixMilli()
	for i, id := range ids {
		values[i] = "?"
		args = append(args, id)
	}
	args = append(args, itemID)
	if _, err := db.Exec(fmt.Sprintf(`UPDATE queue_feedback SET delivered_at=? WHERE id IN (%s) AND queue_item_id=?`, strings.Join(values, ",")), append([]any{now}, args...)...); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE queue_items SET updated_at=? WHERE id=?`, now, itemID)
	return err
}
func FeedbackPrompt(item Item, feedback []Feedback, decisionBody string) string {
	lines := make([]string, 0, len(feedback))
	for _, entry := range feedback {
		prefix := ""
		if entry.Path != nil {
			prefix = *entry.Path
			if entry.Line != nil {
				prefix += fmt.Sprintf(":%d", *entry.Line)
			}
			prefix += ": "
		}
		lines = append(lines, "- "+prefix+entry.Body)
	}
	sections := []string{fmt.Sprintf("Local review feedback for %s.", item.Title), fmt.Sprintf("The review snapshot came from %s. Keep working in your existing session; address the feedback and report what changed.", item.WorkspacePath)}
	if decisionBody != "" {
		sections = append(sections, "Reviewer summary: "+decisionBody)
	}
	if len(lines) > 0 {
		sections = append(sections, "Comments:\n"+strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n")
}
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
func nullableRaw(value json.RawMessage) any {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	return string(value)
}
