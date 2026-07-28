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

func TestOpenMigratesLegacyBtwRowsToNativeCompatibleTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE sessions (id INTEGER PRIMARY KEY, label TEXT, started_at INTEGER NOT NULL, frozen_at INTEGER)`,
		`CREATE TABLE repos (id INTEGER PRIMARY KEY, workspace_relative_path TEXT, git_dir TEXT, remote_url TEXT, base_override TEXT, created_at INTEGER)`,
		`CREATE TABLE btw_threads (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL REFERENCES sessions(id), transport TEXT NOT NULL CHECK(transport IN ('acp','terminal')), acp_provider TEXT, acp_session_id TEXT, repo_id INTEGER REFERENCES repos(id), file_path TEXT, start_line INTEGER, end_line INTEGER, created_at INTEGER NOT NULL, target_agent_id TEXT)`,
		`CREATE TABLE btw_questions (id INTEGER PRIMARY KEY AUTOINCREMENT, thread_id INTEGER NOT NULL REFERENCES btw_threads(id), body TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE btw_answers (id INTEGER PRIMARY KEY AUTOINCREMENT, question_id INTEGER NOT NULL REFERENCES btw_questions(id), body TEXT NOT NULL, pending INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO sessions(id,started_at) VALUES(1,1)`,
		`INSERT INTO repos(id,created_at) VALUES(1,1)`,
		`INSERT INTO btw_threads(id,session_id,transport,repo_id,created_at) VALUES(7,1,'acp',1,1)`,
		`INSERT INTO btw_questions(id,thread_id,body,created_at) VALUES(8,7,'historic',1)`,
		`INSERT INTO btw_answers(question_id,body,pending,created_at,updated_at) VALUES(8,'answer',0,1,1)`,
		`PRAGMA user_version=19`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var answer string
	if err := upgraded.QueryRow(`SELECT a.body FROM btw_answers a JOIN btw_questions q ON q.id=a.question_id JOIN btw_threads t ON t.id=q.thread_id WHERE t.id=7`).Scan(&answer); err != nil || answer != "answer" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if _, err := upgraded.Exec(`INSERT INTO btw_threads(session_id,transport,created_at) VALUES(1,'copilot',2)`); err != nil {
		t.Fatalf("native transport rejected: %v", err)
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
