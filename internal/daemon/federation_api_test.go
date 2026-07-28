package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func federationRequest(t *testing.T, d *Daemon, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+d.token)
	response := httptest.NewRecorder()
	d.authorized(http.HandlerFunc(d.apiHandler)).ServeHTTP(response, req)
	return response
}

func TestFederationNodeCRUDNeverLeaksTokenOrFakesTransport(t *testing.T) {
	dir := t.TempDir()
	d, err := Start(t.Context(), Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	created := federationRequest(t, d, http.MethodPost, "/api/federation/nodes", `{"id":"lab","label":"Lab remote","sshTarget":"reviewer@example.test","remotePort":57140,"token":"not-for-the-browser"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "not-for-the-browser") || strings.Contains(created.Body.String(), `"tunnel"`) {
		t.Fatalf("secret or nonexistent tunnel leaked: %s", created.Body.String())
	}
	var createdBody struct {
		Node struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"node"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil || createdBody.Node.ID != "lab" || !createdBody.Node.Enabled {
		t.Fatalf("unexpected create response %#v err=%v", createdBody, err)
	}

	listed := federationRequest(t, d, http.MethodGet, "/api/federation/nodes", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "Lab remote") || strings.Contains(listed.Body.String(), "not-for-the-browser") {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
	status := federationRequest(t, d, http.MethodGet, "/api/federation/nodes/lab/status", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"available":false`) || !strings.Contains(status.Body.String(), `"state":"disconnected"`) {
		t.Fatalf("status=%d %s", status.Code, status.Body.String())
	}
	aggregate := federationRequest(t, d, http.MethodGet, "/api/federation/queue", "")
	if aggregate.Code != http.StatusOK || !strings.Contains(aggregate.Body.String(), `"transportAvailable":false`) || !strings.Contains(aggregate.Body.String(), "Remote federation transport is not available") {
		t.Fatalf("aggregate=%d %s", aggregate.Code, aggregate.Body.String())
	}

	// A connect request may re-enable saved metadata, but it must never claim a
	// fabricated SSH tunnel or issue a remote request.
	connect := federationRequest(t, d, http.MethodPost, "/api/federation/nodes/lab/connect", "")
	if connect.Code != http.StatusNotImplemented || strings.Contains(connect.Body.String(), `"localPort":57140`) {
		t.Fatalf("connect=%d %s", connect.Code, connect.Body.String())
	}
	remoteQueue := federationRequest(t, d, http.MethodGet, "/api/federation/nodes/lab/queue", "")
	if remoteQueue.Code != http.StatusNotImplemented {
		t.Fatalf("remote queue=%d %s", remoteQueue.Code, remoteQueue.Body.String())
	}

	disconnected := federationRequest(t, d, http.MethodPost, "/api/federation/nodes/lab/disconnect", "")
	if disconnected.Code != http.StatusOK || !strings.Contains(disconnected.Body.String(), `"enabled":false`) || !strings.Contains(disconnected.Body.String(), `"state":"disabled"`) {
		t.Fatalf("disconnect=%d %s", disconnected.Code, disconnected.Body.String())
	}
	deleted := federationRequest(t, d, http.MethodDelete, "/api/federation/nodes/lab", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
	missing := federationRequest(t, d, http.MethodGet, "/api/federation/nodes/lab/status", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
}

func TestFederationNodeValidationAndStableGeneratedID(t *testing.T) {
	dir := t.TempDir()
	d, err := Start(t.Context(), Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	invalid := federationRequest(t, d, http.MethodPost, "/api/federation/nodes", `{"label":"","sshTarget":"https://host","remotePort":0,"token":""}`)
	if invalid.Code != http.StatusBadRequest || strings.Contains(invalid.Body.String(), `"token":"`) {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
	disabled := federationRequest(t, d, http.MethodPost, "/api/federation/nodes", `{"label":"Saved remote","sshTarget":"reviewer@example.test","remotePort":57140,"token":"secret","enabled":false}`)
	if disabled.Code != http.StatusCreated || strings.Contains(disabled.Body.String(), "secret") || !strings.Contains(disabled.Body.String(), `"enabled":false`) {
		t.Fatalf("disabled=%d %s", disabled.Code, disabled.Body.String())
	}
	var body struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	if err := json.Unmarshal(disabled.Body.Bytes(), &body); err != nil || body.Node.ID == "" {
		t.Fatalf("generated ID absent %#v %v", body, err)
	}
}
