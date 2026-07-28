package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoopbackDiscoveryAndBrowserSession(t *testing.T) {
	dir := t.TempDir()
	ui := filepath.Join(dir, "ui")
	if err := os.MkdirAll(ui, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ui, "index.html"), []byte("<main>review</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: dir, UIDir: ui})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	contents, err := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var discovered discovery
	if err := json.Unmarshal(contents, &discovered); err != nil {
		t.Fatal(err)
	}
	if discovered.Port != d.Port() || discovered.Token == "" {
		t.Fatalf("unexpected discovery: %#v", discovered)
	}
	if _, err := os.Stat(filepath.Join(dir, "daemon.db")); err != nil {
		t.Fatalf("Go daemon did not own persistent state: %v", err)
	}
	base := "http://127.0.0.1:" + fmt.Sprint(discovered.Port)
	health, err := http.Get(base + "/health")
	if err != nil || health.StatusCode != http.StatusOK {
		t.Fatalf("health=%v err=%v", health, err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/api/browser/session", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("exchange status=%d", response.StatusCode)
	}
	if cookie := response.Header.Get("Set-Cookie"); cookie == "" || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") {
		t.Fatalf("unsafe cookie: %q", cookie)
	}
}

func TestQueueHTTPContract(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	contents, err := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var discovered discovery
	if err := json.Unmarshal(contents, &discovered); err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + fmt.Sprint(d.Port())
	request, _ := http.NewRequest(http.MethodPost, base+"/api/queue", strings.NewReader(`{"title":"Parser","workspacePath":"/tmp/parser","reviewTopic":"parser"}`))
	request.Header.Set("Authorization", "Bearer "+discovered.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue status=%d", response.StatusCode)
	}
	list, _ := http.NewRequest(http.MethodGet, base+"/api/queue", nil)
	list.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(list)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("list=%v err=%v", response, err)
	}
}
