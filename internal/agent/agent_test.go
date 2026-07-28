package agent

import (
	"path/filepath"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/store"
)

func text(value string) *string { return &value }

func TestHeartbeatMergesRoutingMetadataAndReconnectIsDurable(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	initial, err := Register(db, Record{
		ID: "agent-1", Provider: "copilot-cli", Command: text("copilot --acp --port 7777"),
		WorkspacePath: text("/work/a"), ReviewSessionID: text("session-a"), SurfaceID: text("surface-a"),
		Metadata: map[string]any{"acpPort": 7777},
	})
	if err != nil {
		t.Fatal(err)
	}
	if initial.LastSeenAt == nil || initial.SurfaceID == nil {
		t.Fatalf("initial=%#v", initial)
	}

	reconnecting, err := Reconnect(db, initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconnecting.Status != StatusReconnecting || reconnecting.ReconnectAttempts != 1 {
		t.Fatalf("reconnecting=%#v", reconnecting)
	}

	heartbeat, err := Heartbeat(db, initial.ID, Record{WorkspacePath: text("/work/b"), SurfaceID: text("surface-b"), Metadata: map[string]any{"cmuxWorkspace": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.Status != StatusConnected || heartbeat.ReconnectAttempts != 0 || heartbeat.LastError != nil {
		t.Fatalf("heartbeat=%#v", heartbeat)
	}
	if heartbeat.Command == nil || *heartbeat.Command != "copilot --acp --port 7777" || heartbeat.ReviewSessionID == nil || *heartbeat.ReviewSessionID != "session-a" {
		t.Fatalf("heartbeat lost routing data: %#v", heartbeat)
	}
	if heartbeat.WorkspacePath == nil || *heartbeat.WorkspacePath != "/work/b" || heartbeat.SurfaceID == nil || *heartbeat.SurfaceID != "surface-b" {
		t.Fatalf("heartbeat binding=%#v", heartbeat)
	}
	if heartbeat.Metadata["acpPort"] != float64(7777) || heartbeat.Metadata["cmuxWorkspace"] != "b" {
		t.Fatalf("heartbeat metadata=%#v", heartbeat.Metadata)
	}
}

func TestHeartbeatRejectsUnknownAndUnboundAgent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = Heartbeat(db, "missing", Record{}); err == nil {
		t.Fatal("missing heartbeat succeeded")
	}
	if _, err = Register(db, Record{ID: "unbound", Provider: "copilot-cli"}); err != nil {
		t.Fatal(err)
	}
	if _, err = Heartbeat(db, "unbound", Record{}); err == nil {
		t.Fatal("unbound heartbeat succeeded")
	}
	if _, err = Reconnect(db, "unbound"); err == nil {
		t.Fatal("unbound reconnect succeeded")
	}
}
