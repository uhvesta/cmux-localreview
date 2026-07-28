package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
	"github.com/uhvesta/cmux-localreview/internal/snapshot"
)

func exportedTestWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "export-test@example.invalid"}, {"config", "user.name", "Export test"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "review.go"), []byte("package review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", workspace, "add", "review.go").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	if output, err := exec.Command("git", "-C", workspace, "commit", "-qm", "initial").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	return workspace
}

func exportHTTP(t *testing.T, base, token, method, path, payload string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func TestNativeFormalExportSendsCmuxAndRecordsHistory(t *testing.T) {
	workspace := exportedTestWorkspace(t)
	socketFile, err := os.CreateTemp("/tmp", "lr-cmux-*.sock")
	if err != nil {
		t.Fatal(err)
	}
	socket := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(socket) }()
	defer listener.Close()
	received := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		line, _ := bufio.NewReader(connection).ReadString('\n')
		received <- line
		var request struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(line), &request)
		_, _ = fmt.Fprintf(connection, `{"id":%q,"result":{"ok":true}}`+"\n", request.ID)
	}()

	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: dataDir, UIDir: filepath.Join(dataDir, "missing-ui"), CmuxSocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.activateWorkspace(workspace, "export fixture"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	review := *d.review
	d.mu.Unlock()
	now := time.Now().UnixMilli()
	if _, err := d.db.Exec(`INSERT INTO comments(session_id,repo_id,file_path,side,start_line,end_line,body,anchor_content_hash,created_at,updated_at,thread_id,messages_json,channel) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, review.SessionID, review.Repos[0].DBID, "review.go", "new", 1, 1, "Explain the package name", "fixture", now, now, "export-thread", `[{"body":"Explain the package name","author":"reviewer"}]`, "formal"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var discovered discovery
	if err := json.Unmarshal(contents, &discovered); err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + fmt.Sprint(d.Port())
	response, body := exportHTTP(t, base, discovered.Token, http.MethodPost, "/api/export", `{"destination":"cmux"}`)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Explain the package name") {
		t.Fatalf("export status=%d body=%s", response.StatusCode, body)
	}
	select {
	case line := <-received:
		if !strings.Contains(line, `"method":"surface.send_text"`) || !strings.Contains(line, "Explain the package name") {
			t.Fatalf("unexpected cmux message: %s", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cmux did not receive formal export")
	}
	var recorded int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM exports WHERE session_id=? AND destination='cmux'`, review.SessionID).Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("export history count=%d err=%v", recorded, err)
	}
	bad, _ := exportHTTP(t, base, discovered.Token, http.MethodPost, "/api/export", `{"destination":"terminal"}`)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid destination status=%d", bad.StatusCode)
	}
}

func TestNativeQueuePackageExportIsPortableAndAtomic(t *testing.T) {
	workspace := exportedTestWorkspace(t)
	manifest, manifestPath, err := snapshot.Capture(workspace, filepath.Join(t.TempDir(), "artifacts"), "")
	if err != nil {
		t.Fatal(err)
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: dataDir, UIDir: filepath.Join(dataDir, "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	item, created, err := queueStore.Enqueue(d.db, queueStore.EnqueueInput{Title: "portable snapshot", WorkspacePath: workspace, SnapshotManifestPath: manifestPath, SnapshotManifest: encodedManifest})
	if err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}
	if _, err := queueStore.AddFeedback(d.db, item.ID, queueStore.FeedbackInput{Body: "Review this", Path: "review.go", Line: intPointer(1)}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var discovered discovery
	if err := json.Unmarshal(contents, &discovered); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "exported-review")
	base := "http://127.0.0.1:" + fmt.Sprint(d.Port())
	response, body := exportHTTP(t, base, discovered.Token, http.MethodPost, "/api/queue/"+item.ID+"/export", `{"destination":`+strconv.Quote(destination)+`}`)
	if response.StatusCode != http.StatusCreated || !strings.Contains(string(body), destination) {
		t.Fatalf("package export=%d %s", response.StatusCode, body)
	}
	packageBytes, err := os.ReadFile(filepath.Join(destination, "review-package.json"))
	if err != nil || !strings.Contains(string(packageBytes), `"Review this"`) || !strings.Contains(string(packageBytes), `"workspacePath": "."`) {
		t.Fatalf("incomplete review package err=%v package=%s", err, packageBytes)
	}
	if _, err := snapshot.Materialize(filepath.Join(destination, "snapshot-manifest.json"), filepath.Join(t.TempDir(), "restored")); err != nil {
		t.Fatalf("exported package cannot be materialized: %v", err)
	}
	again, _ := exportHTTP(t, base, discovered.Token, http.MethodPost, "/api/queue/"+item.ID+"/export", `{"destination":`+strconv.Quote(destination)+`}`)
	if again.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-atomic overwrite status=%d", again.StatusCode)
	}
}
