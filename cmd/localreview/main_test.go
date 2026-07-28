package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthAndRemoteCommandUsageIsValidatedBeforeDaemonAccess(t *testing.T) {
	for _, command := range [][]string{
		nil,
		{"logout"},
		{"login", "unexpected"},
	} {
		if err := authCommand(command); err == nil {
			t.Fatalf("auth command %v unexpectedly succeeded", command)
		}
	}
	for _, command := range [][]string{
		nil,
		{"submit"},
		{"status", "unexpected"},
		{"daemon", "unknown"},
	} {
		if err := remoteCommand(command); err == nil {
			t.Fatalf("remote command %v unexpectedly succeeded", command)
		}
	}
	for _, command := range [][]string{
		{"status", "unexpected"},
		{"stop", "unexpected"},
		{"unknown"},
	} {
		if err := daemonCommand(command); err == nil {
			t.Fatalf("daemon command %v unexpectedly succeeded", command)
		}
	}
	for _, command := range [][]string{
		{"guide", "unexpected"},
		{"configure", "--capability", "copilot"},
		{"connect"},
		{"disconnect"},
		{"unknown"},
	} {
		if err := githubAppCommand(command); err == nil {
			t.Fatalf("github-app command %v unexpectedly succeeded", command)
		}
	}
}

func TestDaemonStatusVerifiesServingDiscoveryIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"pid":1234,"version":"test"}`)
	}))
	defer server.Close()

	data := t.TempDir()
	t.Setenv("CMUX_LOCALREVIEW_DATA_DIR", data)
	var port int
	if _, err := fmt.Sscanf(strings.TrimPrefix(server.URL, "http://127.0.0.1:"), "%d", &port); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(`{"port":%d,"token":"private","pid":1234,"version":"test","createdAt":"2026-01-01T00:00:00Z"}`, port)
	if err := os.WriteFile(filepath.Join(data, "daemon.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := daemonStatus(&output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"running":true`, `"pid":1234`, `"port":`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status output %q missing %q", output.String(), expected)
		}
	}
}

func TestDaemonStatusRejectsPIDMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":true,"pid":555,"version":"test"}`)
	}))
	defer server.Close()
	data := t.TempDir()
	t.Setenv("CMUX_LOCALREVIEW_DATA_DIR", data)
	var port int
	if _, err := fmt.Sscanf(strings.TrimPrefix(server.URL, "http://127.0.0.1:"), "%d", &port); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "daemon.json"), []byte(fmt.Sprintf(`{"port":%d,"token":"private","pid":444}`, port)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := daemonStatus(&bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected stale discovery rejection, got %v", err)
	}
}

func TestShellQuoteCLI(t *testing.T) {
	for input, expected := range map[string]string{
		"":             "''",
		"/tmp/a b":     "'/tmp/a b'",
		"/tmp/it's ok": "'/tmp/it'\\''s ok'",
	} {
		if got := shellQuoteCLI(input); got != expected {
			t.Fatalf("shellQuoteCLI(%q)=%q, want %q", input, got, expected)
		}
	}
}

func TestOpenPullRequestUsesReadOnlyEndpointAndNeverQueues(t *testing.T) {
	requests := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = io.WriteString(w, `{"ok":true,"pid":1234,"version":"test"}`)
			return
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/api/local-review/pr" {
			http.Error(w, "queue insertion is forbidden for --pr", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer private" {
			http.Error(w, "missing daemon capability", http.StatusUnauthorized)
			return
		}
		var input struct {
			RemoteURL string `json:"remoteUrl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.RemoteURL != "https://github.com/acme/widget/pull/7" {
			http.Error(w, "bad pull request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"reviewUrl":"/review?localOnly=1"}`)
	}))
	defer server.Close()

	data := t.TempDir()
	t.Setenv("CMUX_LOCALREVIEW_DATA_DIR", data)
	var port int
	if _, err := fmt.Sscanf(strings.TrimPrefix(server.URL, "http://127.0.0.1:"), "%d", &port); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "daemon.json"), []byte(fmt.Sprintf(`{"port":%d,"token":"private","pid":1234}`, port)), 0o600); err != nil {
		t.Fatal(err)
	}

	// --no-open keeps the assertion browser-independent and lets the test
	// verify the same capability URL that a user can paste into Firefox.
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	callErr := openHome([]string{"--no-open", "--pr", "https://github.com/acme/widget/pull/7"})
	_ = writer.Close()
	os.Stdout = originalStdout
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if callErr != nil {
		t.Fatal(callErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(output); !strings.Contains(got, "/review?localOnly=1#daemonToken=private") {
		t.Fatalf("unexpected local-only review URL %q", got)
	}
	if len(requests) != 1 || requests[0] != "POST /api/local-review/pr" {
		t.Fatalf("--pr made unexpected daemon requests: %v", requests)
	}
}
