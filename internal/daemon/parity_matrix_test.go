package daemon

// This file is deliberately a consumer of the checked-in TypeScript capture,
// not a second hand-written HTTP contract.  The frozen capture is the oracle;
// the matrix below says which rows are executable today and why every other
// row is intentionally deferred or intentionally different.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/githubauth"
)

type frozenParityCorpus struct {
	Fixtures []frozenParityFixture `json:"fixtures"`
}

type frozenParityFixture struct {
	Name    string `json:"name"`
	Request struct {
		Method        string `json:"method"`
		Path          string `json:"path"`
		Body          any    `json:"body"`
		Authenticated bool   `json:"authenticated"`
	} `json:"request"`
	Response struct {
		Status      int     `json:"status"`
		ContentType *string `json:"contentType"`
		Body        any     `json:"body"`
	} `json:"response"`
}

// parityDisposition is an audit record.  An endpoint cannot silently fall out
// of the parity gate: every frozen fixture must either be replayed below or
// carry one concrete, reviewable reason that it cannot be replayed yet.
type parityDisposition struct {
	Execute                   bool
	Reason                    string
	ForceDaemonCapability     bool
	DeviceFlow                bool
	NativeAskSettings         bool
	NativeAskMessageDelivery  bool
	NativeCommentCollection   bool
	NativeQueueWatch          bool
	NativeQuestionSetDelivery bool
	NativeQueueReproduction   bool
	NativeWebsocketDiff       bool
	NativeQueueHook           bool
	NativeFederationLifecycle bool
}

var parityMatrix = map[string]parityDisposition{
	"health":                   {Execute: true},
	"unauthenticated_queue":    {Execute: true},
	"browser_session_exchange": {Execute: true},
	"github_auth_status":       {Execute: true},
	"github_auth_configure":    {Execute: true},
	// The native daemon defaults to registered loopback OAuth; this frozen row
	// explicitly exercises the SSH/headless device fallback.
	"github_auth_device_start":         {Execute: true, DeviceFlow: true, Reason: "Native device-flow fallback."},
	"github_auth_device_poll":          {Execute: true},
	"github_auth_authenticated_status": {Execute: true},
	"github_auth_disconnect":           {Execute: true},
	"local_pr_requires_read_auth":      {Execute: true},
	"workspaces_empty":                 {Execute: true},
	"queue_empty":                      {Execute: true},
	"federation_nodes_empty":           {Execute: true},
	"open_workspace":                   {Execute: true},
	// TS accidentally left these reviewer reads public after workspace opening.
	// Native keeps all /api routes behind a loopback capability, so the exact
	// payload is replayed with that capability rather than weakening the daemon.
	"repos":                       {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"repo_diff":                   {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"repo_diff_ignore_whitespace": {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"repo_line_count":             {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"repo_blob":                   {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"repo_generated_status":       {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"repo_fullfile":               {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"ui_state_empty":              {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"ui_state_put":                {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"export_prompt":               {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"new_session":                 {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"sessions":                    {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},

	"repo_revisions": {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer reads are deliberately capability-protected."},
	// The frozen endpoint returned an Express HTML 404 for both reads. Native
	// serves a durable collection instead; replay the same URL and assert its
	// real empty/saved thread semantics instead of preserving the dead route.
	"repo_comments_empty": {Execute: true, ForceDaemonCapability: true, NativeCommentCollection: true},
	"create_comment":      {Execute: true, ForceDaemonCapability: true, Reason: "Frozen difit short comment shape is translated at the native boundary into a durable formal thread."},
	"repo_comments_saved": {Execute: true, ForceDaemonCapability: true, NativeCommentCollection: true},
	// Import accepts the original compact difit row and stores it as a durable
	// formal thread. The response is intentionally still byte-compatible with
	// the frozen capture, so replay it instead of relying only on unit coverage.
	"comment_import": {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer writes are capability-protected."},
	"review_history": {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer reads are capability-protected."},
	// The frozen browser capture made these reads public. Native keeps /btw
	// behind the daemon capability, but the empty thread projection and missing
	// question validation remain compatible. Native /btw is SDK-only; terminal
	// and ACP delivery are separately rejected with no focused-pane fallback.
	"btw_threads_empty":      {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"btw_ask_validation":     {Execute: true, ForceDaemonCapability: true, Reason: "Native capability boundary is intentionally stricter than frozen TS."},
	"websocket_diff_updated": {Execute: true, NativeWebsocketDiff: true, Reason: "Replayed through the daemon's real Git polling watcher and mounted WebSocket endpoint."},
	// A new review session must expose an empty durable comment collection and
	// retain the old empty comments-output compatibility endpoint. The fixtures
	// assert those lifecycle edges without treating the old projection as the
	// source of truth for native exports.
	"comments_json":                          {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer reads are capability-protected."},
	"comments_output":                        {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer reads are capability-protected."},
	"ask_models":                             {Execute: true, ForceDaemonCapability: true},
	"ask_conversations_empty":                {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer reads are deliberately capability-protected."},
	"ask_question_set_create":                {Execute: true, ForceDaemonCapability: true},
	"ask_question_sets":                      {Execute: true, ForceDaemonCapability: true},
	"ask_question_set_get":                   {Execute: true, ForceDaemonCapability: true},
	"ask_question_set_update":                {Execute: true, ForceDaemonCapability: true},
	"ask_question_set_delete":                {Execute: true, ForceDaemonCapability: true},
	"ask_conversation_create":                {Execute: true, ForceDaemonCapability: true},
	"ask_conversation_get":                   {Execute: true, ForceDaemonCapability: true},
	"ask_inline_conversation_reuses_context": {Execute: true, ForceDaemonCapability: true},
	// The frozen model route cleared thinking/context settings as a side
	// effect. Native picker updates preserve explicit choices, which prevents a
	// reviewer silently losing their requested context window. Replay the
	// historical requests and assert that stronger native contract explicitly.
	"ask_conversation_model":    {Execute: true, ForceDaemonCapability: true, NativeAskSettings: true},
	"ask_conversation_settings": {Execute: true, ForceDaemonCapability: true, NativeAskSettings: true},
	// The native API accepts the turn and exposes streaming through a durable
	// EventSource endpoint. Replay the legacy request and prove its persisted
	// turn and location settle exactly once before a transcript read.
	"ask_conversation_message_sse": {Execute: true, ForceDaemonCapability: true, NativeAskMessageDelivery: true},
	// Idle cancellation is a deterministic safety no-op. Replaying it verifies
	// that a repeated UI action cannot cancel a future prompt after reload.
	"ask_conversation_cancel_idle": {Execute: true, ForceDaemonCapability: true},
	// Native accepts a set turn before the durable EventSource delivers its
	// response. Replay the frozen requests and assert that accepted delivery,
	// the persisted transcript, and a subsequent read are all deterministic.
	"ask_question_set_for_send":       {Execute: true, ForceDaemonCapability: true},
	"ask_question_set_combined_sse":   {Execute: true, ForceDaemonCapability: true, NativeQuestionSetDelivery: true, Reason: "Native uses POST-202 plus durable EventSource events instead of response-body SSE."},
	"ask_question_set_sequential_sse": {Execute: true, ForceDaemonCapability: true, NativeQuestionSetDelivery: true, Reason: "Native uses POST-202 plus durable EventSource events instead of response-body SSE."},
	// Fresh/history are metadata-only lifecycle routes. They archive or read
	// persisted conversations; neither opens an SDK session nor replays a
	// prompt, so the frozen lifecycle can be replayed deterministically.
	"ask_conversation_fresh":   {Execute: true, ForceDaemonCapability: true},
	"ask_conversation_history": {Execute: true, ForceDaemonCapability: true},
	"queue_create_local":       {Execute: true},
	"queue_list_with_item":     {Execute: true},
	"queue_detail":             {Execute: true},
	"queue_reorder":            {Execute: true},
	"queue_add_feedback":       {Execute: true},
	"queue_feedback_prompt":    {Execute: true},
	// ACP is a deliberate non-goal of the Go migration. The native reproduce
	// plan exposes a fresh SDK-native /ask session instead of advertising a
	// resumable terminal protocol that no longer exists; see
	// TestQueueControlPlaneHTTPContract and
	// TestNativeQueuePackageExportIsPortableAndAtomic.
	"queue_reproduce": {Execute: true, NativeQueueReproduction: true, Reason: "Native reproduction deliberately gives a fresh SDK-native /ask plan rather than retired ACP resume fields."},
	"queue_export":    {Execute: true},
	"queue_open":      {Execute: true},
	"queue_complete":  {Execute: true},
	"queue_requeue":   {Execute: true},
	"queue_delete":    {Execute: true},
	"queue_history":   {Execute: true},
	// The native watcher exposes a deterministic no-op poll seam in this
	// matrix. These rows prove real DB registration and cancellation lifecycle
	// without a wall-clock sleep or a synthetic Git snapshot.
	"queue_watch_enable":  {Execute: true, NativeQueueWatch: true},
	"queue_watch_disable": {Execute: true, NativeQueueWatch: true},
	// The frozen hook row follows an older removed queue item. Native retains
	// the item's stable identity rather than fabricating a third queue round;
	// replay the same request and assert the stronger idempotency/provenance
	// contract explicitly.
	"queue_hook":      {Execute: true, NativeQueueHook: true},
	"agent_register":  {Execute: true},
	"agent_list":      {Execute: true},
	"agent_heartbeat": {Execute: true},
	"agent_reconnect": {Execute: true},
	// These lifecycle rows run against the production native route and durable
	// store. The aggregate request follows disconnect, so it intentionally
	// proves the safe no-tunnel path; fake-loopback transport coverage remains
	// in TestFederationNodeCRUDAndNativeLoopbackTransport. This is not an SSH
	// host validation claim.
	"federation_node_create":     {Execute: true, NativeFederationLifecycle: true},
	"federation_node_status":     {Execute: true, NativeFederationLifecycle: true},
	"federation_node_disconnect": {Execute: true, NativeFederationLifecycle: true},
	"federation_aggregate_queue": {Execute: true, NativeFederationLifecycle: true},
	"federation_node_delete":     {Execute: true, NativeFederationLifecycle: true},
}

func TestFrozenTypeScriptParityMatrix(t *testing.T) {
	corpus := loadFrozenParityCorpus(t)
	byName := map[string]frozenParityFixture{}
	for _, fixture := range corpus.Fixtures {
		byName[fixture.Name] = fixture
		disposition, ok := parityMatrix[fixture.Name]
		if !ok {
			t.Fatalf("frozen fixture %q has no Go parity disposition", fixture.Name)
		}
		if !disposition.Execute && strings.TrimSpace(disposition.Reason) == "" {
			t.Fatalf("fixture %q is not executable and has no explicit exception", fixture.Name)
		}
	}
	for name := range parityMatrix {
		if _, ok := byName[name]; !ok {
			t.Fatalf("parity matrix contains unknown frozen fixture %q", name)
		}
	}

	workspace := makeFrozenFixtureWorkspace(t)
	d := startFrozenParityDaemon(t)
	state := map[string]string{"<fixture-root>": filepath.Dir(workspace), "<fixture-root>/workspace": workspace}
	for _, name := range []string{
		"health", "unauthenticated_queue", "browser_session_exchange",
		"github_auth_status", "github_auth_configure", "github_auth_device_start", "github_auth_device_poll", "github_auth_authenticated_status", "github_auth_disconnect",
		"local_pr_requires_read_auth", "workspaces_empty", "queue_empty", "federation_nodes_empty", "federation_node_create", "federation_node_status", "federation_node_disconnect", "federation_aggregate_queue", "federation_node_delete", "open_workspace", "repos",
		"repo_diff", "repo_diff_ignore_whitespace", "repo_revisions", "websocket_diff_updated", "repo_line_count", "repo_blob", "repo_generated_status", "repo_fullfile", "repo_comments_empty", "create_comment", "repo_comments_saved", "comment_import",
		"sessions", "review_history", "btw_threads_empty", "btw_ask_validation", "ui_state_empty", "ui_state_put", "export_prompt", "new_session", "comments_json", "comments_output",
		"ask_models", "ask_conversations_empty", "ask_question_set_create", "ask_question_sets", "ask_question_set_get", "ask_question_set_update", "ask_question_set_delete", "ask_conversation_create", "ask_conversation_get", "ask_inline_conversation_reuses_context", "ask_conversation_model", "ask_conversation_settings", "ask_conversation_message_sse", "ask_conversation_cancel_idle", "ask_question_set_for_send", "ask_question_set_combined_sse", "ask_question_set_sequential_sse", "ask_conversation_fresh", "ask_conversation_history",
		"queue_create_local", "queue_list_with_item", "queue_detail", "queue_reorder", "queue_add_feedback", "queue_feedback_prompt", "queue_reproduce", "queue_export", "queue_open", "queue_complete", "queue_requeue", "queue_delete", "queue_history", "queue_watch_enable", "queue_watch_disable", "queue_hook",
		"agent_register", "agent_list", "agent_heartbeat", "agent_reconnect",
	} {
		fixture := byName[name]
		disposition := parityMatrix[name]
		response := replayFrozenFixture(t, d, fixture, disposition, state)
		if disposition.NativeWebsocketDiff {
			assertNativeWebsocketDiffFixture(t, d, fixture, state)
		} else if disposition.NativeFederationLifecycle {
			assertNativeFederationLifecycleFixture(t, fixture, response)
		} else if disposition.NativeQueueHook {
			assertNativeQueueHookFixture(t, fixture, response, state)
		} else if disposition.NativeAskSettings {
			assertNativeAskSettingsFixture(t, fixture, response)
		} else if disposition.NativeCommentCollection {
			assertNativeCommentCollectionFixture(t, fixture, response)
		} else if disposition.NativeQueueWatch {
			assertNativeQueueWatchFixture(t, d, fixture, response, state)
		} else if disposition.NativeAskMessageDelivery {
			assertNativeAskMessageDeliveryFixture(t, d, fixture, response, state)
		} else if disposition.NativeQuestionSetDelivery {
			assertNativeQuestionSetDeliveryFixture(t, d, fixture, response, state)
		} else if disposition.NativeQueueReproduction {
			assertNativeQueueReproductionFixture(t, fixture, response, state)
		} else {
			assertFrozenFixtureResponse(t, fixture, response)
		}
		if name == "repos" {
			var body struct {
				Repos []struct {
					ID string `json:"id"`
				} `json:"repos"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Repos) == 0 || body.Repos[0].ID == "" {
				t.Fatalf("repos did not yield a replayable repository id: %v %s", err, response.Body.String())
			}
			state["<short-id>"] = body.Repos[0].ID
		}
		if name == "queue_create_local" {
			state["<uuid>"] = frozenResponseID(t, name, response.Body.Bytes(), "item")
		}
		if name == "ask_question_set_create" || name == "ask_conversation_create" || name == "ask_question_set_for_send" {
			field := map[string]string{"ask_question_set_create": "questionSet", "ask_conversation_create": "conversation", "ask_question_set_for_send": "questionSet"}[name]
			id := frozenResponseID(t, name, response.Body.Bytes(), field)
			state["<uuid>"] = id
			if name == "ask_conversation_create" {
				state["<conversation-id>"] = id
			}
			if name == "ask_question_set_for_send" {
				state["<question-set-id>"] = id
			}
		}
	}
}

func loadFrozenParityCorpus(t *testing.T) frozenParityCorpus {
	t.Helper()
	path := findParityFixture(t)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus frozenParityCorpus
	if err := json.Unmarshal(contents, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Fixtures) == 0 {
		t.Fatal("frozen parity corpus is empty")
	}
	return corpus
}

func findParityFixture(t *testing.T) string {
	t.Helper()
	for dir, depth := ".", 0; depth < 8; dir, depth = filepath.Join(dir, ".."), depth+1 {
		candidate := filepath.Join(dir, "testdata", "parity", "ts-final", "http.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatal("could not locate testdata/parity/ts-final/http.json; Bazel test must expose the frozen corpus as runfile data")
	return ""
}

func startFrozenParityDaemon(t *testing.T) *Daemon {
	t.Helper()
	transport := authTransport(func(request *http.Request) (*http.Response, error) {
		body := `{"access_token":"fixture-token"}`
		switch request.URL.Path {
		case "/login/device/code":
			body = `{"device_code":"device","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900}`
		case "/user":
			body = `{"login":"fixture-reviewer"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: ioNopCloser{strings.NewReader(body)}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := Start(ctx, Options{DataDir: t.TempDir(), GitHubAuth: githubauth.New(authSecrets{}, authConfig{}, transport, func(string) error { return nil }), FederationSecrets: authSecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	// Model discovery is a client-level SDK operation, not a conversation turn.
	// Keep it hermetic so the frozen model-list row proves the native picker
	// contract without a dedicated credential or network call.
	d.askRuntime = askruntime.New(askRouteBackend{session: &askRouteSession{emit: true}, models: []copilotsdk.ModelInfo{{ID: "fixture-model", Name: "Fixture Model"}}})
	d.askFactory = &AskRuntimeFactory{}
	// Lifecycle rows assert registration/cancellation synchronously; they do
	// not need a timer tick or a captured snapshot to prove the route works.
	d.queueWatchPoll = func(string) {}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// ioNopCloser avoids importing the full io package solely for a test fixture.
type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }

func makeFrozenFixtureWorkspace(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	init := func(path, file, contents string) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "fixture"}} {
			if output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, output)
			}
		}
		if err := os.WriteFile(filepath.Join(path, file), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "fixture"}} {
			if output, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, output)
			}
		}
	}
	init(root, "root.ts", "export const root = 1;\n")
	init(filepath.Join(root, "nested"), "nested.ts", "export const nested = 2;\n")
	if err := os.WriteFile(filepath.Join(root, "root.ts"), []byte("export const root = 3;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func replayFrozenFixture(t *testing.T, d *Daemon, fixture frozenParityFixture, disposition parityDisposition, state map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	path := replaceFrozenValues(fixture.Request.Path, state)
	body := ""
	if fixture.Request.Body != nil {
		encoded, err := json.Marshal(fixture.Request.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = string(encoded)
	}
	if disposition.NativeAskMessageDelivery {
		conversationID := state["<conversation-id>"]
		if conversationID == "" {
			t.Fatalf("%s lacks native ask-message replay state", fixture.Name)
		}
		path = strings.ReplaceAll(fixture.Request.Path, "<uuid>", conversationID)
		body = strings.ReplaceAll(body, "<uuid>", conversationID)
		body = strings.ReplaceAll(body, `\u003cuuid\u003e`, conversationID)
		// The frozen browser bundle used prompt and expected a response-body SSE
		// stream. Native public delivery uses body and returns a durable
		// EventSource handoff instead; preserve the prompt and location while
		// exercising that replacement contract.
		var nativeBody map[string]any
		if err := json.Unmarshal([]byte(body), &nativeBody); err != nil {
			t.Fatalf("%s: invalid native ask-message body: %v", fixture.Name, err)
		}
		nativeBody["body"] = nativeBody["prompt"]
		delete(nativeBody, "prompt")
		encoded, err := json.Marshal(nativeBody)
		if err != nil {
			t.Fatal(err)
		}
		body = string(encoded)
	} else if disposition.NativeQuestionSetDelivery {
		questionSetID, conversationID := state["<question-set-id>"], state["<conversation-id>"]
		if questionSetID == "" || conversationID == "" {
			t.Fatalf("%s lacks native question-set replay state: set=%q conversation=%q", fixture.Name, questionSetID, conversationID)
		}
		path = strings.ReplaceAll(fixture.Request.Path, "<uuid>", questionSetID)
		body = strings.ReplaceAll(body, "<uuid>", conversationID)
		body = strings.ReplaceAll(body, `\u003cuuid\u003e`, conversationID)
	} else {
		body = replaceFrozenValues(body, state)
	}
	// A path substituted into the fixture body must remain visible to the
	// request decoder; keep this assertion close to the replay boundary so a
	// future normalization change cannot silently test an empty request.
	if fixture.Name == "open_workspace" && !strings.Contains(body, state["<fixture-root>/workspace"]) {
		t.Fatalf("open_workspace fixture substitution failed: %s", body)
	}
	if disposition.DeviceFlow {
		body = `{"flow":"device"}`
	}
	req := httptest.NewRequest(fixture.Request.Method, "http://local.test"+path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if fixture.Request.Authenticated || disposition.ForceDaemonCapability {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	result := httptest.NewRecorder()
	d.server.Handler.ServeHTTP(result, req)
	return result
}

// assertNativeWebsocketDiffFixture exercises the same observable chain as a
// reviewer: an actual WebSocket upgrade to the mounted daemon endpoint,
// followed by a Git source mutation and the daemon's polling invalidation.
// Calling Hub.Broadcast directly would only prove the hub; this proves that
// workspace activation started the watcher and that its emitted frame reaches
// a real client with the frozen wire shape.
func assertNativeWebsocketDiffFixture(t *testing.T, d *Daemon, fixture frozenParityFixture, state map[string]string) {
	t.Helper()
	if fixture.Request.Method != "WEBSOCKET" {
		t.Fatalf("%s: expected websocket fixture", fixture.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("ws://127.0.0.1:%d%s", d.Port(), fixture.Request.Path)
	connection, response, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		t.Fatalf("%s: websocket dial %s: %v", fixture.Name, endpoint, err)
	}
	defer connection.CloseNow()
	if response == nil || response.StatusCode != fixture.Response.Status {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("%s: upgrade status got=%d want=%d", fixture.Name, status, fixture.Response.Status)
	}
	for d.ws.ClientCount() != 1 {
		if ctx.Err() != nil {
			t.Fatalf("%s: websocket client did not register", fixture.Name)
		}
		time.Sleep(time.Millisecond)
	}
	d.mu.Lock()
	review := d.review
	repos := []reviewRepo(nil)
	if review != nil {
		repos = append(repos, review.Repos...)
	}
	d.mu.Unlock()
	if review == nil || len(repos) == 0 {
		t.Fatalf("%s: no active watched repositories", fixture.Name)
	}
	fingerprints := make(map[string]string, len(repos))
	for _, repo := range repos {
		value, err := repoFingerprint(repo.AbsolutePath)
		if err != nil {
			t.Fatalf("%s: baseline fingerprint %s: %v", fixture.Name, repo.AbsolutePath, err)
		}
		fingerprints[repo.ID] = value
	}
	workspace := state["<fixture-root>/workspace"]
	if err := os.WriteFile(filepath.Join(workspace, "root.ts"), []byte("export const root = 4;\n"), 0o600); err != nil {
		t.Fatalf("%s: mutate watched fixture: %v", fixture.Name, err)
	}
	// Invoke the same one-tick production watcher operation immediately. The
	// production goroutine remains exercised elsewhere; this keeps the frozen
	// wire replay deterministic instead of sleeping for its ticker.
	d.pollDiffWatcher(repos, fingerprints)
	_, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatalf("%s: read watcher frame: %v", fixture.Name, err)
	}
	var got any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("%s: decode watcher frame %q: %v", fixture.Name, payload, err)
	}
	assertJSONShape(t, fixture.Name, fixture.Response.Body, got)
}

func replaceFrozenValues(value string, state map[string]string) string {
	for needle, replacement := range state {
		value = strings.ReplaceAll(value, needle, replacement)
		// encoding/json HTML-escapes angle brackets in request bodies.  The
		// captured placeholders intentionally use angle brackets, so rewrite the
		// encoded form as well before replaying the HTTP request.
		value = strings.ReplaceAll(value, strings.ReplaceAll(strings.ReplaceAll(needle, "<", `\u003c`), ">", `\u003e`), replacement)
	}
	return value
}

func assertFrozenFixtureResponse(t *testing.T, fixture frozenParityFixture, actual *httptest.ResponseRecorder) {
	t.Helper()
	if actual.Code != fixture.Response.Status {
		t.Fatalf("%s: status got=%d want=%d body=%s", fixture.Name, actual.Code, fixture.Response.Status, actual.Body.String())
	}
	if fixture.Response.ContentType == nil {
		if got := actual.Header().Get("Content-Type"); got != "" {
			t.Fatalf("%s: unexpected content type %q", fixture.Name, got)
		}
		return
	}
	if got := actual.Header().Get("Content-Type"); got != *fixture.Response.ContentType {
		t.Fatalf("%s: content type got=%q want=%q", fixture.Name, got, *fixture.Response.ContentType)
	}
	if _, jsonResponse := fixture.Response.Body.(map[string]any); jsonResponse {
		var got any
		if err := json.Unmarshal(actual.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: invalid JSON %v: %s", fixture.Name, err, actual.Body.String())
		}
		assertJSONShape(t, fixture.Name, fixture.Response.Body, got)
	}
}

// assertNativeFederationLifecycleFixture replays the frozen create/status/
// disconnect/aggregate/delete sequence through the native API. It deliberately
// does not substitute an SSH process: after disconnect an aggregate must not
// attempt a tunnel at all. The separate federation transport test exercises a
// loopback transport; a real remote SSH host remains a manual release gate.
func assertNativeFederationLifecycleFixture(t *testing.T, fixture frozenParityFixture, actual *httptest.ResponseRecorder) {
	t.Helper()
	if actual.Code != fixture.Response.Status {
		t.Fatalf("%s: status got=%d want=%d body=%s", fixture.Name, actual.Code, fixture.Response.Status, actual.Body.String())
	}
	if fixture.Response.ContentType == nil {
		if got := actual.Header().Get("Content-Type"); got != "" {
			t.Fatalf("%s: unexpected content type %q", fixture.Name, got)
		}
		return
	}
	if got := actual.Header().Get("Content-Type"); got != *fixture.Response.ContentType {
		t.Fatalf("%s: content type got=%q want=%q", fixture.Name, got, *fixture.Response.ContentType)
	}

	var payload map[string]any
	if err := json.Unmarshal(actual.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: invalid federation JSON: %v", fixture.Name, err)
	}
	if strings.Contains(actual.Body.String(), "fixture-node-token") {
		t.Fatalf("%s: browser response leaked the remote daemon capability", fixture.Name)
	}
	object := func(name string) map[string]any {
		value, ok := payload[name].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing %s object: %s", fixture.Name, name, actual.Body.String())
		}
		return value
	}
	assertNode := func(node map[string]any, enabled bool) {
		t.Helper()
		if node["id"] != "fixture-node" || node["label"] != "fixture remote" || node["sshTarget"] != "fixture@localhost" || node["remotePort"] != float64(57140) || node["enabled"] != enabled || node["lastError"] != nil {
			t.Fatalf("%s: unsafe or incomplete federation node: %#v", fixture.Name, node)
		}
	}
	assertRuntime := func(runtime map[string]any, state string) {
		t.Helper()
		if runtime["id"] != "fixture-node" || runtime["state"] != state || runtime["localPort"] != nil || runtime["cachedResponses"] != float64(0) || runtime["lastError"] != nil || runtime["available"] != true {
			t.Fatalf("%s: unexpected federation runtime: %#v", fixture.Name, runtime)
		}
	}

	switch fixture.Name {
	case "federation_node_create":
		assertNode(object("node"), true)
	case "federation_node_status":
		assertNode(object("node"), true)
		assertRuntime(object("runtime"), "disconnected")
	case "federation_node_disconnect":
		assertNode(object("node"), false)
		assertRuntime(object("runtime"), "disabled")
	case "federation_aggregate_queue":
		if payload["transportAvailable"] != true {
			t.Fatalf("%s: transport availability missing: %s", fixture.Name, actual.Body.String())
		}
		nodes, ok := payload["nodes"].([]any)
		if !ok || len(nodes) != 1 {
			t.Fatalf("%s: disabled saved node should remain visible without fetching: %s", fixture.Name, actual.Body.String())
		}
		row, ok := nodes[0].(map[string]any)
		if !ok {
			t.Fatalf("%s: invalid aggregate row: %#v", fixture.Name, nodes[0])
		}
		rowNode, ok := row["node"].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing aggregate node: %#v", fixture.Name, row)
		}
		rowRuntime, ok := row["runtime"].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing aggregate runtime: %#v", fixture.Name, row)
		}
		assertNode(rowNode, false)
		assertRuntime(rowRuntime, "disabled")
		items, ok := row["items"].([]any)
		if !ok || len(items) != 0 {
			t.Fatalf("%s: disabled node must not fetch a remote queue: %#v", fixture.Name, row)
		}
	default:
		t.Fatalf("unexpected federation lifecycle fixture %q", fixture.Name)
	}
}

// assertNativeAskSettingsFixture is a compatibility adapter, not a relaxed
// assertion. The frozen TS contract reset explicit settings when only a model
// was changed. Native `/ask` intentionally preserves them so a reviewer does
// not silently lose thinking/context preferences. We still replay the frozen
// request, status, content type, and conversation envelope, then require the
// stronger persisted values at the API boundary.
func assertNativeAskSettingsFixture(t *testing.T, fixture frozenParityFixture, actual *httptest.ResponseRecorder) {
	t.Helper()
	if actual.Code != fixture.Response.Status {
		t.Fatalf("%s: status got=%d want=%d body=%s", fixture.Name, actual.Code, fixture.Response.Status, actual.Body.String())
	}
	if fixture.Response.ContentType == nil || actual.Header().Get("Content-Type") != *fixture.Response.ContentType {
		want := ""
		if fixture.Response.ContentType != nil {
			want = *fixture.Response.ContentType
		}
		t.Fatalf("%s: content type got=%q want=%q", fixture.Name, actual.Header().Get("Content-Type"), want)
	}
	var response struct {
		Conversation struct {
			ID              string  `json:"id"`
			Model           *string `json:"model"`
			ReasoningEffort *string `json:"reasoningEffort"`
			ContextTier     *string `json:"contextTier"`
		} `json:"conversation"`
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s: invalid JSON: %v", fixture.Name, err)
	}
	if strings.TrimSpace(response.Conversation.ID) == "" || response.Conversation.Model == nil || response.Conversation.ReasoningEffort == nil || response.Conversation.ContextTier == nil {
		t.Fatalf("%s: incomplete native conversation settings: %s", fixture.Name, actual.Body.String())
	}
	request, ok := fixture.Request.Body.(map[string]any)
	if !ok {
		t.Fatalf("%s: frozen request is not an object", fixture.Name)
	}
	model, _ := request["model"].(string)
	if *response.Conversation.Model != model {
		t.Fatalf("%s: model got=%q want=%q", fixture.Name, *response.Conversation.Model, model)
	}
	wantReasoning, wantTier := "high", "long_context" // create fixture values
	if fixture.Name == "ask_conversation_settings" {
		wantReasoning, _ = request["reasoningEffort"].(string)
		wantTier, _ = request["contextTier"].(string)
	}
	if *response.Conversation.ReasoningEffort != wantReasoning || *response.Conversation.ContextTier != wantTier {
		t.Fatalf("%s: native picker settings got=(%q,%q) want=(%q,%q)", fixture.Name, *response.Conversation.ReasoningEffort, *response.Conversation.ContextTier, wantReasoning, wantTier)
	}
}

// assertNativeAskMessageDeliveryFixture replaces the historical response-body
// SSE contract with the native POST-202 plus durable EventSource design. This
// is deliberately hermetic: the parity daemon uses its deterministic fake
// runtime, so it proves persistence and replay safety without claiming a live
// Copilot response.
func assertNativeAskMessageDeliveryFixture(t *testing.T, d *Daemon, fixture frozenParityFixture, actual *httptest.ResponseRecorder, state map[string]string) {
	t.Helper()
	if actual.Code != http.StatusAccepted {
		t.Fatalf("%s: native accepted-delivery status got=%d want=%d body=%s", fixture.Name, actual.Code, http.StatusAccepted, actual.Body.String())
	}
	if got := actual.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("%s: native accepted-delivery content type got=%q", fixture.Name, got)
	}
	var payload struct {
		Delivery string `json:"delivery"`
		User     struct {
			ConversationID string        `json:"conversationId"`
			Body           string        `json:"body"`
			Location       *ask.Location `json:"location"`
		} `json:"user"`
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: invalid native accepted-delivery JSON: %v", fixture.Name, err)
	}
	conversationID := state["<conversation-id>"]
	request, ok := fixture.Request.Body.(map[string]any)
	if !ok {
		t.Fatalf("%s: frozen request is not an object", fixture.Name)
	}
	wantPrompt, _ := request["prompt"].(string)
	if payload.Delivery != "streaming" || payload.User.ConversationID != conversationID || payload.User.Body != wantPrompt || payload.User.Location == nil || payload.User.Location.FilePath != "root.ts" || payload.User.Location.StartLine != 1 || payload.User.Location.SelectedCode != "export const root = 3;" {
		t.Fatalf("%s: invalid native delivery envelope: %#v", fixture.Name, payload)
	}

	var beforeRead []ask.Message
	deadline := time.Now().Add(time.Second)
	for {
		messages, err := ask.ListMessages(context.Background(), d.db, conversationID)
		if err == nil && len(messages) == 2 && !messages[0].Pending && !messages[1].Pending && messages[0].Role == ask.RoleUser && messages[0].Body == wantPrompt && messages[1].Role == ask.RoleAssistant && messages[1].Body == "Copilot reply" {
			beforeRead = messages
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: native delivery did not settle: messages=%#v err=%v", fixture.Name, messages, err)
		}
		time.Sleep(time.Millisecond)
	}
	read := httptest.NewRecorder()
	requestRead := httptest.NewRequest(http.MethodGet, "http://local.test/api/ask/conversations/"+conversationID, nil)
	requestRead.Header.Set("Authorization", "Bearer "+d.token)
	d.server.Handler.ServeHTTP(read, requestRead)
	if read.Code != http.StatusOK {
		t.Fatalf("%s: transcript read=%d %s", fixture.Name, read.Code, read.Body.String())
	}
	afterRead, err := ask.ListMessages(context.Background(), d.db, conversationID)
	if err != nil || len(afterRead) != len(beforeRead) {
		t.Fatalf("%s: transcript read must not replay: before=%#v after=%#v err=%v", fixture.Name, beforeRead, afterRead, err)
	}
}

// assertNativeQuestionSetDeliveryFixture adapts the retired response-body SSE
// rows to the native durable-stream design. The same frozen request must be
// accepted once, retain its mode and question count, settle the expected turns
// in the existing conversation, and remain a pure transcript read afterward.
func assertNativeQuestionSetDeliveryFixture(t *testing.T, d *Daemon, fixture frozenParityFixture, actual *httptest.ResponseRecorder, state map[string]string) {
	t.Helper()
	if actual.Code != http.StatusAccepted {
		t.Fatalf("%s: native accepted-delivery status got=%d want=%d body=%s", fixture.Name, actual.Code, http.StatusAccepted, actual.Body.String())
	}
	if got := actual.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("%s: native accepted-delivery content type got=%q", fixture.Name, got)
	}
	var payload struct {
		Mode              string `json:"mode"`
		Delivery          string `json:"delivery"`
		QuestionsAccepted int    `json:"questionsAccepted"`
		Remaining         int    `json:"remaining"`
		QuestionSet       struct {
			ID string `json:"id"`
		} `json:"questionSet"`
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: invalid native accepted-delivery JSON: %v", fixture.Name, err)
	}
	wantMode := "combined"
	wantCount, wantRemaining := 2, 0
	if fixture.Name == "ask_question_set_sequential_sse" {
		wantMode, wantRemaining = "sequential", 1
	}
	if payload.Mode != wantMode || payload.Delivery != "streaming" || payload.QuestionsAccepted != wantCount || payload.Remaining != wantRemaining || payload.QuestionSet.ID != state["<question-set-id>"] || payload.Conversation.ID != state["<conversation-id>"] {
		t.Fatalf("%s: invalid native delivery envelope: %#v", fixture.Name, payload)
	}

	conversationID := state["<conversation-id>"]
	wantUserBodies := []string{"Please answer these review questions in order. Keep each answer clearly numbered.\n\n1. What changed?\n2. What should be tested?"}
	if wantMode == "sequential" {
		wantUserBodies = append(wantUserBodies, "What changed?", "What should be tested?")
	}
	var beforeRead []ask.Message
	deadline := time.Now().Add(time.Second)
	for {
		messages, err := ask.ListMessages(context.Background(), d.db, conversationID)
		if err == nil && questionSetTurnsSettled(messages, wantUserBodies) {
			beforeRead = messages
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: native delivery did not settle: messages=%#v err=%v", fixture.Name, messages, err)
		}
		time.Sleep(time.Millisecond)
	}
	read := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://local.test/api/ask/conversations/"+conversationID, nil)
	request.Header.Set("Authorization", "Bearer "+d.token)
	d.server.Handler.ServeHTTP(read, request)
	if read.Code != http.StatusOK {
		t.Fatalf("%s: transcript read=%d %s", fixture.Name, read.Code, read.Body.String())
	}
	afterRead, err := ask.ListMessages(context.Background(), d.db, conversationID)
	if err != nil || len(afterRead) != len(beforeRead) {
		t.Fatalf("%s: transcript read must not replay: before=%#v after=%#v err=%v", fixture.Name, beforeRead, afterRead, err)
	}
}

func questionSetTurnsSettled(messages []ask.Message, wantUserBodies []string) bool {
	userBodies := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Pending {
			return false
		}
		if message.Role == ask.RoleUser {
			userBodies = append(userBodies, message.Body)
		}
	}
	if len(userBodies) < len(wantUserBodies) || len(messages) < len(wantUserBodies)*2 {
		return false
	}
	start := len(userBodies) - len(wantUserBodies)
	for index, want := range wantUserBodies {
		if userBodies[start+index] != want {
			return false
		}
	}
	return true
}

// assertNativeQueueReproductionFixture proves that the frozen queue item
// still produces a safe, materializable plan. Native deliberately removes
// ACP resume commands: opening the reproduced directory starts a fresh SDK
// /ask conversation instead, and this adapter rejects any accidental revival
// of a stale terminal-session instruction.
func assertNativeQueueReproductionFixture(t *testing.T, fixture frozenParityFixture, actual *httptest.ResponseRecorder, state map[string]string) {
	t.Helper()
	if actual.Code != http.StatusOK {
		t.Fatalf("%s: native reproduce status got=%d want=%d body=%s", fixture.Name, actual.Code, http.StatusOK, actual.Body.String())
	}
	if got := actual.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("%s: native reproduce content type got=%q", fixture.Name, got)
	}
	var payload struct {
		ItemID           string `json:"itemId"`
		WorkspacePath    string `json:"workspacePath"`
		CopilotSessionID any    `json:"copilotSessionId"`
		Snapshot         *struct {
			ID           string `json:"id"`
			ManifestPath string `json:"manifestPath"`
			Repositories int    `json:"repositories"`
		} `json:"snapshot"`
		Commands map[string]string `json:"commands"`
		Notes    []string          `json:"notes"`
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: invalid native reproduce JSON: %v", fixture.Name, err)
	}
	wantWorkspace := state["<fixture-root>/workspace"]
	if canonical, err := filepath.EvalSymlinks(wantWorkspace); err == nil {
		wantWorkspace = canonical
	}
	if payload.ItemID != state["<uuid>"] || payload.WorkspacePath != wantWorkspace || payload.Snapshot == nil || strings.TrimSpace(payload.Snapshot.ID) == "" || payload.Snapshot.Repositories != 1 {
		t.Fatalf("%s: incomplete native reproduce plan: %#v", fixture.Name, payload)
	}
	reproduce := payload.Commands["reproduceSnapshot"]
	if !strings.Contains(reproduce, "localreview reproduce ") || !strings.Contains(reproduce, payload.Snapshot.ManifestPath) || !strings.Contains(reproduce, "<empty-destination>") || strings.Contains(reproduce, "acp") {
		t.Fatalf("%s: unsafe native reproduce command %q", fixture.Name, reproduce)
	}
	if open := payload.Commands["openReviewer"]; !strings.Contains(open, "localreview open <empty-destination>") {
		t.Fatalf("%s: missing reviewer-open handoff %q", fixture.Name, open)
	}
	if _, staleACP := payload.Commands["reproduceCopilot"]; staleACP {
		t.Fatalf("%s: native reproduce plan must not advertise retired ACP continuation: %#v", fixture.Name, payload)
	}
	if !strings.Contains(strings.Join(payload.Notes, "\n"), "fresh SDK-native /ask") {
		t.Fatalf("%s: native reproduce plan does not explain fresh /ask handoff: %#v", fixture.Name, payload.Notes)
	}
}

// assertNativeQueueHookFixture checks the native hook's meaningful contract:
// an unchanged source is idempotent, preserves the original queue identity,
// and retains self-contained provenance/snapshot material for later review.
// The frozen TS fixture expected a newly numbered row even after an earlier
// removal; retaining identity is safer because it cannot silently create a
// duplicate review round for the same immutable source fingerprint.
func assertNativeQueueHookFixture(t *testing.T, fixture frozenParityFixture, actual *httptest.ResponseRecorder, state map[string]string) {
	t.Helper()
	if actual.Code != http.StatusOK || actual.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%s: hook response=%d type=%q body=%s", fixture.Name, actual.Code, actual.Header().Get("Content-Type"), actual.Body.String())
	}
	var payload struct {
		Created bool `json:"created"`
		Item    struct {
			ID                   string          `json:"id"`
			WorkspacePath        string          `json:"workspacePath"`
			SnapshotManifestPath *string         `json:"snapshotManifestPath"`
			SnapshotManifest     json.RawMessage `json:"snapshotManifest"`
			SourceFingerprint    *string         `json:"sourceFingerprint"`
			Provenance           json.RawMessage `json:"provenance"`
		} `json:"item"`
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: invalid hook response: %v", fixture.Name, err)
	}
	wantWorkspace := state["<fixture-root>/workspace"]
	if canonical, err := filepath.EvalSymlinks(wantWorkspace); err == nil {
		wantWorkspace = canonical
	}
	if payload.Created || payload.Item.ID != state["<uuid>"] || payload.Item.WorkspacePath != wantWorkspace || payload.Item.SnapshotManifestPath == nil || strings.TrimSpace(*payload.Item.SnapshotManifestPath) == "" || payload.Item.SourceFingerprint == nil || strings.TrimSpace(*payload.Item.SourceFingerprint) == "" {
		t.Fatalf("%s: hook is not a retained idempotent snapshot: %#v", fixture.Name, payload)
	}
	for label, raw := range map[string]json.RawMessage{"item": payload.Item.Provenance, "snapshot": payload.Item.SnapshotManifest} {
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("%s: %s provenance decode: %v", fixture.Name, label, err)
		}
		if label == "snapshot" {
			value, _ = value["provenance"].(map[string]any)
		}
		if _, ok := value["supplied"].(map[string]any); !ok {
			t.Fatalf("%s: %s missing retained submission provenance: %s", fixture.Name, label, raw)
		}
	}
}

// assertNativeCommentCollectionFixture documents the one intentional change
// from the frozen server: a GET that used to be an Express 404 is now the
// durable comment collection. This is deliberately stricter than a generic
// "native differs" waiver: both frozen reads must reach the same route, have
// JSON semantics, and prove the empty-to-saved thread lifecycle created by the
// frozen legacy POST row immediately before the saved read.
func assertNativeCommentCollectionFixture(t *testing.T, fixture frozenParityFixture, actual *httptest.ResponseRecorder) {
	t.Helper()
	if actual.Code != http.StatusOK {
		t.Fatalf("%s: native durable collection status got=%d want=%d body=%s", fixture.Name, actual.Code, http.StatusOK, actual.Body.String())
	}
	if got := actual.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("%s: native durable collection content type got=%q", fixture.Name, got)
	}
	var payload struct {
		Version int `json:"version"`
		Threads []struct {
			ID       string `json:"id"`
			FilePath string `json:"filePath"`
			Position struct {
				Side string `json:"side"`
				Line int    `json:"line"`
			} `json:"position"`
			Messages []struct {
				ID   string `json:"id"`
				Body string `json:"body"`
			} `json:"messages"`
			Channel  string `json:"channel"`
			Orphaned bool   `json:"orphaned"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: invalid native durable collection JSON: %v", fixture.Name, err)
	}
	switch fixture.Name {
	case "repo_comments_empty":
		if payload.Version != 0 || len(payload.Threads) != 0 {
			t.Fatalf("empty durable collection got version=%d threads=%#v", payload.Version, payload.Threads)
		}
	case "repo_comments_saved":
		if payload.Version < 1 || len(payload.Threads) != 1 {
			t.Fatalf("saved durable collection got version=%d threads=%#v", payload.Version, payload.Threads)
		}
		thread := payload.Threads[0]
		if thread.ID != "fixture-comment" || thread.FilePath != "root.ts" || thread.Position.Side != "new" || thread.Position.Line != 1 || thread.Channel != "formal" || thread.Orphaned || len(thread.Messages) != 1 || thread.Messages[0].ID != "fixture-comment" || thread.Messages[0].Body != "Fixture comment" {
			t.Fatalf("saved durable thread does not preserve frozen legacy comment: %#v", thread)
		}
	default:
		t.Fatalf("unexpected durable comment adapter fixture %q", fixture.Name)
	}
}

// assertNativeQueueWatchFixture proves the watcher control plane without
// relying on a ticker race. The injected no-op poll leaves the actual route's
// synchronous registration, persisted interval, cancellation map, and DB
// enabled state observable immediately after each frozen request.
func assertNativeQueueWatchFixture(t *testing.T, d *Daemon, fixture frozenParityFixture, actual *httptest.ResponseRecorder, state map[string]string) {
	t.Helper()
	if actual.Code != fixture.Response.Status {
		t.Fatalf("%s: status got=%d want=%d body=%s", fixture.Name, actual.Code, fixture.Response.Status, actual.Body.String())
	}
	if fixture.Response.ContentType == nil || actual.Header().Get("Content-Type") != *fixture.Response.ContentType {
		want := ""
		if fixture.Response.ContentType != nil {
			want = *fixture.Response.ContentType
		}
		t.Fatalf("%s: content type got=%q want=%q", fixture.Name, actual.Header().Get("Content-Type"), want)
	}
	request, ok := fixture.Request.Body.(map[string]any)
	if !ok {
		t.Fatalf("%s: frozen watcher request is not an object", fixture.Name)
	}
	workspace, ok := request["workspacePath"].(string)
	if !ok {
		t.Fatalf("%s: frozen watcher request has no workspacePath", fixture.Name)
	}
	workspace = replaceFrozenValues(workspace, state)
	// safeWorkspacePath resolves macOS's /var -> /private/var symlink. Match
	// the daemon's canonical persisted path without weakening the request
	// assertion on platforms where no symlink is present.
	if canonical, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = canonical
	}
	var response struct {
		WorkspacePath string `json:"workspacePath"`
		Enabled       bool   `json:"enabled"`
		PollInterval  int    `json:"pollIntervalMs"`
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s: invalid watcher response: %v", fixture.Name, err)
	}
	if response.WorkspacePath != workspace {
		t.Fatalf("%s: workspace got=%q want=%q", fixture.Name, response.WorkspacePath, workspace)
	}

	d.queueWatchMu.Lock()
	_, registered := d.queueWatches[workspace]
	d.queueWatchMu.Unlock()
	var enabled int
	var interval int
	if err := d.db.QueryRow(`SELECT enabled,poll_interval_ms FROM queue_watchers WHERE workspace_path=?`, workspace).Scan(&enabled, &interval); err != nil {
		t.Fatalf("%s: watcher row: %v", fixture.Name, err)
	}
	switch fixture.Name {
	case "queue_watch_enable":
		if !response.Enabled || response.PollInterval != 1000 || !registered || enabled != 1 || interval != 1000 {
			t.Fatalf("enable lifecycle response=%#v registered=%t db=(enabled=%d interval=%d)", response, registered, enabled, interval)
		}
	case "queue_watch_disable":
		if response.Enabled || registered || enabled != 0 {
			t.Fatalf("disable lifecycle response=%#v registered=%t dbEnabled=%d", response, registered, enabled)
		}
	default:
		t.Fatalf("unexpected native queue watch fixture %q", fixture.Name)
	}
}

// frozenResponseID bridges the deliberately opaque IDs in the captured TS
// corpus. The rest of a lifecycle remains an ordinary frozen replay: later
// rows use the ID emitted by the native daemon instead of a brittle literal.
// Keeping this at the corpus boundary means queue/agent workflows exercise
// the same route ordering and request payloads the TypeScript capture used.
func frozenResponseID(t *testing.T, name string, response []byte, field string) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(response, &body); err != nil {
		t.Fatalf("%s: decode lifecycle response: %v", name, err)
	}
	value, ok := body[field].(map[string]any)
	if !ok {
		t.Fatalf("%s: response has no object %q: %s", name, field, response)
	}
	id, ok := value["id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		t.Fatalf("%s: response %q has no non-empty id: %s", name, field, response)
	}
	return id
}

// assertJSONShape compares response structure without copying volatile IDs,
// paths, SHAs, timestamps, or build versions into a second oracle.
func assertJSONShape(t *testing.T, name string, want, got any) {
	t.Helper()
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("%s: expected object, got %T", name, got)
		}
		for key, value := range expected {
			actualValue, exists := actual[key]
			if !exists {
				t.Fatalf("%s: response missing key %q; got %v", name, key, mapsKeys(actual))
			}
			assertJSONShape(t, name+"."+key, value, actualValue)
		}
	case []any:
		actual, ok := got.([]any)
		if !ok {
			t.Fatalf("%s: expected array, got %T", name, got)
		}
		if len(expected) > 0 && len(actual) == 0 {
			t.Fatalf("%s: expected non-empty array", name)
		}
		if len(expected) > 0 && len(actual) > 0 {
			assertJSONShape(t, name+"[0]", expected[0], actual[0])
		}
	case nil:
		if got != nil {
			t.Fatalf("%s: expected null, got %T", name, got)
		}
	default:
		if fmt.Sprintf("%T", expected) != fmt.Sprintf("%T", got) {
			t.Fatalf("%s: type got=%T want=%T", name, got, expected)
		}
	}
}

func mapsKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}
