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

func TestQueueControlPlaneHTTPContract(t *testing.T) {
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
	if err = json.Unmarshal(contents, &discovered); err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + fmt.Sprint(d.Port())
	call := func(method, path, payload string) (*http.Response, []byte) {
		request, _ := http.NewRequest(method, base+path, strings.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+discovered.Token)
		if payload != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response, body
	}
	created, body := call(http.MethodPost, "/api/queue", `{"title":"Parser","workspacePath":"/tmp/parser","topic":"parser","snapshotManifestPath":"/tmp/manifest.json","snapshotManifest":{"id":"snap","repos":[{}]},"acpHost":"127.0.0.1","acpPort":4123,"acpSessionId":"acp-1"}`)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create=%d %s", created.StatusCode, body)
	}
	var result struct {
		Item struct {
			ID       string `json:"id"`
			ACPState string `json:"acpState"`
		} `json:"item"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Item.ID == "" || result.Item.ACPState != "idle" {
		t.Fatalf("created=%s", body)
	}
	createdTwo, body := call(http.MethodPost, "/api/queue", `{"title":"Other","workspacePath":"/tmp/other"}`)
	if createdTwo.StatusCode != http.StatusCreated {
		t.Fatalf("second=%d %s", createdTwo.StatusCode, body)
	}
	var second struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatal(err)
	}
	response, body := call(http.MethodPost, "/api/queue/"+second.Item.ID+"/reorder", `{"position":1}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reorder=%d %s", response.StatusCode, body)
	}
	response, body = call(http.MethodPost, "/api/queue/open-next", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), second.Item.ID) || !strings.Contains(string(body), "in_review") {
		t.Fatalf("open=%d %s", response.StatusCode, body)
	}
	response, body = call(http.MethodPost, "/api/queue/"+result.Item.ID+"/feedback", `{"body":"Check boundary","path":"packages/parser/a.go","line":8}`)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("feedback=%d %s", response.StatusCode, body)
	}
	response, body = call(http.MethodGet, "/api/queue/"+result.Item.ID+"/feedback/prompt", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "packages/parser/a.go:8: Check boundary") {
		t.Fatalf("prompt=%d %s", response.StatusCode, body)
	}
	response, body = call(http.MethodPost, "/api/queue/"+result.Item.ID+"/decision", `{"decision":"changes_requested","body":"Please revise"}`)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "changes_requested") {
		t.Fatalf("decision=%d %s", response.StatusCode, body)
	}
	response, body = call(http.MethodGet, "/api/queue/"+result.Item.ID, "")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Check boundary") || !strings.Contains(string(body), "changes_requested") {
		t.Fatalf("detail=%d %s", response.StatusCode, body)
	}
	response, body = call(http.MethodGet, "/api/queue/"+result.Item.ID+"/reproduce", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "localreview reproduce") || !strings.Contains(string(body), "snap") {
		t.Fatalf("reproduce=%d %s", response.StatusCode, body)
	}
}

func TestQueueHomeReadModelsAreAvailableBeforeWorkspaceActivation(t *testing.T) {
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
	for _, path := range []string{"/api/workspaces", "/api/federation/queue", "/api/federation/nodes"} {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.Header.Set("Authorization", "Bearer "+discovered.Token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "nodes") && path != "/api/workspaces" {
			t.Fatalf("%s status=%d body=%s err=%v", path, response.StatusCode, body, err)
		}
	}
}

func TestWorkspaceActivationDiscoversNestedRepositories(t *testing.T) {
	workspace := t.TempDir()
	for _, relative := range []string{".", "tools/child"} {
		repo := filepath.Join(workspace, relative)
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, arguments := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
			if output, err := exec.Command("git", append([]string{"-C", repo}, arguments...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", arguments, err, output)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, "review.txt"), []byte("first\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("git", "-C", repo, "add", "review.txt").CombinedOutput(); err != nil {
			t.Fatalf("git add: %v %s", err, output)
		}
		if output, err := exec.Command("git", "-C", repo, "commit", "-m", "initial").CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v %s", err, output)
		}
	}
	// Make the nested repository a real modified-file review so full-file
	// projection has a deleted-line gate to assert against.
	if err := os.WriteFile(filepath.Join(workspace, "tools", "child", "review.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	req, _ := http.NewRequest(http.MethodPost, base+"/api/workspaces/open", strings.NewReader(`{"workspacePath":`+strconv.Quote(workspace)+`}`))
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "tools/child") {
		t.Fatalf("open status=%d body=%s", response.StatusCode, body)
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/api/repos", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "tools/child") {
		t.Fatalf("repos status=%d body=%s", response.StatusCode, body)
	}
	var repositories struct {
		Repos []struct {
			ID                    string `json:"id"`
			WorkspaceRelativePath string `json:"workspaceRelativePath"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(body, &repositories); err != nil {
		t.Fatal(err)
	}
	if len(repositories.Repos) != 2 || repositories.Repos[0].ID == "" {
		t.Fatalf("repositories=%s", body)
	}
	childID := ""
	for _, repo := range repositories.Repos {
		if repo.WorkspaceRelativePath == "tools/child" {
			childID = repo.ID
		}
	}
	if childID == "" {
		t.Fatalf("child repository missing: %s", body)
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/api/repos/"+childID+"/api/diff", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "review.txt") {
		t.Fatalf("diff status=%d body=%s", response.StatusCode, body)
	}
	for _, expectation := range []struct{ path, contains string }{
		{"/api/repos/" + childID + "/api/line-count/review.txt?oldRef=HEAD&newRef=HEAD", `"oldLineCount":1`},
		{"/api/repos/" + childID + "/api/generated-status/review.txt", `"isGenerated":false`},
		{"/api/repos/" + childID + "/api/fullfile/review.txt?side=current", `"hiddenStart":1`},
	} {
		req, _ = http.NewRequest(http.MethodGet, base+expectation.path, nil)
		req.Header.Set("Authorization", "Bearer "+discovered.Token)
		response, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), expectation.contains) {
			t.Fatalf("%s status=%d body=%s", expectation.path, response.StatusCode, body)
		}
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/api/repos/"+childID+"/api/revisions", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "initial") {
		t.Fatalf("revisions status=%d body=%s", response.StatusCode, body)
	}
	commentPayload := `{"threads":[{"id":"comment-1","filePath":"review.txt","position":{"side":"new","line":1},"messages":[{"id":"message-1","body":"Please clarify"}],"codeSnapshot":{"content":"first"}}]}`
	req, _ = http.NewRequest(http.MethodPost, base+"/api/repos/"+childID+"/api/comments", strings.NewReader(commentPayload))
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("comment write status=%d body=%s", response.StatusCode, body)
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/api/repos/"+childID+"/api/comments-json", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Please clarify") {
		t.Fatalf("comment read status=%d body=%s", response.StatusCode, body)
	}
	req, _ = http.NewRequest(http.MethodDelete, base+"/api/repos/"+childID+"/api/comments/comment-1", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("comment delete status=%d body=%s", response.StatusCode, body)
	}
	// Replaying the stale save must not resurrect the explicitly resolved thread.
	req, _ = http.NewRequest(http.MethodPost, base+"/api/repos/"+childID+"/api/comments", strings.NewReader(commentPayload))
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, base+"/api/repos/"+childID+"/api/comments-json", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(string(body), "Please clarify") {
		t.Fatalf("tombstone read status=%d body=%s", response.StatusCode, body)
	}
}

func TestGeneratedStatusRecognizesPathAndContentSignals(t *testing.T) {
	for _, test := range []struct {
		name, path, contents, source string
		generated                    bool
	}{
		{name: "ordinary source", path: "src/review.ts", contents: "export const answer = 42;\n", generated: false, source: "content"},
		{name: "generated path", path: "api/service.pb.go", contents: "package api\n", generated: true, source: "path"},
		{name: "generated header", path: "src/output.ts", contents: "// Code generated by tool. DO NOT EDIT.\n", generated: true, source: "content"},
		{name: "markdown wording is not a marker", path: "README.md", contents: "Generated code examples live here.\n", generated: false, source: "content"},
	} {
		t.Run(test.name, func(t *testing.T) {
			generated, source := generatedStatus(test.path, []byte(test.contents))
			if generated != test.generated || source != test.source {
				t.Fatalf("generatedStatus(%q) = (%v, %q), want (%v, %q)", test.path, generated, source, test.generated, test.source)
			}
		})
	}
}
