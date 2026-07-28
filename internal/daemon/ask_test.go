package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
	"github.com/uhvesta/cmux-localreview/internal/store"
)

type askRouteBackend struct {
	session *askRouteSession
	models  []copilotsdk.ModelInfo
}

func (b askRouteBackend) ListModels(context.Context) ([]copilotsdk.ModelInfo, error) {
	return b.models, nil
}
func (b askRouteBackend) OpenSession(_ context.Context, config copilot.SessionConfig) (askruntime.Session, error) {
	b.session.mu.Lock()
	b.session.configs = append(b.session.configs, config)
	b.session.mu.Unlock()
	return b.session, nil
}

type askRouteSession struct {
	mu       sync.Mutex
	callback func(askruntime.RuntimeEvent)
	prompts  []string
	configs  []copilot.SessionConfig
	aborted  bool
	emit     bool
}

func TestWorkspaceModelDefaultsAreExposedAndAppliedOnlyWhenConversationIsUnset(t *testing.T) {
	d := askRouteDaemon(t)
	if _, err := d.db.Exec(`INSERT INTO sessions(id,label,started_at) VALUES(1,'review',1)`); err != nil {
		t.Fatal(err)
	}
	d.review = &workspaceReview{Root: "/workspace", SessionID: 1}
	session := &askRouteSession{emit: true}
	d.askFactory = &AskRuntimeFactory{Source: daemonTokenSource{token: "dedicated"}, Build: func(context.Context, copilot.ClientConfig) (*askruntime.Runtime, func() error, error) {
		return askruntime.New(askRouteBackend{session: session, models: []copilotsdk.ModelInfo{{ID: "gpt-5", Name: "GPT-5"}}}), nil, nil
	}}
	updated := askRequest(t, d, http.MethodPut, "/ask/settings", `{"model":"gpt-5","reasoningEffort":"high","contextTier":"long_context"}`)
	if updated.Code != http.StatusOK || !bytes.Contains(updated.Body.Bytes(), []byte(`"model":"gpt-5"`)) {
		t.Fatalf("settings=%d %s", updated.Code, updated.Body.String())
	}
	models := askRequest(t, d, http.MethodGet, "/ask/models", "")
	if models.Code != http.StatusOK || !bytes.Contains(models.Body.Bytes(), []byte(`"source":"sdk"`)) || !bytes.Contains(models.Body.Bytes(), []byte(`"id":"gpt-5"`)) {
		t.Fatalf("models=%d %s", models.Code, models.Body.String())
	}
	conversation, err := ask.CreateConversation(context.Background(), d.db, ask.CreateConversationInput{ReviewSessionID: d.askSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	response := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/messages", `{"body":"why?"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("send=%d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		configs := append([]copilot.SessionConfig(nil), session.configs...)
		session.mu.Unlock()
		if len(configs) > 0 {
			if configs[0].Model != "gpt-5" || configs[0].ReasoningEffort != "high" || configs[0].ContextTier != copilotsdk.ContextTierLongContext {
				t.Fatalf("config=%#v", configs[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("model defaults were not sent")
		}
		time.Sleep(time.Millisecond)
	}
	// An explicit override always wins over the workspace setting.
	explicit := "other-model"
	if _, err := ask.UpdateConversation(context.Background(), d.db, conversation.ID, ask.UpdateConversationInput{Model: &explicit}); err != nil {
		t.Fatal(err)
	}
	overridden, err := ask.GetConversation(context.Background(), d.db, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	config, err := d.askTurnConfig(overridden, "/workspace")
	if err != nil || config.Model != "other-model" {
		t.Fatalf("override config=%#v err=%v", config, err)
	}
}

func (s *askRouteSession) SendPrompt(_ context.Context, prompt string) (string, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	callback := s.callback
	s.mu.Unlock()
	if s.emit {
		callback(askruntime.RuntimeEvent{Kind: askruntime.EventDelta, Text: "Copilot reply"})
		callback(askruntime.RuntimeEvent{Kind: askruntime.EventDone})
	}
	return "sdk-msg", nil
}
func (s *askRouteSession) Abort(context.Context) error {
	s.mu.Lock()
	s.aborted = true
	s.mu.Unlock()
	return nil
}
func (s *askRouteSession) On(callback func(askruntime.RuntimeEvent)) func() {
	s.mu.Lock()
	s.callback = callback
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.callback = nil
		s.mu.Unlock()
	}
}

func askRouteDaemon(t *testing.T) *Daemon {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Daemon{db: db}
}

func askRequest(t *testing.T, daemon *Daemon, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewBufferString(body))
	result := httptest.NewRecorder()
	if !daemon.handleAsk(result, req, path) {
		t.Fatalf("route %s was not handled", path)
	}
	return result
}

func TestAskMessageAndCancelAreDurableWithoutSDKNetwork(t *testing.T) {
	d := askRouteDaemon(t)
	d.askRuntime = askruntime.New(askRouteBackend{session: &askRouteSession{}})
	conversation, err := ask.CreateConversation(context.Background(), d.db, ask.CreateConversationInput{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"body":"Explain this line","location":{"repoId":"repo-1","filePath":"cmd/main.go","workspacePath":"services/api/cmd/main.go","side":"current","startLine":9,"endLine":11,"selectedCode":"return err"}}`
	created := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/messages", body)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var payload struct {
		Delivery  string      `json:"delivery"`
		Assistant ask.Message `json:"assistant"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Delivery != "streaming" || !payload.Assistant.Pending {
		t.Fatalf("payload=%#v", payload)
	}
	deadline := time.Now().Add(time.Second)
	for !d.askRuntime.IsBusy(conversation.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !d.askRuntime.IsBusy(conversation.ID) {
		t.Fatal("runtime did not begin the explicit turn")
	}
	messages, err := ask.ListMessages(context.Background(), d.db, conversation.ID)
	if err != nil || len(messages) != 2 || messages[0].Location == nil || messages[0].Location.WorkspacePath != "services/api/cmd/main.go" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	cancelled := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/cancel", ``)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	messages, err = ask.ListMessages(context.Background(), d.db, conversation.ID)
	if err != nil || messages[1].Pending || messages[1].Body != "Response cancelled before it completed." {
		t.Fatalf("after cancel=%#v err=%v", messages, err)
	}
	// A second cancellation is a safe no-op, so reload/retry cannot cancel a
	// later prompt merely because the earlier request was repeated.
	again := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/cancel", ``)
	if again.Code != http.StatusOK || !bytes.Contains(again.Body.Bytes(), []byte(`"cancelled":false`)) {
		t.Fatalf("repeat cancel=%s", again.Body.String())
	}
}

func TestInlineConversationPersistsInitialAnchor(t *testing.T) {
	d := askRouteDaemon(t)
	response := askRequest(t, d, http.MethodPost, "/ask/inline-conversations", `{"model":"gpt-5","context":{"repoId":"repo-1","filePath":"lib/check.go","workspacePath":"nested/lib/check.go","side":"current","startLine":4,"endLine":5,"selectedCode":"return valid"}}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Conversation ask.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Conversation.Context == nil || payload.Conversation.Context.WorkspacePath != "nested/lib/check.go" || payload.Conversation.Context.SelectedCode != "return valid" {
		t.Fatalf("conversation=%#v", payload.Conversation)
	}
}

func TestAskMessageStreamsIntoDurableTranscriptWithoutReplayOnRead(t *testing.T) {
	d := askRouteDaemon(t)
	session := &askRouteSession{emit: true}
	d.askRuntime = askruntime.New(askRouteBackend{session: session})
	conversation, err := ask.CreateConversation(context.Background(), d.db, ask.CreateConversationInput{Model: "gpt-5", Context: &ask.Location{WorkspacePath: "apps/reviewer/main.go", FilePath: "main.go", StartLine: 7, EndLine: 7, SelectedCode: "return err"}})
	if err != nil {
		t.Fatal(err)
	}
	response := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/messages", `{"body":"why?"}`)
	if response.Code != http.StatusAccepted || !bytes.Contains(response.Body.Bytes(), []byte(`"delivery":"streaming"`)) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		messages, listErr := ask.ListMessages(context.Background(), d.db, conversation.ID)
		if listErr == nil && len(messages) == 2 && !messages[1].Pending && messages[1].Body == "Copilot reply" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream did not settle: messages=%#v err=%v", messages, listErr)
		}
		time.Sleep(time.Millisecond)
	}
	// A transcript read is intentionally only a DB read: it must never cause a
	// second SDK SendPrompt or replay the question after a page reload.
	read := askRequest(t, d, http.MethodGet, "/ask/conversations/"+conversation.ID, "")
	if read.Code != http.StatusOK {
		t.Fatalf("read=%d %s", read.Code, read.Body.String())
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.prompts) != 1 || !strings.Contains(session.prompts[0], "Workspace-relative path: apps/reviewer/main.go") || !strings.Contains(session.prompts[0], "lines: 7-7") {
		t.Fatalf("prompts=%q", session.prompts)
	}
}
