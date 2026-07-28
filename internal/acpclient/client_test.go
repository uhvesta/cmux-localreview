package acpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeACP struct {
	listener net.Listener
	port     int
	mu       sync.Mutex
	methods  []string
	prompts  []string
	cancels  int
	loads    int
}

func startFakeACP(t *testing.T, loadSession bool) *fakeACP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeACP{listener: listener, port: listener.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.serve(conn, loadSession)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (f *fakeACP) serve(conn net.Conn, loadSession bool) {
	defer conn.Close()
	decoder := json.NewDecoder(bufio.NewReader(conn))
	encoder := json.NewEncoder(conn)
	for {
		var call struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&call); err != nil {
			return
		}
		f.mu.Lock()
		f.methods = append(f.methods, call.Method)
		f.mu.Unlock()
		switch call.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(call.ID), "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": loadSession}}})
		case "session/load":
			f.mu.Lock()
			f.loads++
			f.mu.Unlock()
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(call.ID), "result": map[string]any{}})
		case "session/prompt":
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(call.Params, &params)
			f.mu.Lock()
			for _, block := range params.Prompt {
				f.prompts = append(f.prompts, block.Text)
			}
			f.mu.Unlock()
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": "streaming"}})
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(call.ID), "result": map[string]any{"stopReason": "end_turn"}})
		case "session/cancel":
			f.mu.Lock()
			f.cancels++
			f.mu.Unlock()
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(call.ID), "result": map[string]any{}})
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(call.ID), "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
}

func TestConnectLoadsRetainedSessionDeliversPromptAndCancels(t *testing.T) {
	fixture := startFakeACP(t, true)
	var states []State
	var stateMu sync.Mutex
	updates := make(chan json.RawMessage, 1)
	client, err := Connect(context.Background(), Endpoint{Host: "127.0.0.1", Port: fixture.port, SessionID: "retained-session", CWD: "/workspace"}, Options{
		OnState:         func(state State, _ error) { stateMu.Lock(); states = append(states, state); stateMu.Unlock() },
		OnSessionUpdate: func(update json.RawMessage) { updates <- update },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Prompt(context.Background(), "Please address comment one")
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if err := client.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("did not receive session update")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.loads != 1 {
		t.Fatalf("loads = %d, want 1", fixture.loads)
	}
	if fixture.cancels != 1 {
		t.Fatalf("cancels = %d, want 1", fixture.cancels)
	}
	if len(fixture.prompts) != 1 || fixture.prompts[0] != "Please address comment one" {
		t.Fatalf("prompts = %#v", fixture.prompts)
	}
	stateMu.Lock()
	gotStates := append([]State(nil), states...)
	stateMu.Unlock()
	want := []State{StateConnecting, StateIdle, StateBusy, StateIdle}
	if len(gotStates) < len(want) {
		t.Fatalf("states = %#v, want prefix %#v", gotStates, want)
	}
	for i := range want {
		if gotStates[i] != want[i] {
			t.Fatalf("states = %#v, want prefix %#v", gotStates, want)
		}
	}
}

func TestConnectSkipsLoadForLegacyACP(t *testing.T) {
	fixture := startFakeACP(t, false)
	client, err := Connect(context.Background(), Endpoint{Host: "localhost", Port: fixture.port, SessionID: "retained"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.loads != 0 {
		t.Fatalf("loads = %d, want 0", fixture.loads)
	}
}

func TestValidateEndpointRejectsNonLoopback(t *testing.T) {
	_, err := ValidateEndpoint(Endpoint{Host: "example.com", Port: 4312, SessionID: "x"})
	if err == nil {
		t.Fatal("ValidateEndpoint accepted remote host")
	}
}

func TestPromptRejectsConcurrentDelivery(t *testing.T) {
	fixture := startFakeACP(t, false)
	client, err := Connect(context.Background(), Endpoint{Host: "127.0.0.1", Port: fixture.port, SessionID: "retained"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.mu.Lock()
	client.busy = true
	client.mu.Unlock()
	_, err = client.Prompt(context.Background(), "duplicate")
	if err == nil {
		t.Fatal("Prompt accepted concurrent delivery")
	}
}
