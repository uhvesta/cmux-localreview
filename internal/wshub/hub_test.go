package wshub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dial(t *testing.T, serverURL string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/ws"
	connection, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.CloseNow() })
	return connection
}

func readJSON(t *testing.T, connection *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBroadcastDiffUpdatedUsesExactReviewerShape(t *testing.T) {
	hub := New(Options{})
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Close()
	client := dial(t, server.URL)
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	hub.BroadcastDiffUpdated("repo-abc")
	got := readJSON(t, client)
	if len(got) != 2 || got["type"] != "diff-updated" || got["repoId"] != "repo-abc" {
		t.Fatalf("message=%#v", got)
	}
}

func TestClientLifecycleCallsLastDisconnectOnce(t *testing.T) {
	var calls atomic.Int32
	hub := New(Options{OnLastClientDisconnect: func() { calls.Add(1) }})
	server := httptest.NewServer(hub)
	defer server.Close()
	defer hub.Close()
	a := dial(t, server.URL)
	b := dial(t, server.URL)
	deadline := time.Now().Add(time.Second)
	for hub.ClientCount() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hub.ClientCount() != 2 {
		t.Fatalf("count=%d", hub.ClientCount())
	}
	_ = a.Close(websocket.StatusNormalClosure, "")
	deadline = time.Now().Add(time.Second)
	for hub.ClientCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 0 {
		t.Fatalf("last callback fired with one client remaining: %d", calls.Load())
	}
	_ = b.Close(websocket.StatusNormalClosure, "")
	deadline = time.Now().Add(time.Second)
	for calls.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("last callback count=%d", calls.Load())
	}
}

func TestCloseStopsFutureConnections(t *testing.T) {
	hub := New(Options{})
	server := httptest.NewServer(hub)
	defer server.Close()
	hub.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	_, response, err := websocket.Dial(context.Background(), url, nil)
	if err == nil {
		t.Fatal("dial succeeded after close")
	}
	if response == nil || response.StatusCode != 503 {
		t.Fatalf("response=%v", response)
	}
}
