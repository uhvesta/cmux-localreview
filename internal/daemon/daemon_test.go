package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/githubauth"
	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
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

type authSecrets map[string]string

func (s authSecrets) Get(service, account string) (string, error) { return s[service+"/"+account], nil }
func (s authSecrets) Set(service, account, value string) error {
	s[service+"/"+account] = value
	return nil
}
func (s authSecrets) Delete(service, account string) error {
	delete(s, service+"/"+account)
	return nil
}

type authConfig map[githubauth.Capability]string

func (c authConfig) ClientID(capability githubauth.Capability) (string, error) {
	return c[capability], nil
}
func (c authConfig) SetClientID(capability githubauth.Capability, id string) error {
	c[capability] = id
	return nil
}

type authTransport func(*http.Request) (*http.Response, error)

func (f authTransport) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestGitHubAuthHTTPContract(t *testing.T) {
	transport := authTransport(func(request *http.Request) (*http.Response, error) {
		body := `{"access_token":"fixture-token"}`
		switch request.URL.Path {
		case "/login/device/code":
			body = `{"device_code":"device","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900}`
		case "/user":
			body = `{"login":"fixture-reviewer"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body))}, nil
	})
	auth := githubauth.New(authSecrets{}, authConfig{}, transport, func(string) error { return nil })
	dir := t.TempDir()
	ui := filepath.Join(dir, "ui")
	if err := os.MkdirAll(ui, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ui, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: dir, UIDir: ui, GitHubAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	base := "http://127.0.0.1:" + strconv.Itoa(d.Port())
	request := func(method, path, body string) (*http.Response, []byte) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+d.token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		contents, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return response, contents
	}
	response, body := request(http.MethodGet, "/api/github/auth/status", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"configured":false`) {
		t.Fatalf("initial status=%d %s", response.StatusCode, body)
	}
	response, body = request(http.MethodPost, "/api/github/auth/configure", `{"capability":"read","clientId":"Iv1.fixtureRead"}`)
	if response.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("configure=%d %s", response.StatusCode, body)
	}
	response, body = request(http.MethodPost, "/api/github/auth/configure", `{"capability":"read","clientId":"Iv1.fixtureRead","clientSecret":"fixture-secret"}`)
	if response.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("configure secret=%d %s", response.StatusCode, body)
	}
	response, body = request(http.MethodPost, "/api/github/auth/read/start", `{}`)
	if response.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"flow":"loopback"`) || !strings.Contains(string(body), "127.0.0.1") || !strings.Contains(string(body), "8787") || strings.Contains(string(body), "fixture-secret") {
		t.Fatalf("default loopback start=%d %s", response.StatusCode, body)
	}
	response, body = request(http.MethodPost, "/api/github/auth/read/start", `{"flow":"device"}`)
	if response.StatusCode != http.StatusAccepted || !strings.Contains(string(body), "ABCD-1234") || strings.Contains(string(body), "fixture-secret") {
		t.Fatalf("device fallback start=%d %s", response.StatusCode, body)
	}
	response, body = request(http.MethodPost, "/api/github/auth/read/poll", "{}")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "fixture-reviewer") || strings.Contains(string(body), "fixture-token") {
		t.Fatalf("poll=%d %s", response.StatusCode, body)
	}
	response, body = request(http.MethodDelete, "/api/github/auth/read", "")
	if response.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("disconnect=%d %s", response.StatusCode, body)
	}
}

func TestAskMetadataRoutesDoNotCreateMessagesOnRead(t *testing.T) {
	dir := t.TempDir()
	ui := filepath.Join(dir, "ui")
	if err := os.MkdirAll(ui, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ui, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: dir, UIDir: ui, GitHubAuth: githubauth.New(authSecrets{}, authConfig{}, authTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("not used") }), func(string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	base := "http://127.0.0.1:" + strconv.Itoa(d.Port())
	request := func(method, path, body string) ([]byte, int) {
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+d.token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		contents, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return contents, response.StatusCode
	}
	body, status := request(http.MethodPost, "/api/ask/conversations", `{"model":"auto","reasoningEffort":"medium","contextTier":"long_context"}`)
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	var created struct {
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
	}
	if json.Unmarshal(body, &created) != nil || created.Conversation.ID == "" {
		t.Fatalf("invalid create %s", body)
	}
	body, status = request(http.MethodGet, "/api/ask/conversations/"+created.Conversation.ID, "")
	if status != http.StatusOK || !strings.Contains(string(body), `"messages":[]`) {
		t.Fatalf("read %d %s", status, body)
	}
	_, status = request(http.MethodPost, "/api/ask/question-sets", `{"name":"Review","questions":["What changed?","Any risks?"]}`)
	if status != http.StatusCreated {
		t.Fatalf("question set=%d", status)
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

// A local PR investigation must use only the daemon-owned read credential,
// materialize the PR in cache, and open the reviewer without manufacturing a
// queue row or exposing a credential to the browser.
func TestLocalReadOnlyPullRequestAndRemoteQueueLifecycle(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "origin.git")
	for _, command := range [][]string{{"init", "-q", "-b", "main", source}, {"init", "--bare", "-q", remote}} {
		if output, err := exec.Command("git", command...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", command, err, output)
		}
	}
	git := func(args ...string) string {
		output, err := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("config", "user.email", "localreview-test@example.invalid")
	git("config", "user.name", "localreview-test")
	if err := os.WriteFile(filepath.Join(source, "review.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "review.txt")
	git("commit", "-qm", "base")
	baseSHA := git("rev-parse", "HEAD")
	git("checkout", "-qb", "feature")
	if err := os.WriteFile(filepath.Join(source, "review.txt"), []byte("base\nfeature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "review.txt")
	git("commit", "-qm", "feature")
	headSHA := git("rev-parse", "HEAD")
	git("remote", "add", "origin", remote)
	git("push", "-q", "origin", baseSHA+":refs/heads/main")
	git("push", "-q", "origin", headSHA+":refs/pull/7/head")

	transport := authTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/repos/acme/widget/pulls/7" {
			return nil, fmt.Errorf("unexpected GitHub endpoint %s", request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer read-token" {
			return nil, fmt.Errorf("missing read credential")
		}
		body := fmt.Sprintf(`{"number":7,"html_url":"https://github.com/acme/widget/pull/7","title":"Feature","body":"PR body","state":"open","draft":false,"head":{"sha":%q,"ref":"feature"},"base":{"sha":%q,"ref":"main","repo":{"full_name":"acme/widget","clone_url":%q}}}`, headSHA, baseSHA, remote)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	secrets := authSecrets{}
	token, _ := json.Marshal(githubauth.Token{AccessToken: "read-token", ClientID: "Iv1.fixtureRead"})
	secrets[githubauth.Service+"/github.com:read"] = string(token)
	auth := githubauth.New(secrets, authConfig{githubauth.Read: "Iv1.fixtureRead"}, transport, func(string) error { return nil })
	data := filepath.Join(root, "daemon")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: data, UIDir: filepath.Join(root, "ui"), GitHubAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	call := func(method, path, payload string) (int, []byte) {
		req, _ := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(d.Port())+path, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+d.token)
		req.Header.Set("Content-Type", "application/json")
		response, callErr := http.DefaultClient.Do(req)
		if callErr != nil {
			t.Fatal(callErr)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return response.StatusCode, body
	}
	status, body := call(http.MethodPost, "/api/local-review/pr", `{"remoteUrl":"https://github.com/acme/widget/pull/7"}`)
	if status != http.StatusOK || !strings.Contains(string(body), "localOnly=1") || strings.Contains(string(body), "read-token") {
		t.Fatalf("open status=%d body=%s", status, body)
	}
	var opened struct {
		Repos []struct {
			ID string `json:"id"`
		} `json:"repos"`
	}
	if json.Unmarshal(body, &opened) != nil || len(opened.Repos) != 1 || opened.Repos[0].ID == "" {
		t.Fatalf("bad open response %s", body)
	}
	// The clean detached worktree has no working-tree changes. The daemon must
	// retain the immutable PR base selected at open time so the default diff is
	// still the PR diff rather than a misleading empty response.
	status, body = call(http.MethodGet, "/api/repos/"+opened.Repos[0].ID+"/api/diff", "")
	if status != http.StatusOK || !strings.Contains(string(body), "feature") {
		t.Fatalf("default PR diff=%d %s", status, body)
	}
	if _, err := queueStore.List(d.db, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM queue_items").Scan(&count); err != nil || count != 0 {
		t.Fatalf("read-only open created queue row count=%d err=%v", count, err)
	}
	status, body = call(http.MethodPost, "/api/queue", `{"remoteUrl":"https://github.com/acme/widget/pull/7"}`)
	if status != http.StatusCreated || !strings.Contains(string(body), headSHA) {
		t.Fatalf("remote queue status=%d body=%s", status, body)
	}
	var queued struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if json.Unmarshal(body, &queued) != nil || queued.Item.ID == "" {
		t.Fatalf("bad queue response %s", body)
	}
	status, body = call(http.MethodGet, "/api/queue/"+queued.Item.ID+"/remote-status", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"worktreePresent":true`) {
		t.Fatalf("status=%d body=%s", status, body)
	}
	status, body = call(http.MethodPost, "/api/queue/"+queued.Item.ID+"/cleanup", `{"removeMirror":true}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"worktreeRemoved":true`) {
		t.Fatalf("cleanup=%d body=%s", status, body)
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

func TestQueueOpenFailureKeepsItemQueuedAndRecoverable(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	item, created, err := queueStore.Enqueue(d.db, queueStore.EnqueueInput{
		Title:                "broken retained snapshot",
		WorkspacePath:        filepath.Join(dir, "unavailable-workspace"),
		SnapshotManifestPath: filepath.Join(dir, "missing-manifest.json"),
		SnapshotManifest:     json.RawMessage(`{"id":"missing-snapshot","repos":[]}`),
	})
	if err != nil || !created || item == nil {
		t.Fatalf("enqueue item=%#v created=%v err=%v", item, created, err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(d.Port())+"/api/queue/"+item.ID+"/open", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "recovery") || !strings.Contains(string(body), "remains queued") {
		t.Fatalf("open response=%d body=%s", response.StatusCode, body)
	}
	persisted, err := queueStore.Get(d.db, item.ID)
	if err != nil || persisted == nil || persisted.Status != queueStore.Queued {
		t.Fatalf("failed open changed durable queue state: item=%#v err=%v", persisted, err)
	}
}

// An explicit GitHub App publication first verifies the immutable snapshot
// head with read authority, then publishes exactly once with write authority,
// and only then records the local transition.
func TestRemotePublishDecisionUsesDedicatedWriteCapability(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var published string
	auth := githubauth.New(authSecrets{
		githubauth.Service + "/github.com:read":  `{"accessToken":"read-token","clientId":"read-client-id"}`,
		githubauth.Service + "/github.com:write": `{"accessToken":"write-token","clientId":"write-client-id"}`,
	}, authConfig{githubauth.Read: "read-client-id", githubauth.Write: "write-client-id"}, authTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet && request.URL.Path == "/repos/acme/widget/pulls/7" {
			if request.Header.Get("Authorization") != "Bearer read-token" {
				t.Fatalf("read authorization=%q", request.Header.Get("Authorization"))
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"number":7,"html_url":"https://github.com/acme/widget/pull/7","title":"Remote PR","state":"open","draft":false,"head":{"sha":"` + head + `","ref":"feature"},"base":{"sha":"` + base + `","ref":"main","repo":{"full_name":"acme/widget","clone_url":"https://github.com/acme/widget.git"}}}`))}, nil
		}
		if request.Method == http.MethodPost && request.URL.Path == "/repos/acme/widget/pulls/7/reviews" {
			if request.Header.Get("Authorization") != "Bearer write-token" {
				t.Fatalf("write authorization=%q", request.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(request.Body)
			published = string(body)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":41}`))}, nil
		}
		return nil, fmt.Errorf("unexpected GitHub request %s %s", request.Method, request.URL)
	}), nil)
	d, err := Start(ctx, Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui"), GitHubAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	item, created, err := queueStore.Enqueue(d.db, queueStore.EnqueueInput{
		Title:            "Remote PR",
		WorkspacePath:    filepath.Join(dir, "remote-worktree"),
		Kind:             "remote",
		RemoteURL:        "https://github.com/acme/widget/pull/7",
		SnapshotManifest: json.RawMessage(`{"remotePullRequest":{"url":"https://github.com/acme/widget/pull/7","number":7,"title":"Remote PR","body":"","state":"OPEN","isDraft":false,"repository":"acme/widget","repositoryUrl":"https://github.com/acme/widget.git","headRefName":"feature","headSha":"` + head + `","baseRefName":"main","baseSha":"` + base + `"}}`),
	})
	if err != nil || !created {
		t.Fatalf("enqueue remote: item=%#v created=%v err=%v", item, created, err)
	}
	if _, err := queueStore.Open(d.db, item.ID); err != nil {
		t.Fatalf("open remote: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(d.Port())+"/api/queue/"+item.ID+"/decision", strings.NewReader(`{"decision":"approved","publish":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"published":true`) || !strings.Contains(published, `"event":"APPROVE"`) || !strings.Contains(published, `"commit_id":"`+head+`"`) {
		t.Fatalf("publish response=%d %s", response.StatusCode, body)
	}
	persisted, err := queueStore.Get(d.db, item.ID)
	if err != nil || persisted == nil || persisted.Status != queueStore.Approved {
		t.Fatalf("published decision must become approved: item=%#v err=%v", persisted, err)
	}
	decisions, err := queueStore.DecisionsForItem(d.db, item.ID)
	if err != nil || len(decisions) != 1 || decisions[0].Status != string(queueStore.Approved) {
		t.Fatalf("published decision history=%#v err=%v", decisions, err)
	}
}

func TestRemotePublishStaleHeadSavesNoLocalDecision(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	writes := 0
	auth := githubauth.New(authSecrets{githubauth.Service + "/github.com:read": `{"accessToken":"read-token","clientId":"read-client-id"}`, githubauth.Service + "/github.com:write": `{"accessToken":"write-token","clientId":"write-client-id"}`}, authConfig{githubauth.Read: "read-client-id", githubauth.Write: "write-client-id"}, authTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			writes++
			return nil, errors.New("write must not happen")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"number":7,"html_url":"https://github.com/acme/widget/pull/7","title":"Remote PR","state":"open","draft":false,"head":{"sha":"cccccccccccccccccccccccccccccccccccccccc","ref":"feature"},"base":{"sha":"` + base + `","ref":"main","repo":{"full_name":"acme/widget","clone_url":"https://github.com/acme/widget.git"}}}`))}, nil
	}), nil)
	d, err := Start(ctx, Options{DataDir: dir, UIDir: filepath.Join(dir, "missing-ui"), GitHubAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	item, _, err := queueStore.Enqueue(d.db, queueStore.EnqueueInput{Title: "Remote PR", WorkspacePath: filepath.Join(dir, "remote"), Kind: "remote", RemoteURL: "https://github.com/acme/widget/pull/7", SnapshotManifest: json.RawMessage(`{"remotePullRequest":{"url":"https://github.com/acme/widget/pull/7","number":7,"title":"Remote PR","body":"","state":"OPEN","isDraft":false,"repository":"acme/widget","repositoryUrl":"https://github.com/acme/widget.git","headRefName":"feature","headSha":"` + head + `","baseRefName":"main","baseSha":"` + base + `"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queueStore.Open(d.db, item.ID); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(d.Port())+"/api/queue/"+item.ID+"/decision", strings.NewReader(`{"decision":"changes_requested","publishGitHub":true}`))
	request.Header.Set("Authorization", "Bearer "+d.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "github_review_publish_stale_head") || !strings.Contains(string(body), "Click Refresh PR") || writes != 0 {
		t.Fatalf("status=%d body=%s writes=%d", response.StatusCode, body, writes)
	}
	persisted, err := queueStore.Get(d.db, item.ID)
	if err != nil || persisted == nil || persisted.Status != queueStore.InReview {
		t.Fatalf("item=%#v err=%v", persisted, err)
	}
	decisions, err := queueStore.DecisionsForItem(d.db, item.ID)
	if err != nil || len(decisions) != 0 {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
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

func TestRepoFingerprintChangesWhenModifiedFileContentChanges(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "localreview-test@example.invalid"}, {"config", "user.name", "localreview-test"}} {
		if output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	file := filepath.Join(repo, "review.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "review.txt"}, {"commit", "-qm", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	if err := os.WriteFile(file, []byte("first change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := repoFingerprint(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("second change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := repoFingerprint(repo)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("fingerprint did not change for content-only edit while porcelain status stayed modified")
	}
}

func TestQueueHookAndWatcherHTTPContract(t *testing.T) {
	workspace := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "localreview-test@example.invalid"}, {"config", "user.name", "localreview-test"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "review.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "review.txt"}, {"commit", "-qm", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	d, err := Start(context.Background(), Options{DataDir: t.TempDir(), UIDir: filepath.Join(t.TempDir(), "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	call := func(path, payload string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", d.Port(), path), strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+d.token)
		req.Header.Set("Content-Type", "application/json")
		res, callErr := http.DefaultClient.Do(req)
		if callErr != nil {
			t.Fatal(callErr)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, body
	}
	payload := `{"workspacePath":` + strconv.Quote(workspace) + `,"title":"Hook review"}`
	status, body := call("/api/queue/hook", payload)
	if status != http.StatusCreated || !strings.Contains(string(body), `"created":true`) || !strings.Contains(string(body), `"snapshotManifest"`) {
		t.Fatalf("first hook=%d %s", status, body)
	}
	status, body = call("/api/queue/hook", payload)
	if status != http.StatusOK || !strings.Contains(string(body), `"created":false`) {
		t.Fatalf("duplicate hook=%d %s", status, body)
	}
	status, body = call("/api/queue/watch", `{"workspacePath":`+strconv.Quote(workspace)+`,"pollIntervalMs":1}`)
	if status != http.StatusCreated || !strings.Contains(string(body), `"pollIntervalMs":1000`) {
		t.Fatalf("enable watch=%d %s", status, body)
	}
	status, body = call("/api/queue/watch", `{"workspacePath":`+strconv.Quote(workspace)+`,"enabled":false}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"enabled":false`) {
		t.Fatalf("disable watch=%d %s", status, body)
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
	for _, expectation := range []struct{ path, contains string }{
		{"/api/export/prompt", "tools/child/review.txt:L1\nPlease clarify"},
		{"/api/repos/" + childID + "/api/comments-output", ""},
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
	req, _ = http.NewRequest(http.MethodGet, base+"/api/repos/"+childID+"/api/comments", nil)
	req.Header.Set("Authorization", "Bearer "+discovered.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Please clarify") {
		t.Fatalf("comments API read status=%d body=%s", response.StatusCode, body)
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

func TestDaemonRestartRestoresActiveWorkspaceAndReviewSession(t *testing.T) {
	workspace := t.TempDir()
	for _, arguments := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "test"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nconst v = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "main.go"}, {"commit", "-qm", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", workspace}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", arguments, err, output)
		}
	}
	data := t.TempDir()
	first, err := Start(context.Background(), Options{DataDir: data, UIDir: filepath.Join(data, "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.activateWorkspace(workspace, "restart fixture"); err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, err := safeWorkspacePath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	sessionID := first.review.SessionID
	first.mu.Unlock()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Start(context.Background(), Options{DataDir: data, UIDir: filepath.Join(data, "missing-ui")})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.mu.Lock()
	restored := second.review
	second.mu.Unlock()
	if restored == nil || restored.Root != canonicalWorkspace || restored.SessionID != sessionID || len(restored.Repos) != 1 {
		t.Fatalf("restored review=%#v, want workspace=%q session=%d", restored, canonicalWorkspace, sessionID)
	}
}

func TestCommentImportValidationAndStableGeneratedID(t *testing.T) {
	entry := commentImport{Type: "thread", FilePath: "pkg/example.go", Body: "looks risky", Author: "octo"}
	entry.Position.Side = "new"
	entry.Position.Line = json.RawMessage(`{"start":4,"end":6}`)
	start, end, err := validateImport(entry)
	if err != nil || start != 4 || end != 6 {
		t.Fatalf("validated import start=%d end=%d err=%v", start, end, err)
	}
	if got, again := importID(entry, start, end), importID(entry, start, end); got == "" || got != again {
		t.Fatalf("unstable generated import ID %q / %q", got, again)
	}
	entry.Position.Side = "invalid"
	if _, _, err := validateImport(entry); err == nil {
		t.Fatal("invalid side accepted")
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
