package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenCreatesCompatibleCurrentSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion { t.Fatalf("version=%d err=%v", version, err) }
	for _, name := range []string{"queue_items", "ask_conversations", "comments", "federation_nodes"} {
		var found string
		if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&found); err != nil || found != name { t.Fatalf("missing %s: %v", name, err) }
	}
}

func TestOpenUpgradesV18ChannelWithoutChangingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.db")
	db, err := sql.Open("sqlite", path); if err != nil { t.Fatal(err) }
	if _, err = db.Exec("CREATE TABLE comments(id INTEGER, session_id INTEGER, repo_id INTEGER); PRAGMA user_version=18"); err != nil { t.Fatal(err) }
	_ = db.Close()
	if _, err = Open(path); err != nil { t.Fatal(err) }
}
