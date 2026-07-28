package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Record struct {
	ID            string         `json:"id"`
	Provider      string         `json:"provider"`
	WorkspacePath *string        `json:"workspacePath"`
	Status        string         `json:"status"`
	Metadata      map[string]any `json:"metadata"`
	UpdatedAt     int64          `json:"updatedAt"`
}

func Register(db *sql.DB, r Record) (*Record, error) {
	if r.ID == "" || r.Provider == "" {
		return nil, errors.New("id and provider are required")
	}
	if r.Status == "" {
		r.Status = "connected"
	}
	now := time.Now().UnixMilli()
	meta, _ := json.Marshal(r.Metadata)
	_, e := db.Exec(`INSERT INTO agent_registry(id,provider,workspace_path,status,metadata_json,last_seen_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET provider=excluded.provider,workspace_path=excluded.workspace_path,status=excluded.status,metadata_json=excluded.metadata_json,last_seen_at=excluded.last_seen_at,updated_at=excluded.updated_at`, r.ID, r.Provider, r.WorkspacePath, r.Status, string(meta), now, now)
	if e != nil {
		return nil, e
	}
	r.UpdatedAt = now
	return &r, nil
}
func List(db *sql.DB) ([]Record, error) {
	rows, e := db.Query(`SELECT id,provider,workspace_path,status,metadata_json,updated_at FROM agent_registry ORDER BY updated_at DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var r Record
		var meta sql.NullString
		if e = rows.Scan(&r.ID, &r.Provider, &r.WorkspacePath, &r.Status, &meta, &r.UpdatedAt); e != nil {
			return nil, e
		}
		if meta.Valid {
			_ = json.Unmarshal([]byte(meta.String), &r.Metadata)
		}
		if r.Metadata == nil {
			r.Metadata = map[string]any{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
