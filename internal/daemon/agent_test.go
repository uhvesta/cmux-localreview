package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/agent"
)

func agentRequest(t *testing.T, d *Daemon, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://local.test/api"+path, bytes.NewBufferString(body))
	result := httptest.NewRecorder()
	d.apiHandler(result, req)
	return result
}

func TestAgentHeartbeatAndReconnectPersistNativeRoutingState(t *testing.T) {
	d := askRouteDaemon(t)
	registered := agentRequest(t, d, http.MethodPost, "/agents", `{
  "id":"copilot-feature", "provider":"copilot-cli", "command":"copilot --acp --port 7777",
  "workspacePath":"/work/review", "reviewSessionId":"session-7", "surfaceId":"surface-42",
  "metadata":{"acpHost":"127.0.0.1","acpPort":7777}
}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register=%d %s", registered.Code, registered.Body.String())
	}

	reconnect := agentRequest(t, d, http.MethodPost, "/agents/copilot-feature/reconnect", `{}`)
	if reconnect.Code != http.StatusOK || !bytes.Contains(reconnect.Body.Bytes(), []byte(`"status":"reconnecting"`)) {
		t.Fatalf("reconnect=%d %s", reconnect.Code, reconnect.Body.String())
	}

	heartbeat := agentRequest(t, d, http.MethodPost, "/agents/copilot-feature/heartbeat", `{
  "workspacePath":"/work/review-next", "surfaceId":"surface-43", "metadata":{"cmuxWorkspace":"review-2"}
}`)
	if heartbeat.Code != http.StatusOK || !bytes.Contains(heartbeat.Body.Bytes(), []byte(`"status":"connected"`)) {
		t.Fatalf("heartbeat=%d %s", heartbeat.Code, heartbeat.Body.String())
	}
	var payload struct {
		Agent agent.Record `json:"agent"`
	}
	if err := json.Unmarshal(heartbeat.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Agent.Command == nil || *payload.Agent.Command != "copilot --acp --port 7777" || payload.Agent.ReviewSessionID == nil || *payload.Agent.ReviewSessionID != "session-7" {
		t.Fatalf("heartbeat lost durable agent context: %#v", payload.Agent)
	}
	if payload.Agent.WorkspacePath == nil || *payload.Agent.WorkspacePath != "/work/review-next" || payload.Agent.SurfaceID == nil || *payload.Agent.SurfaceID != "surface-43" {
		t.Fatalf("heartbeat did not update binding: %#v", payload.Agent)
	}
	if payload.Agent.ReconnectAttempts != 0 || payload.Agent.LastError != nil {
		t.Fatalf("heartbeat did not reset connection state: %#v", payload.Agent)
	}
	if payload.Agent.Metadata["acpPort"] != float64(7777) || payload.Agent.Metadata["cmuxWorkspace"] != "review-2" {
		t.Fatalf("heartbeat did not merge metadata: %#v", payload.Agent.Metadata)
	}

	listed := agentRequest(t, d, http.MethodGet, "/agents", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(`"lastSeenAt"`)) || !bytes.Contains(listed.Body.Bytes(), []byte(`"reconnectAttempts":0`)) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
}

func TestAgentHeartbeatAndReconnectRejectUnsafeBindings(t *testing.T) {
	d := askRouteDaemon(t)
	if response := agentRequest(t, d, http.MethodPost, "/agents/missing/heartbeat", `{}`); response.Code != http.StatusNotFound {
		t.Fatalf("missing heartbeat=%d %s", response.Code, response.Body.String())
	}
	if response := agentRequest(t, d, http.MethodPost, "/agents", `{"id":"unbound","provider":"copilot"}`); response.Code != http.StatusCreated {
		t.Fatalf("register=%d %s", response.Code, response.Body.String())
	}
	if response := agentRequest(t, d, http.MethodPost, "/agents/unbound/heartbeat", `{}`); response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("surfaceId")) {
		t.Fatalf("unbound heartbeat=%d %s", response.Code, response.Body.String())
	}
	if response := agentRequest(t, d, http.MethodPost, "/agents/unbound/reconnect", `{"dryRun":true}`); response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("surface binding")) {
		t.Fatalf("unbound reconnect=%d %s", response.Code, response.Body.String())
	}
}
