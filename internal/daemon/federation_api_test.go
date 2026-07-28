package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/federation"
)

type memoryFederationSecrets map[string]string

func (m memoryFederationSecrets) key(s, a string) string          { return s + "/" + a }
func (m memoryFederationSecrets) Get(s, a string) (string, error) { return m[m.key(s, a)], nil }
func (m memoryFederationSecrets) Set(s, a, v string) error        { m[m.key(s, a)] = v; return nil }
func (m memoryFederationSecrets) Delete(s, a string) error        { delete(m, m.key(s, a)); return nil }

type unavailableFederationSecrets struct{}

func (unavailableFederationSecrets) Get(string, string) (string, error) {
	return "", errors.New("keychain locked")
}
func (unavailableFederationSecrets) Set(string, string, string) error {
	return errors.New("keychain locked")
}
func (unavailableFederationSecrets) Delete(string, string) error { return nil }

type fakeFederationTunnel struct {
	endpoint federation.TunnelEndpoint
	closed   bool
}

func (t *fakeFederationTunnel) Endpoint() federation.TunnelEndpoint { return t.endpoint }
func (t *fakeFederationTunnel) Close() error                        { t.closed = true; return nil }

type fakeFederationDialer struct {
	endpoint federation.TunnelEndpoint
	calls    int
	tunnel   *fakeFederationTunnel
}

func (d *fakeFederationDialer) Dial(_ context.Context, _ federation.Node) (FederationTunnel, error) {
	d.calls++
	d.tunnel = &fakeFederationTunnel{endpoint: d.endpoint}
	return d.tunnel, nil
}

type failedFederationDialer struct{}

func (failedFederationDialer) Dial(context.Context, federation.Node) (FederationTunnel, error) {
	return nil, errors.New("SSH key rejected")
}

func federationRequest(t *testing.T, d *Daemon, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+d.token)
	response := httptest.NewRecorder()
	d.authorized(http.HandlerFunc(d.apiHandler)).ServeHTTP(response, req)
	return response
}

func TestFederationNodeCRUDAndNativeLoopbackTransport(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer not-for-the-browser" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/queue":
			_, _ = w.Write([]byte(`{"items":[{"id":"remote-item","title":"Remote review"}]}`))
		case "/api/workspaces":
			_, _ = w.Write([]byte(`{"workspaces":[{"workspacePath":"/remote/project","label":"Remote project"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	address := remote.Listener.Addr().(*net.TCPAddr)
	dialer := &fakeFederationDialer{endpoint: federation.TunnelEndpoint{Host: "127.0.0.1", Port: address.Port}}
	dir := t.TempDir()
	secrets := memoryFederationSecrets{}
	d, err := Start(t.Context(), Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui"), FederationDialer: dialer, FederationSecrets: secrets})
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
	var persisted string
	if err := d.db.QueryRow(`SELECT token FROM federation_nodes WHERE id='lab'`).Scan(&persisted); err != nil || persisted != "" {
		t.Fatalf("node secret was persisted in sqlite: %q %v", persisted, err)
	}
	if secret, _ := secrets.Get(federationSecretService, federationSecretAccount("lab")); secret != "not-for-the-browser" {
		t.Fatalf("secret store=%q", secret)
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
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"available":true`) || !strings.Contains(status.Body.String(), `"state":"disconnected"`) {
		t.Fatalf("status=%d %s", status.Code, status.Body.String())
	}
	aggregate := federationRequest(t, d, http.MethodGet, "/api/federation/queue", "")
	if aggregate.Code != http.StatusOK || !strings.Contains(aggregate.Body.String(), `"transportAvailable":true`) || !strings.Contains(aggregate.Body.String(), "remote-item") || strings.Contains(aggregate.Body.String(), "not-for-the-browser") {
		t.Fatalf("aggregate=%d %s", aggregate.Code, aggregate.Body.String())
	}
	if dialer.calls != 1 {
		t.Fatalf("dial calls=%d, want 1", dialer.calls)
	}
	// A cached aggregate does not reconnect; an explicit connect refreshes.
	again := federationRequest(t, d, http.MethodGet, "/api/federation/queue", "")
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"cached":true`) || dialer.calls != 1 {
		t.Fatalf("cached=%d %s calls=%d", again.Code, again.Body.String(), dialer.calls)
	}
	connect := federationRequest(t, d, http.MethodPost, "/api/federation/nodes/lab/connect", "")
	if connect.Code != http.StatusOK || !strings.Contains(connect.Body.String(), `"state":"connected"`) || strings.Contains(connect.Body.String(), `"localPort":57140`) {
		t.Fatalf("connect=%d %s", connect.Code, connect.Body.String())
	}
	remoteQueue := federationRequest(t, d, http.MethodGet, "/api/federation/nodes/lab/queue", "")
	if remoteQueue.Code != http.StatusOK || !strings.Contains(remoteQueue.Body.String(), "remote-item") {
		t.Fatalf("remote queue=%d %s", remoteQueue.Code, remoteQueue.Body.String())
	}
	workspaces := federationRequest(t, d, http.MethodGet, "/api/federation/nodes/lab/workspaces", "")
	if workspaces.Code != http.StatusOK || !strings.Contains(workspaces.Body.String(), "/remote/project") {
		t.Fatalf("workspaces=%d %s", workspaces.Code, workspaces.Body.String())
	}

	disconnected := federationRequest(t, d, http.MethodPost, "/api/federation/nodes/lab/disconnect", "")
	if disconnected.Code != http.StatusOK || !strings.Contains(disconnected.Body.String(), `"enabled":false`) || !strings.Contains(disconnected.Body.String(), `"state":"disabled"`) {
		t.Fatalf("disconnect=%d %s", disconnected.Code, disconnected.Body.String())
	}
	if dialer.tunnel == nil || !dialer.tunnel.closed {
		t.Fatal("disconnect did not close tunnel")
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

func TestFederationNodeSecretStoreFailureIsActionable(t *testing.T) {
	d, err := Start(t.Context(), Options{
		DataDir:           t.TempDir(),
		UIDir:             filepath.Join(t.TempDir(), "missing-ui"),
		FederationSecrets: unavailableFederationSecrets{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	response := federationRequest(t, d, http.MethodPost, "/api/federation/nodes", `{"label":"Locked keychain","sshTarget":"reviewer@example.test","remotePort":57140,"token":"not-for-the-browser"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Unlock or enable your OS credential store") || strings.Contains(response.Body.String(), "not-for-the-browser") {
		t.Fatalf("response is not safe/actionable: %s", response.Body.String())
	}
}

func TestFederationNodeValidationAndStableGeneratedID(t *testing.T) {
	dir := t.TempDir()
	d, err := Start(t.Context(), Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui"), FederationSecrets: memoryFederationSecrets{}})
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

func TestFederationFailureIsRetryableAndDoesNotHideOtherQueueState(t *testing.T) {
	dir := t.TempDir()
	d, err := Start(t.Context(), Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui"), FederationDialer: failedFederationDialer{}, FederationSecrets: memoryFederationSecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	created := federationRequest(t, d, http.MethodPost, "/api/federation/nodes", `{"id":"bad","label":"Bad SSH","sshTarget":"user@host","remotePort":57140,"token":"capability"}`)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	queue := federationRequest(t, d, http.MethodGet, "/api/federation/queue", "")
	if queue.Code != http.StatusOK || !strings.Contains(queue.Body.String(), "SSH key rejected") || !strings.Contains(queue.Body.String(), `"state":"error"`) {
		t.Fatalf("queue=%d %s", queue.Code, queue.Body.String())
	}
	retry := federationRequest(t, d, http.MethodPost, "/api/federation/nodes/bad/connect", "")
	if retry.Code != http.StatusBadGateway || !strings.Contains(retry.Body.String(), "SSH key rejected") {
		t.Fatalf("retry=%d %s", retry.Code, retry.Body.String())
	}
}
