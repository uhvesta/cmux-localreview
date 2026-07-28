package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenCreatesCompatibleCurrentSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	for _, name := range []string{"queue_items", "ask_conversations", "comments", "federation_nodes"} {
		var found string
		if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&found); err != nil || found != name {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestOpenUpgradesV18ChannelWithoutChangingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE comments(id INTEGER, session_id INTEGER, repo_id INTEGER); PRAGMA user_version=18"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err = Open(path); err != nil {
		t.Fatal(err)
	}
}

// This fixture deliberately starts at the first shipped schema rather than a
// hand-waved v18 approximation. It catches missing ALTERs/indexes that would
// otherwise only surface when a user upgrades an old daemon in place.
func TestOpenUpgradesV1DatabaseThroughEntireHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE workspace (id INTEGER PRIMARY KEY CHECK (id = 1), root_path TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE repos (id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_relative_path TEXT NOT NULL, git_dir TEXT NOT NULL, remote_url TEXT, base_override TEXT, created_at INTEGER NOT NULL, UNIQUE(git_dir))`,
		`CREATE TABLE sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT, started_at INTEGER NOT NULL, frozen_at INTEGER)`,
		`CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL REFERENCES sessions(id), repo_id INTEGER NOT NULL REFERENCES repos(id), file_path TEXT NOT NULL, side TEXT NOT NULL CHECK(side IN ('old','new')), start_line INTEGER NOT NULL, end_line INTEGER NOT NULL, body TEXT NOT NULL, anchor_content_hash TEXT NOT NULL, orphaned INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE btw_threads (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL REFERENCES sessions(id), transport TEXT NOT NULL CHECK(transport IN ('acp','terminal')), acp_provider TEXT, acp_session_id TEXT, repo_id INTEGER REFERENCES repos(id), file_path TEXT, start_line INTEGER, end_line INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE TABLE btw_questions (id INTEGER PRIMARY KEY AUTOINCREMENT, thread_id INTEGER NOT NULL REFERENCES btw_threads(id), body TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE btw_answers (id INTEGER PRIMARY KEY AUTOINCREMENT, question_id INTEGER NOT NULL REFERENCES btw_questions(id), body TEXT NOT NULL, pending INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE exports (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL REFERENCES sessions(id), content TEXT NOT NULL, destination TEXT NOT NULL CHECK(destination IN ('clipboard','cmux')), created_at INTEGER NOT NULL)`,
		`CREATE TABLE ui_state (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	for _, column := range []string{"channel", "thread_id", "messages_json"} {
		var found string
		if err := upgraded.QueryRow("SELECT name FROM pragma_table_info('comments') WHERE name=?", column).Scan(&found); err != nil || found != column {
			t.Fatalf("missing comments.%s: %v", column, err)
		}
	}
}
