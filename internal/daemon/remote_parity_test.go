package daemon

// This test turns the frozen remote-PR capture into an executable native
// lifecycle check.  The capture's TS paths are intentionally not recreated;
// instead, the same user-visible contract is driven through the production Go
// routes with a disposable bare Git remote and a daemon-owned read credential.
// That leaves Phase 4 free to remove the TS capture harness while preserving a
// real check for every captured remote-review transition.

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

	"github.com/uhvesta/cmux-localreview/internal/githubauth"
)

func TestFrozenRemotePullRequestLifecycleParity(t *testing.T) {
	fixtures := loadFrozenRemoteParityFixtures(t)
	byName := make(map[string]frozenParityFixture, len(fixtures))
	for _, fixture := range fixtures {
		if _, duplicate := byName[fixture.Name]; duplicate {
			t.Fatalf("remote parity corpus has duplicate fixture %q", fixture.Name)
		}
		byName[fixture.Name] = fixture
	}
	for _, name := range []string{
		"configure_read", "start_read", "poll_read", "queue_remote_pr",
		"remote_status", "open_remote", "cleanup_remote",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("remote parity corpus missing lifecycle fixture %q", name)
		}
	}
	if len(byName) != 7 {
		t.Fatalf("remote parity corpus has %d fixtures; add an explicit native replay before changing it", len(byName))
	}

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
	git("config", "user.email", "remote-parity@example.invalid")
	git("config", "user.name", "remote parity")
	if err := os.WriteFile(filepath.Join(source, "review.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "review.txt")
	git("commit", "-qm", "base")
	baseSHA := git("rev-parse", "HEAD")
	git("checkout", "-qb", "fixture")
	if err := os.WriteFile(filepath.Join(source, "review.txt"), []byte("base\nfeature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "review.txt")
	git("commit", "-qm", "fixture")
	headSHA := git("rev-parse", "HEAD")
	git("remote", "add", "origin", remote)
	git("push", "-q", "origin", baseSHA+":refs/heads/main")
	git("push", "-q", "origin", headSHA+":refs/pull/42/head")

	transport := authTransport(func(request *http.Request) (*http.Response, error) {
		body := ""
		switch request.URL.Path {
		case "/login/device/code":
			body = `{"device_code":"remote-device","user_code":"REMOTE-123","verification_uri":"https://github.com/login/device","expires_in":900}`
		case "/login/oauth/access_token":
			body = `{"access_token":"remote-read-token"}`
		case "/user":
			body = `{"login":"remote-fixture"}`
		case "/repos/fixture/repository/pulls/42":
			if request.Header.Get("Authorization") != "Bearer remote-read-token" {
				return nil, fmt.Errorf("remote parity request lacks daemon-owned read credential")
			}
			body = fmt.Sprintf(`{"number":42,"html_url":"https://github.com/fixture/repository/pull/42","title":"Fixture remote PR","body":"","state":"open","draft":false,"head":{"sha":%q,"ref":"fixture"},"base":{"sha":%q,"ref":"main","repo":{"full_name":"fixture/repository","clone_url":%q}}}`, headSHA, baseSHA, remote)
		default:
			return nil, fmt.Errorf("unexpected remote parity endpoint %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := Start(ctx, Options{
		DataDir:    filepath.Join(root, "daemon"),
		UIDir:      filepath.Join(root, "ui"),
		GitHubAuth: githubauth.New(authSecrets{}, authConfig{}, transport, func(string) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	call := func(fixture string, method string, path string, body string) []byte {
		t.Helper()
		req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(d.Port())+path, strings.NewReader(body))
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
		contents, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		want := byName[fixture].Response
		if response.StatusCode != want.Status {
			t.Fatalf("%s: status got=%d want=%d body=%s", fixture, response.StatusCode, want.Status, contents)
		}
		if want.ContentType == nil {
			if got := response.Header.Get("Content-Type"); got != "" {
				t.Fatalf("%s: content type got=%q want empty", fixture, got)
			}
		} else if got := response.Header.Get("Content-Type"); got != *want.ContentType {
			t.Fatalf("%s: content type got=%q want=%q", fixture, got, *want.ContentType)
		}
		return contents
	}

	// The captured TS fixture started the old device flow by default. Native
	// defaults to PKCE loopback, so explicitly request the preserved headless
	// fallback while retaining every remaining captured route/method/status.
	configure := byName["configure_read"]
	configureBody, err := json.Marshal(configure.Request.Body)
	if err != nil {
		t.Fatal(err)
	}
	call("configure_read", configure.Request.Method, configure.Request.Path, string(configureBody))
	startBody := `{"flow":"device"}`
	start := call("start_read", byName["start_read"].Request.Method, byName["start_read"].Request.Path, startBody)
	if !strings.Contains(string(start), "REMOTE-123") || !strings.Contains(string(start), `"flow":"device"`) {
		t.Fatalf("start_read did not expose native device fallback: %s", start)
	}
	poll := call("poll_read", byName["poll_read"].Request.Method, byName["poll_read"].Request.Path, "{}")
	if !strings.Contains(string(poll), "remote-fixture") || strings.Contains(string(poll), "remote-read-token") {
		t.Fatalf("poll_read leaked or omitted credential state: %s", poll)
	}

	queueFixture := byName["queue_remote_pr"]
	queueBody, err := json.Marshal(queueFixture.Request.Body)
	if err != nil {
		t.Fatal(err)
	}
	queued := call("queue_remote_pr", queueFixture.Request.Method, queueFixture.Request.Path, string(queueBody))
	var queueResponse struct {
		Item struct {
			ID            string `json:"id"`
			WorkspacePath string `json:"workspacePath"`
			Kind          string `json:"kind"`
			RemoteURL     string `json:"remoteUrl"`
			Status        string `json:"status"`
		} `json:"item"`
	}
	if err := json.Unmarshal(queued, &queueResponse); err != nil {
		t.Fatal(err)
	}
	if queueResponse.Item.ID == "" || queueResponse.Item.Kind != "remote" || queueResponse.Item.RemoteURL != "https://github.com/fixture/repository/pull/42" || queueResponse.Item.Status != "queued" || !strings.Contains(queueResponse.Item.WorkspacePath, "pr-42") {
		t.Fatalf("queue_remote_pr produced incomplete native item: %s", queued)
	}

	itemPath := func(name string) string {
		return strings.ReplaceAll(byName[name].Request.Path, "<uuid>", queueResponse.Item.ID)
	}
	status := call("remote_status", byName["remote_status"].Request.Method, itemPath("remote_status"), "")
	if !strings.Contains(string(status), `"mirrorPresent":true`) || !strings.Contains(string(status), `"worktreePresent":true`) || !strings.Contains(string(status), `"number":42`) {
		t.Fatalf("remote_status did not describe the cached PR: %s", status)
	}
	opened := call("open_remote", byName["open_remote"].Request.Method, itemPath("open_remote"), "{}")
	if !strings.Contains(string(opened), `"status":"in_review"`) || !strings.Contains(string(opened), `"reviewUrl":"/review?queueItem=`) {
		t.Fatalf("open_remote did not activate native review: %s", opened)
	}
	cleanup := call("cleanup_remote", byName["cleanup_remote"].Request.Method, itemPath("cleanup_remote"), `{"removeMirror":true}`)
	if !strings.Contains(string(cleanup), `"worktreeRemoved":true`) || !strings.Contains(string(cleanup), `"mirrorRemoved":true`) {
		t.Fatalf("cleanup_remote did not remove only managed cache paths: %s", cleanup)
	}
}

func loadFrozenRemoteParityFixtures(t *testing.T) []frozenParityFixture {
	t.Helper()
	contents, err := os.ReadFile(findFrozenParityCorpusFile(t, "remote-pr.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus frozenParityCorpus
	if err := json.Unmarshal(contents, &corpus); err != nil {
		t.Fatalf("decode frozen remote parity corpus: %v", err)
	}
	if len(corpus.Fixtures) == 0 {
		t.Fatal("frozen remote parity corpus is empty")
	}
	return corpus.Fixtures
}
