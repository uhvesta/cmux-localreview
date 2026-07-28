package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Record struct {
	ID                string         `json:"id"`
	Provider          string         `json:"provider"`
	Command           *string        `json:"command"`
	WorkspacePath     *string        `json:"workspacePath"`
	ReviewSessionID   *string        `json:"reviewSessionId"`
	Status            string         `json:"status"`
	Metadata          map[string]any `json:"metadata"`
	SurfaceID         *string        `json:"surfaceId,omitempty"`
	LastSeenAt        *int64         `json:"lastSeenAt,omitempty"`
	LastError         *string        `json:"lastError,omitempty"`
	ReconnectAttempts int64          `json:"reconnectAttempts"`
	UpdatedAt         int64          `json:"updatedAt"`
}

const (
	StatusConnected    = "connected"
	StatusReconnecting = "reconnecting"
	StatusDisconnected = "disconnected"
)

func validStatus(status string) bool {
	switch status {
	case StatusConnected, StatusReconnecting, StatusDisconnected:
		return true
	default:
		return false
	}
}

func Register(db *sql.DB, r Record) (*Record, error) {
	if r.ID == "" || r.Provider == "" {
		return nil, errors.New("id and provider are required")
	}
	if r.Status == "" {
		r.Status = StatusConnected
	}
	if !validStatus(r.Status) {
		return nil, errors.New("status must be connected, reconnecting, or disconnected")
	}
	existing, err := Get(db, r.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if existing != nil {
		mergeRecord(&r, existing)
	}
	now := time.Now().UnixMilli()
	if r.Metadata == nil {
		r.Metadata = map[string]any{}
	}
	if r.SurfaceID != nil {
		r.Metadata["surfaceId"] = *r.SurfaceID
	}
	r.LastSeenAt = &now
	r.Metadata["lastSeenAt"] = now
	if r.Status == StatusConnected {
		r.LastError = nil
		delete(r.Metadata, "lastError")
	}
	r.Metadata["reconnectAttempts"] = r.ReconnectAttempts
	meta, _ := json.Marshal(r.Metadata)
	_, e := db.Exec(`INSERT INTO agent_registry(id,provider,command,workspace_path,review_session_id,status,metadata_json,surface_id,last_seen_at,last_error,reconnect_attempts,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET provider=excluded.provider,command=excluded.command,workspace_path=excluded.workspace_path,review_session_id=excluded.review_session_id,status=excluded.status,metadata_json=excluded.metadata_json,surface_id=excluded.surface_id,last_seen_at=excluded.last_seen_at,last_error=excluded.last_error,reconnect_attempts=excluded.reconnect_attempts,updated_at=excluded.updated_at`, r.ID, r.Provider, r.Command, r.WorkspacePath, r.ReviewSessionID, r.Status, string(meta), r.SurfaceID, now, r.LastError, r.ReconnectAttempts, now)
	if e != nil {
		return nil, e
	}
	return Get(db, r.ID)
}

// Get returns the full durable routing record.  The connection fields are
// columns as well as metadata so older clients can continue reading metadata
// while the native UI gets an explicit, typed status model.
func Get(db *sql.DB, id string) (*Record, error) {
	row := db.QueryRow(`SELECT id,provider,command,workspace_path,review_session_id,status,metadata_json,surface_id,last_seen_at,last_error,reconnect_attempts,updated_at FROM agent_registry WHERE id=?`, id)
	return scan(row)
}

type rowScanner interface{ Scan(...any) error }

func scan(row rowScanner) (*Record, error) {
	var r Record
	var metadata sql.NullString
	var surfaceID, lastError sql.NullString
	var lastSeen sql.NullInt64
	if err := row.Scan(&r.ID, &r.Provider, &r.Command, &r.WorkspacePath, &r.ReviewSessionID, &r.Status, &metadata, &surfaceID, &lastSeen, &lastError, &r.ReconnectAttempts, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Metadata = map[string]any{}
	if metadata.Valid {
		_ = json.Unmarshal([]byte(metadata.String), &r.Metadata)
	}
	if surfaceID.Valid {
		value := surfaceID.String
		r.SurfaceID = &value
		r.Metadata["surfaceId"] = value
	}
	if lastSeen.Valid {
		value := lastSeen.Int64
		r.LastSeenAt = &value
		r.Metadata["lastSeenAt"] = value
	}
	if lastError.Valid {
		value := lastError.String
		r.LastError = &value
		r.Metadata["lastError"] = value
	}
	r.Metadata["reconnectAttempts"] = r.ReconnectAttempts
	return &r, nil
}

func mergeRecord(r *Record, existing *Record) {
	if r.Command == nil {
		r.Command = existing.Command
	}
	if r.WorkspacePath == nil {
		r.WorkspacePath = existing.WorkspacePath
	}
	if r.ReviewSessionID == nil {
		r.ReviewSessionID = existing.ReviewSessionID
	}
	if r.SurfaceID == nil {
		r.SurfaceID = existing.SurfaceID
	}
	if r.Metadata == nil {
		r.Metadata = map[string]any{}
	}
	for key, value := range existing.Metadata {
		if _, exists := r.Metadata[key]; !exists {
			r.Metadata[key] = value
		}
	}
}
func List(db *sql.DB) ([]Record, error) {
	rows, e := db.Query(`SELECT id,provider,command,workspace_path,review_session_id,status,metadata_json,surface_id,last_seen_at,last_error,reconnect_attempts,updated_at FROM agent_registry ORDER BY updated_at DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Heartbeat is intentionally idempotent. It is the only native proof that a
// separately-running terminal agent is alive, so it clears a previous error
// and transitions the record to connected without reaching into a terminal.
func Heartbeat(db *sql.DB, id string, update Record) (*Record, error) {
	existing, err := Get(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if update.SurfaceID == nil && existing.SurfaceID == nil {
		return nil, errors.New("surfaceId is required for an unbound agent")
	}
	update.ID = existing.ID
	update.Provider = existing.Provider
	update.Status = StatusConnected
	update.LastError = nil
	update.ReconnectAttempts = 0
	return Register(db, update)
}

// Reconnect never injects keystrokes or starts a hidden external process. The
// Go daemon has no ACP/Node adapter by design, so a reconnect request marks a
// registered surface as awaiting its next heartbeat. This makes the status
// truthful while giving an agent a safe, idempotent rendezvous protocol.
func Reconnect(db *sql.DB, id string) (*Record, error) {
	existing, err := Get(db, id)
	if err != nil {
		return nil, err
	}
	if existing.SurfaceID == nil || *existing.SurfaceID == "" {
		return nil, errors.New("agent has no cmux surface binding")
	}
	now := time.Now().UnixMilli()
	metadata := make(map[string]any, len(existing.Metadata)+2)
	for key, value := range existing.Metadata {
		metadata[key] = value
	}
	attempts := existing.ReconnectAttempts + 1
	metadata["reconnectAttempts"] = attempts
	metadata["reconnectRequestedAt"] = now
	meta, _ := json.Marshal(metadata)
	_, err = db.Exec(`UPDATE agent_registry SET status=?,metadata_json=?,surface_id=?,last_seen_at=?,last_error=NULL,reconnect_attempts=?,updated_at=? WHERE id=?`, StatusReconnecting, string(meta), existing.SurfaceID, existing.LastSeenAt, attempts, now, id)
	if err != nil {
		return nil, err
	}
	return Get(db, id)
}
