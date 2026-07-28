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

	copilotsdk "github.com/github/copilot-sdk/go"
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
	Execute               bool
	Reason                string
	ForceDaemonCapability bool
	DeviceFlow            bool
	NativeAskSettings     bool
}

var parityMatrix = map[string]parityDisposition{
	"health":                   {Execute: true},
	"unauthenticated_queue":    {Execute: true},
	"browser_session_exchange": {Execute: true},
	"github_auth_status":       {Execute: true},
	"github_auth_configure":    {Execute: true},
	// The native daemon defaults to device OAuth. The frozen device payload
	// therefore exercises the supported default rather than an implicit
	// loopback callback.
	"github_auth_device_start":         {Execute: true, DeviceFlow: true, Reason: "Native defaults to device OAuth."},
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

	"repo_revisions":      {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer reads are deliberately capability-protected."},
	"repo_comments_empty": {Reason: "Legacy difit comment-empty 404 semantics are intentionally replaced by durable native comment collections; dedicated comment tests cover the native contract."},
	"create_comment":      {Execute: true, ForceDaemonCapability: true, Reason: "Frozen difit short comment shape is translated at the native boundary into a durable formal thread."},
	"repo_comments_saved": {Reason: "Frozen request uses legacy difit comment schema; native durable-thread migration is covered by daemon comment tests until an adapter fixture is added."},
	// Import accepts the original compact difit row and stores it as a durable
	// formal thread. The response is intentionally still byte-compatible with
	// the frozen capture, so replay it instead of relying only on unit coverage.
	"comment_import":         {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer writes are capability-protected."},
	"review_history":         {Execute: true, ForceDaemonCapability: true, Reason: "Replayed with the daemon capability: native reviewer reads are capability-protected."},
	"btw_threads_empty":      {Reason: "Native /btw uses the SDK conversation store rather than the retired ACP thread projection."},
	"btw_ask_validation":     {Reason: "Native /btw validation is tested directly against explicit target routing; frozen ACP prompt shape is intentionally retired."},
	"websocket_diff_updated": {Reason: "WebSocket frame byte parity is validated in internal/wshub; the fixture needs a deterministic watcher clock before direct replay."},
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
	"ask_conversation_model":          {Execute: true, ForceDaemonCapability: true, NativeAskSettings: true},
	"ask_conversation_settings":       {Execute: true, ForceDaemonCapability: true, NativeAskSettings: true},
	"ask_conversation_message_sse":    {Reason: "Native stream events use an EventSource endpoint after accepted submission, intentionally replacing TS POST-SSE framing."},
	"ask_conversation_cancel_idle":    {Reason: "Native cancellation is covered by ask route tests with a fake official SDK session."},
	"ask_question_set_for_send":       {Reason: "Native question-set routes are covered by ask route tests; sequential SSE replay needs deterministic stream framing."},
	"ask_question_set_combined_sse":   {Reason: "Native stream events use an EventSource endpoint after accepted submission, intentionally replacing TS POST-SSE framing."},
	"ask_question_set_sequential_sse": {Reason: "Native stream events use an EventSource endpoint after accepted submission, intentionally replacing TS POST-SSE framing."},
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
	"queue_reproduce":            {Reason: "Native reproduce intentionally omits retired ACP resume fields (existingAcp/freshAcp) and gives an explicit fresh SDK /ask plan; daemon lifecycle tests verify the substitution."},
	"queue_export":               {Execute: true},
	"queue_open":                 {Execute: true},
	"queue_complete":             {Execute: true},
	"queue_requeue":              {Execute: true},
	"queue_delete":               {Execute: true},
	"queue_history":              {Execute: true},
	"queue_watch_enable":         {Reason: "Queue watch requires a wall-clock polling fixture and is covered by queue watcher tests."},
	"queue_watch_disable":        {Reason: "Queue watch requires a wall-clock polling fixture and is covered by queue watcher tests."},
	"queue_hook":                 {Reason: "Hook discovery is CLI-facing and has a dedicated native CLI test."},
	"agent_register":             {Execute: true},
	"agent_list":                 {Execute: true},
	"agent_heartbeat":            {Execute: true},
	"agent_reconnect":            {Execute: true},
	"federation_node_create":     {Reason: "Frozen TS fixture predates native SSH transport; hermetic loopback tunnel API tests cover the stronger native contract."},
	"federation_node_status":     {Reason: "Frozen TS fixture predates native SSH transport; hermetic loopback tunnel API tests cover the stronger native contract."},
	"federation_node_disconnect": {Reason: "Frozen TS fixture predates native SSH transport; hermetic loopback tunnel API tests cover the stronger native contract."},
	"federation_aggregate_queue": {Reason: "Frozen TS fixture predates native SSH transport; hermetic loopback tunnel API tests cover the stronger native contract."},
	"federation_node_delete":     {Reason: "Frozen TS fixture predates native SSH transport; hermetic loopback tunnel API tests cover the stronger native contract."},
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
		"local_pr_requires_read_auth", "workspaces_empty", "queue_empty", "federation_nodes_empty", "open_workspace", "repos",
		"repo_diff", "repo_diff_ignore_whitespace", "repo_revisions", "repo_line_count", "repo_blob", "repo_generated_status", "repo_fullfile", "create_comment", "comment_import",
		"sessions", "review_history", "ui_state_empty", "ui_state_put", "export_prompt", "new_session", "comments_json", "comments_output",
		"ask_models", "ask_conversations_empty", "ask_question_set_create", "ask_question_sets", "ask_question_set_get", "ask_question_set_update", "ask_question_set_delete", "ask_conversation_create", "ask_conversation_get", "ask_inline_conversation_reuses_context", "ask_conversation_model", "ask_conversation_settings", "ask_conversation_fresh", "ask_conversation_history",
		"queue_create_local", "queue_list_with_item", "queue_detail", "queue_reorder", "queue_add_feedback", "queue_feedback_prompt", "queue_export", "queue_open", "queue_complete", "queue_requeue", "queue_delete", "queue_history",
		"agent_register", "agent_list", "agent_heartbeat", "agent_reconnect",
	} {
		fixture := byName[name]
		disposition := parityMatrix[name]
		response := replayFrozenFixture(t, d, fixture, disposition, state)
		if disposition.NativeAskSettings {
			assertNativeAskSettingsFixture(t, fixture, response)
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
		if name == "ask_question_set_create" || name == "ask_conversation_create" {
			state["<uuid>"] = frozenResponseID(t, name, response.Body.Bytes(), map[string]string{"ask_question_set_create": "questionSet", "ask_conversation_create": "conversation"}[name])
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
	d, err := Start(ctx, Options{DataDir: t.TempDir(), GitHubAuth: githubauth.New(authSecrets{}, authConfig{}, transport, func(string) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	// Model discovery is a client-level SDK operation, not a conversation turn.
	// Keep it hermetic so the frozen model-list row proves the native picker
	// contract without a dedicated credential or network call.
	d.askRuntime = askruntime.New(askRouteBackend{session: &askRouteSession{}, models: []copilotsdk.ModelInfo{{ID: "fixture-model", Name: "Fixture Model"}}})
	d.askFactory = &AskRuntimeFactory{}
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
		body = replaceFrozenValues(string(encoded), state)
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
