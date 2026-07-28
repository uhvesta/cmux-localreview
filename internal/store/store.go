// Package store owns the daemon SQLite boundary. The schema mirrors the
// TypeScript daemon's v19 schema so a freshly installed Go daemon creates the
// same durable queue/review state without a JavaScript runtime.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 19

// Open configures the SQLite durability settings shared by both daemon
// implementations. It accepts an existing v18 database and upgrades it
// transactionally; older databases remain the legacy daemon's responsibility
// until their migration parity test is added rather than being guessed at.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion {
		return fmt.Errorf("database schema v%d is newer than localreviewd v%d", version, SchemaVersion)
	}
	if version == SchemaVersion {
		return nil
	}
	tx, err := db.Begin()
	if err != nil { return err }
	defer func() { _ = tx.Rollback() }()
	if version == 0 {
		for _, statement := range currentSchema {
			if _, err := tx.Exec(statement); err != nil { return err }
		}
		version = SchemaVersion
	} else if version == 18 {
		if _, err := tx.Exec("ALTER TABLE comments ADD COLUMN channel TEXT NOT NULL DEFAULT 'formal' CHECK(channel IN ('formal','ask'))"); err != nil { return err }
		if _, err := tx.Exec("CREATE INDEX idx_comments_session_repo_channel ON comments(session_id, repo_id, channel)"); err != nil { return err }
		version = SchemaVersion
	} else {
		return fmt.Errorf("database schema v%d requires the legacy v18 upgrade before Go cutover", version)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil { return err }
	return tx.Commit()
}

// currentSchema is intentionally explicit instead of an ORM model: database
// layout is an on-disk API and is compared in migration parity tests.
var currentSchema = []string{
	`CREATE TABLE workspace (id INTEGER PRIMARY KEY CHECK (id = 1), root_path TEXT NOT NULL, created_at INTEGER NOT NULL)`,
	`CREATE TABLE repos (id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_relative_path TEXT NOT NULL, git_dir TEXT NOT NULL, remote_url TEXT, base_override TEXT, created_at INTEGER NOT NULL, UNIQUE (git_dir))`,
	`CREATE TABLE sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT, started_at INTEGER NOT NULL, frozen_at INTEGER)`,
	`CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL REFERENCES sessions(id), repo_id INTEGER NOT NULL REFERENCES repos(id), file_path TEXT NOT NULL, side TEXT NOT NULL CHECK (side IN ('old', 'new')), start_line INTEGER NOT NULL, end_line INTEGER NOT NULL, body TEXT NOT NULL, anchor_content_hash TEXT NOT NULL, orphaned INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, thread_id TEXT, messages_json TEXT, anchor_content TEXT, channel TEXT NOT NULL DEFAULT 'formal' CHECK(channel IN ('formal','ask')))` ,
	`CREATE UNIQUE INDEX idx_comments_thread ON comments(session_id, repo_id, thread_id)`,
	`CREATE INDEX idx_comments_session_repo_channel ON comments(session_id, repo_id, channel)`,
	`CREATE TABLE comment_tombstones (session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE, thread_id TEXT NOT NULL, deleted_at INTEGER NOT NULL, PRIMARY KEY(session_id,repo_id,thread_id))`,
	`CREATE TABLE btw_threads (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL REFERENCES sessions(id), transport TEXT NOT NULL CHECK (transport IN ('acp', 'terminal')), acp_provider TEXT, acp_session_id TEXT, repo_id INTEGER REFERENCES repos(id), file_path TEXT, start_line INTEGER, end_line INTEGER, created_at INTEGER NOT NULL, target_agent_id TEXT)`,
	`CREATE TABLE btw_questions (id INTEGER PRIMARY KEY AUTOINCREMENT, thread_id INTEGER NOT NULL REFERENCES btw_threads(id), body TEXT NOT NULL, created_at INTEGER NOT NULL)`,
	`CREATE TABLE btw_answers (id INTEGER PRIMARY KEY AUTOINCREMENT, question_id INTEGER NOT NULL REFERENCES btw_questions(id), body TEXT NOT NULL, pending INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
	`CREATE TABLE exports (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL REFERENCES sessions(id), content TEXT NOT NULL, destination TEXT NOT NULL CHECK (destination IN ('clipboard', 'cmux')), created_at INTEGER NOT NULL)`,
	`CREATE TABLE ui_state (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL, revision INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE workspace_registry (id INTEGER PRIMARY KEY AUTOINCREMENT, root_path TEXT NOT NULL UNIQUE, label TEXT, last_opened_at INTEGER NOT NULL, active INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE queue_items (id TEXT PRIMARY KEY, idempotent_key TEXT UNIQUE, title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '', workspace_path TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'local' CHECK(kind IN ('local','remote')), remote_url TEXT, status TEXT NOT NULL CHECK(status IN ('queued','in_review','changes_requested','approved','completed')), position INTEGER NOT NULL, agent_id TEXT, agent_provider TEXT, copilot_session_id TEXT, snapshot_manifest_path TEXT, snapshot_manifest_json TEXT, feedback_target TEXT, decision_body TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, base_ref TEXT, provenance_json TEXT, source_fingerprint TEXT, supersedes_id TEXT REFERENCES queue_items(id), acp_host TEXT, acp_port INTEGER, acp_session_id TEXT, agent_kind TEXT, acp_state TEXT NOT NULL DEFAULT 'unavailable' CHECK(acp_state IN ('unavailable','connecting','idle','busy','error')), acp_last_error TEXT, acp_updated_at INTEGER, identity_key TEXT, review_topic TEXT, removed_at INTEGER, removed_reason TEXT)`,
	`CREATE INDEX idx_queue_items_status_position ON queue_items(status, position)`,
	`CREATE INDEX idx_queue_items_workspace_fingerprint ON queue_items(workspace_path, source_fingerprint)`,
	`CREATE INDEX idx_queue_items_identity_created ON queue_items(identity_key, created_at DESC)`,
	`CREATE INDEX idx_queue_items_active_position ON queue_items(removed_at, status, position)`,
	`CREATE TABLE queue_feedback (id INTEGER PRIMARY KEY AUTOINCREMENT, queue_item_id TEXT NOT NULL REFERENCES queue_items(id) ON DELETE CASCADE, body TEXT NOT NULL, path TEXT, line INTEGER, created_at INTEGER NOT NULL, delivered_at INTEGER, source_key TEXT, side TEXT, end_line INTEGER)`,
	`CREATE UNIQUE INDEX idx_queue_feedback_source_key ON queue_feedback(queue_item_id, source_key) WHERE source_key IS NOT NULL`,
	`CREATE TABLE queue_decisions (id INTEGER PRIMARY KEY AUTOINCREMENT, queue_item_id TEXT NOT NULL REFERENCES queue_items(id) ON DELETE CASCADE, status TEXT NOT NULL CHECK(status IN ('approved','changes_requested','completed','requeued')), body TEXT, created_at INTEGER NOT NULL)`,
	`CREATE INDEX idx_queue_decisions_item_created ON queue_decisions(queue_item_id, created_at, id)`,
	`CREATE TABLE queue_watchers (workspace_path TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1, poll_interval_ms INTEGER NOT NULL DEFAULT 5000, title TEXT, body TEXT, base_ref TEXT, agent_id TEXT, agent_provider TEXT, feedback_target TEXT, provenance_json TEXT, last_fingerprint TEXT, last_queue_item_id TEXT REFERENCES queue_items(id), updated_at INTEGER NOT NULL, acp_host TEXT, acp_port INTEGER, acp_session_id TEXT, agent_kind TEXT, copilot_session_id TEXT)`,
	`CREATE TABLE agent_registry (id TEXT PRIMARY KEY, provider TEXT NOT NULL, command TEXT, workspace_path TEXT, review_session_id TEXT, status TEXT NOT NULL DEFAULT 'connected', metadata_json TEXT, updated_at INTEGER NOT NULL, surface_id TEXT, last_seen_at INTEGER, last_error TEXT, reconnect_attempts INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX idx_agent_registry_workspace_status ON agent_registry(workspace_path, status)`,
	`CREATE INDEX idx_btw_threads_target_agent ON btw_threads(target_agent_id)`,
	`CREATE TABLE ask_conversations (id TEXT PRIMARY KEY, queue_item_id TEXT, model TEXT, copilot_session_id TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, context_json TEXT, reasoning_effort TEXT, context_tier TEXT, review_session_id INTEGER REFERENCES sessions(id), archived_at INTEGER)`,
	`CREATE INDEX idx_ask_conversations_session_state ON ask_conversations(review_session_id, archived_at, updated_at DESC)`,
	`CREATE TABLE ask_messages (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL REFERENCES ask_conversations(id) ON DELETE CASCADE, role TEXT NOT NULL CHECK(role IN ('user','assistant','system')), body TEXT NOT NULL, pending INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, location_json TEXT)`,
	`CREATE INDEX idx_ask_messages_conversation ON ask_messages(conversation_id, id)`,
	`CREATE TABLE question_sets (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
	`CREATE TABLE questions (id INTEGER PRIMARY KEY AUTOINCREMENT, question_set_id TEXT NOT NULL REFERENCES question_sets(id) ON DELETE CASCADE, body TEXT NOT NULL, position INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(question_set_id,position))`,
	`CREATE INDEX idx_questions_set_position ON questions(question_set_id, position)`,
	`CREATE TABLE federation_nodes (id TEXT PRIMARY KEY, label TEXT NOT NULL, ssh_target TEXT NOT NULL, remote_port INTEGER NOT NULL, token TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, last_connected_at INTEGER, last_error TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
}

var ErrNotFound = errors.New("not found")
