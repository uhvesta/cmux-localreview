// Package queue implements the durable, review-stream identity semantics used
// by Queue Home. Snapshot/Git acquisition is deliberately outside this small
// store so idempotency can be tested without a checkout.
package queue

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
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
	ID                   string  `json:"id"`
	Title                string  `json:"title"`
	Body                 string  `json:"body"`
	WorkspacePath        string  `json:"workspacePath"`
	Kind                 string  `json:"kind"`
	RemoteURL            *string `json:"remoteUrl"`
	Status               Status  `json:"status"`
	Position             int     `json:"position"`
	IdentityKey          *string `json:"identityKey"`
	ReviewTopic          *string `json:"reviewTopic"`
	SnapshotManifestPath *string `json:"snapshotManifestPath"`
	RemovedAt            *int64  `json:"removedAt"`
	RemovedReason        *string `json:"removedReason"`
	CreatedAt            int64   `json:"createdAt"`
	UpdatedAt            int64   `json:"updatedAt"`
}

type EnqueueInput struct {
	Title                string
	Body                 string
	WorkspacePath        string
	Kind                 string
	RemoteURL            string
	ReviewTopic          string
	IdentityKey          string
	SnapshotManifestPath string
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

func scan(row interface{ Scan(...any) error }) (*Item, error) {
	var item Item
	var status string
	err := row.Scan(&item.ID, &item.Title, &item.Body, &item.WorkspacePath, &item.Kind, &item.RemoteURL, &status, &item.Position, &item.IdentityKey, &item.ReviewTopic, &item.SnapshotManifestPath, &item.RemovedAt, &item.RemovedReason, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.Status = Status(status)
	return &item, nil
}

const columns = `id,title,body,workspace_path,kind,remote_url,status,position,identity_key,review_topic,snapshot_manifest_path,removed_at,removed_reason,created_at,updated_at`

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
	key := identity(in)
	if existing, err := scan(db.QueryRow(`SELECT `+columns+` FROM queue_items WHERE identity_key=? AND removed_at IS NULL AND status IN ('queued','in_review') ORDER BY created_at DESC LIMIT 1`, key)); err != nil {
		return nil, false, err
	} else if existing != nil {
		return existing, false, nil
	}
	itemID, err := id()
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UnixMilli()
	var position int
	if err := db.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM queue_items`).Scan(&position); err != nil {
		return nil, false, err
	}
	var remote any
	if strings.TrimSpace(in.RemoteURL) != "" {
		remote = in.RemoteURL
	}
	var topic any
	if strings.TrimSpace(in.ReviewTopic) != "" {
		topic = in.ReviewTopic
	}
	if _, err := db.Exec(`INSERT INTO queue_items(id,title,body,workspace_path,kind,remote_url,status,position,identity_key,review_topic,snapshot_manifest_path,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, itemID, in.Title, in.Body, in.WorkspacePath, in.Kind, remote, Queued, position, key, topic, nullable(in.SnapshotManifestPath), now, now); err != nil {
		return nil, false, err
	}
	item, err := Get(db, itemID)
	return item, true, err
}

func Open(db *sql.DB, itemID string) (*Item, error) {
	return transition(db, itemID, Queued, InReview, "")
}
func Requeue(db *sql.DB, itemID string) (*Item, error) {
	return transition(db, itemID, "", Queued, "Requeued by reviewer.")
}
func Complete(db *sql.DB, itemID string, status Status, body string) (*Item, error) {
	if status != Approved && status != ChangesRequested && status != Completed {
		return nil, errors.New("invalid decision")
	}
	return transition(db, itemID, "", status, body)
}
func Remove(db *sql.DB, itemID, reason string) (*Item, error) {
	if reason == "" {
		reason = "Removed from queue without review."
	}
	now := time.Now().UnixMilli()
	result, err := db.Exec(`UPDATE queue_items SET removed_at=?,removed_reason=?,updated_at=? WHERE id=? AND removed_at IS NULL`, now, reason, now, itemID)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	_, err = db.Exec(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`, itemID, Completed, reason, now)
	if err != nil {
		return nil, err
	}
	return Get(db, itemID)
}
func transition(db *sql.DB, itemID string, only Status, next Status, body string) (*Item, error) {
	now := time.Now().UnixMilli()
	query := `UPDATE queue_items SET status=?,decision_body=?,updated_at=? WHERE id=? AND removed_at IS NULL`
	args := []any{next, nullable(body), now, itemID}
	if only != "" {
		query += " AND status=?"
		args = append(args, only)
	}
	result, err := db.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	if next != InReview {
		if _, err = db.Exec(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`, itemID, next, nullable(body), now); err != nil {
			return nil, err
		}
	}
	return Get(db, itemID)
}
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
