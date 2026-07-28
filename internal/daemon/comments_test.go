package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCommentSnapshotsAreDurableConcurrentAndKeepAskSeparate(t *testing.T) {
	workspace := t.TempDir()
	for _, arguments := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "review.go"), []byte("package review\nconst original = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "review.go"}, {"commit", "-qm", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "review.go"), []byte("package review\nconst changed = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := Start(context.Background(), Options{DataDir: t.TempDir(), UIDir: filepath.Join(t.TempDir(), "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", d.Port())
	call := func(method, path, body string) (int, []byte) {
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+d.token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		contents, _ := io.ReadAll(response.Body)
		return response.StatusCode, contents
	}
	status, body := call(http.MethodPost, "/api/workspaces/open", `{"workspacePath":`+strconv.Quote(workspace)+`}`)
	if status != http.StatusOK {
		t.Fatalf("open workspace status=%d body=%s", status, body)
	}
	var opened struct {
		Repos []struct {
			ID string `json:"id"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(body, &opened); err != nil || len(opened.Repos) != 1 {
		t.Fatalf("open body=%s err=%v", body, err)
	}
	commentsPath := "/api/repos/" + opened.Repos[0].ID + "/api/comments"
	status, body = call(http.MethodGet, commentsPath, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"version":0`) {
		t.Fatalf("initial comments status=%d body=%s", status, body)
	}
	ask := `{"baseVersion":0,"threads":[{"id":"ask-inline","filePath":"review.go","position":{"side":"new","line":2},"messages":[{"id":"ask-message","body":"/ask why is this safe?"}],"codeSnapshot":{"content":"const changed = 2"}}]}`
	status, body = call(http.MethodPost, commentsPath, ask)
	if status != http.StatusOK || !strings.Contains(string(body), `"version":1`) || !strings.Contains(string(body), `"channel":"ask"`) {
		t.Fatalf("ask comment status=%d body=%s", status, body)
	}
	status, body = call(http.MethodGet, "/api/export/prompt", "")
	if status != http.StatusOK || strings.Contains(string(body), "why is this safe") {
		t.Fatalf("/ask leaked into formal export: status=%d body=%s", status, body)
	}
	importedAsk := `[{"type":"thread","id":"ask-import","filePath":"review.go","position":{"side":"new","line":2},"body":"/ask is this import isolated?","codeSnapshot":{"content":"const changed = 2"}}]`
	status, body = call(http.MethodPost, "/api/repos/"+opened.Repos[0].ID+"/api/comment-imports", importedAsk)
	if status != http.StatusOK || !strings.Contains(string(body), `"changed":true`) {
		t.Fatalf("ask import status=%d body=%s", status, body)
	}
	status, body = call(http.MethodGet, "/api/export/prompt", "")
	if status != http.StatusOK || strings.Contains(string(body), "is this import isolated") {
		t.Fatalf("imported /ask leaked into formal export: status=%d body=%s", status, body)
	}

	formal := `{"baseVersion":0,"threads":[{"id":"formal-inline","filePath":"review.go","position":{"side":"new","line":2},"messages":[{"body":"Please rename this."}]}]}`
	status, body = call(http.MethodPost, commentsPath, formal)
	if status != http.StatusConflict || !strings.Contains(string(body), `"merged":true`) || !strings.Contains(string(body), `"ask-inline"`) {
		t.Fatalf("stale comment snapshot status=%d body=%s", status, body)
	}
	status, body = call(http.MethodDelete, commentsPath+"/ask-inline", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"version":3`) {
		t.Fatalf("delete status=%d body=%s", status, body)
	}
	status, body = call(http.MethodPost, commentsPath, ask)
	if status != http.StatusConflict || strings.Contains(string(body), `"ask-inline"`) {
		t.Fatalf("stale replay resurrected resolved ask thread: status=%d body=%s", status, body)
	}
}
