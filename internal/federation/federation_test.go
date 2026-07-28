package federation

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/store"
)

type fakeClient struct {
	items map[string][]map[string]any
	errs  map[string]error
}

func (f fakeClient) ListQueue(_ context.Context, n Node) ([]map[string]any, error) {
	if err := f.errs[n.ID]; err != nil {
		return nil, err
	}
	return f.items[n.ID], nil
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNodeLifecycleRedactsSecretAndPreservesRetry(t *testing.T) {
	db := testDB(t)
	node, err := Upsert(db, Config{ID: "lab", Label: "Lab", SSHTarget: "review@host", RemotePort: 57140, Token: "secret", Enabled: true})
	if err != nil || node.State != Disconnected || node.Tunnel.Host != "127.0.0.1" {
		t.Fatalf("unexpected node: %#v err=%v", node, err)
	}
	if err := MarkRetryableFailure(db, "lab", errors.New("tunnel unavailable")); err != nil {
		t.Fatal(err)
	}
	node, _ = Get(db, "lab")
	if node.State != Error || node.LastError == nil {
		t.Fatalf("expected retryable error, got %#v", node)
	}
	if err := MarkConnected(db, "lab"); err != nil {
		t.Fatal(err)
	}
	node, _ = Get(db, "lab")
	if node.State != Connected || node.LastError != nil {
		t.Fatalf("expected connected node, got %#v", node)
	}
	if err := Disconnect(db, "lab"); err != nil {
		t.Fatal(err)
	}
	node, _ = Get(db, "lab")
	if node.Enabled || node.State != Disconnected {
		t.Fatalf("expected disconnected node, got %#v", node)
	}
	node, err = Reconnect(db, "lab")
	if err != nil || !node.Enabled || node.State != Connecting || node.LastError != nil {
		t.Fatalf("expected connecting retry state, got %#v err=%v", node, err)
	}
	var token string
	if err := db.QueryRow(`SELECT token FROM federation_nodes WHERE id='lab'`).Scan(&token); err != nil || token != "" {
		t.Fatalf("federation token must not be persisted in SQLite, got %q %v", token, err)
	}
}

func TestValidationAndLoopbackEndpoint(t *testing.T) {
	db := testDB(t)
	for _, config := range []Config{
		{ID: "", Label: "x", SSHTarget: "host", RemotePort: 1, Token: "x"},
		{ID: "x", Label: "x", SSHTarget: "https://host", RemotePort: 1, Token: "x"},
		{ID: "x", Label: "x", SSHTarget: "host", RemotePort: 0, Token: "x"},
		{ID: "x", Label: "x", SSHTarget: "host", RemotePort: 1, Token: ""},
	} {
		if _, err := Upsert(db, config); err == nil {
			t.Fatalf("expected validation failure for %#v", config)
		}
	}
	if _, err := ParseLoopbackEndpoint("192.168.1.9", 3); err == nil {
		t.Fatal("external endpoint accepted")
	}
	endpoint, err := ParseLoopbackEndpoint("::1", 57140)
	if err != nil || endpoint.Host != "::1" {
		t.Fatalf("unexpected endpoint %#v %v", endpoint, err)
	}
}

func TestMigrateLegacyTokensClearsSQLiteAfterSecretWrite(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO federation_nodes(id,label,ssh_target,remote_port,token,enabled,created_at,updated_at) VALUES('old','Old','user@host',57140,'legacy-token',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{}
	if err := MigrateLegacyTokens(db, func(id, token string) error { stored[id] = token; return nil }); err != nil {
		t.Fatal(err)
	}
	if stored["old"] != "legacy-token" {
		t.Fatalf("stored=%#v", stored)
	}
	var token string
	if err := db.QueryRow(`SELECT token FROM federation_nodes WHERE id='old'`).Scan(&token); err != nil || token != "" {
		t.Fatalf("sqlite token=%q err=%v", token, err)
	}
}

func TestAggregateQueueKeepsHealthyNodesWhenOneFails(t *testing.T) {
	db := testDB(t)
	for _, config := range []Config{
		{ID: "good", Label: "Good", SSHTarget: "good-host", RemotePort: 57140, Token: "a", Enabled: true},
		{ID: "bad", Label: "Bad", SSHTarget: "bad-host", RemotePort: 57141, Token: "b", Enabled: true},
		{ID: "off", Label: "Off", SSHTarget: "off-host", RemotePort: 57142, Token: "c", Enabled: false},
	} {
		if _, err := Upsert(db, config); err != nil {
			t.Fatal(err)
		}
	}
	result, err := AggregateQueue(context.Background(), db, fakeClient{
		items: map[string][]map[string]any{"good": {{"id": "remote-item"}}},
		errs:  map[string]error{"bad": errors.New("dial failed")},
	})
	if err != nil || len(result.Items) != 1 || result.Items[0].NodeID != "good" {
		t.Fatalf("unexpected aggregate %#v err=%v", result, err)
	}
	var byID = map[string]Node{}
	for _, node := range result.Nodes {
		byID[node.ID] = node
	}
	if byID["good"].State != Connected || byID["bad"].State != Error || byID["off"].State != Disconnected {
		t.Fatalf("states not preserved: %#v", byID)
	}
}
