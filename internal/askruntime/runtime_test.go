package askruntime

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
)

type testTokenSource struct {
	token string
	err   error
}

func (s testTokenSource) CopilotToken(context.Context) (string, error) { return s.token, s.err }

type fakeBackend struct {
	models  []copilotsdk.ModelInfo
	session *fakeSession
	opens   int
}

func (backend *fakeBackend) ListModels(context.Context) ([]copilotsdk.ModelInfo, error) {
	return backend.models, nil
}
func (backend *fakeBackend) OpenSession(context.Context, copilot.SessionConfig) (Session, error) {
	backend.opens++
	return backend.session, nil
}

type fakeSession struct {
	mu        sync.Mutex
	callbacks []func(RuntimeEvent)
	sent      []string
	aborts    int
	gate      chan struct{}
}

func (session *fakeSession) SendPrompt(_ context.Context, prompt string) (string, error) {
	session.mu.Lock()
	session.sent = append(session.sent, prompt)
	gate := session.gate
	session.mu.Unlock()
	if gate != nil {
		<-gate
	}
	session.emit(RuntimeEvent{Kind: EventDelta, Text: "partial", MessageID: "m-1"})
	session.emit(RuntimeEvent{Kind: EventDone})
	return "m-1", nil
}
func (session *fakeSession) Abort(context.Context) error {
	session.mu.Lock()
	session.aborts++
	session.mu.Unlock()
	session.emit(RuntimeEvent{Kind: EventDone, Aborted: true})
	return nil
}
func (session *fakeSession) On(callback func(RuntimeEvent)) func() {
	session.mu.Lock()
	session.callbacks = append(session.callbacks, callback)
	index := len(session.callbacks) - 1
	session.mu.Unlock()
	return func() {
		session.mu.Lock()
		defer session.mu.Unlock()
		if index < len(session.callbacks) {
			session.callbacks[index] = nil
		}
	}
}
func (session *fakeSession) emit(event RuntimeEvent) {
	session.mu.Lock()
	callbacks := append([]func(RuntimeEvent){}, session.callbacks...)
	session.mu.Unlock()
	for _, callback := range callbacks {
		if callback != nil {
			callback(event)
		}
	}
}

func TestListModelsAndStreamOneExplicitTurn(t *testing.T) {
	backend := &fakeBackend{models: []copilotsdk.ModelInfo{{ID: "gpt-5", Name: "GPT-5"}}, session: &fakeSession{}}
	runtime := New(backend)
	models, err := runtime.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "gpt-5" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	var events []Delta
	id, err := runtime.Send(context.Background(), "conversation-1", copilot.SessionConfig{ID: "conversation-1", Model: "gpt-5", WorkingDirectory: "/workspace", Streaming: true}, "why?", func(delta Delta) { events = append(events, delta) })
	if err != nil || id != "m-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if runtime.IsBusy("conversation-1") {
		t.Fatal("turn remained busy after idle")
	}
	if backend.opens != 1 || len(backend.session.sent) != 1 {
		t.Fatalf("opens=%d sent=%#v", backend.opens, backend.session.sent)
	}
	if len(events) != 3 || events[0].Event != EventStatus || events[1].Event != EventDelta || events[1].Text != "partial" || events[2].Event != EventDone {
		t.Fatalf("events=%#v", events)
	}
	// A second explicit message reuses the live session but never replayed the
	// first prompt simply because the runtime was inspected again.
	_, err = runtime.Send(context.Background(), "conversation-1", copilot.SessionConfig{ID: "conversation-1", Model: "gpt-5", WorkingDirectory: "/workspace", Streaming: true}, "follow up", func(Delta) {})
	if err != nil || backend.opens != 1 || len(backend.session.sent) != 2 {
		t.Fatalf("err=%v opens=%d sent=%#v", err, backend.opens, backend.session.sent)
	}
}

func TestTurnClaimAndCancelNeverAffectNextTurn(t *testing.T) {
	gate := make(chan struct{})
	backend := &fakeBackend{session: &fakeSession{gate: gate}}
	runtime := New(backend)
	done := make(chan error, 1)
	go func() {
		_, err := runtime.Send(context.Background(), "conversation-1", copilot.SessionConfig{ID: "conversation-1", Model: "gpt-5", WorkingDirectory: "/workspace", Streaming: true}, "first", func(Delta) {})
		done <- err
	}()
	for !runtime.IsBusy("conversation-1") {
	}
	_, err := runtime.Send(context.Background(), "conversation-1", copilot.SessionConfig{ID: "conversation-1", Model: "gpt-5", WorkingDirectory: "/workspace", Streaming: true}, "duplicate", func(Delta) {})
	if !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("duplicate error=%v", err)
	}
	cancelled, err := runtime.Cancel(context.Background(), "conversation-1")
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancelled, err = runtime.Cancel(context.Background(), "conversation-1")
	if err != nil || cancelled {
		t.Fatalf("idle cancel=%v err=%v", cancelled, err)
	}
	if backend.session.aborts != 1 {
		t.Fatalf("aborts=%d", backend.session.aborts)
	}
}

func TestRuntimePropagatesSDKStyleError(t *testing.T) {
	backend := &fakeBackend{session: &fakeSession{}}
	runtime := New(backend)
	var events []Delta
	// Inject a terminal session event through a session that emits the error as
	// part of Send rather than relying on any credential or live SDK runtime.
	backend.session.gate = make(chan struct{})
	finished := make(chan struct{})
	go func() {
		_, _ = runtime.Send(context.Background(), "conversation-1", copilot.SessionConfig{ID: "conversation-1", Model: "gpt-5", WorkingDirectory: "/workspace", Streaming: true}, "question", func(event Delta) { events = append(events, event) })
		close(finished)
	}()
	for !runtime.IsBusy("conversation-1") {
	}
	backend.session.emit(RuntimeEvent{Kind: EventError, Error: errors.New("unauthenticated")})
	close(backend.session.gate)
	<-finished
	if len(events) < 2 || events[len(events)-1].Event != EventError || events[len(events)-1].Error != "unauthenticated" {
		t.Fatalf("events=%#v", events)
	}
}

func TestAskTransportUsesOnlyInjectedTokenAndSafeSSE(t *testing.T) {
	options, err := ClientOptions(context.Background(), testTokenSource{token: "dedicated-token"}, "/workspace", "/daemon/copilot")
	if err != nil || options.GitHubToken != "dedicated-token" || options.BaseDirectory != "/daemon/copilot" {
		t.Fatalf("options=%#v err=%v", options, err)
	}
	if _, err := ClientOptions(context.Background(), nil, "/workspace", "/daemon/copilot"); err == nil {
		t.Fatal("missing credential provider must fail")
	}
	var stream bytes.Buffer
	if err := WriteSSE(&stream, Delta{Event: EventDelta, ConversationID: "c", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if got := stream.String(); got != "event: delta\ndata: {\"event\":\"delta\",\"conversationId\":\"c\",\"text\":\"hello\"}\n\n" {
		t.Fatalf("SSE=%q", got)
	}
	if err := WriteSSE(&stream, Delta{Event: EventKind("bad\nboom")}); err == nil {
		t.Fatal("event injection accepted")
	}
}

func TestModelsFallsBackWithoutClaimingLiveSDK(t *testing.T) {
	backend := &fakeBackend{models: []copilotsdk.ModelInfo{{ID: "gpt", Name: "GPT"}}, session: &fakeSession{}}
	got, err := Models(context.Background(), New(backend), []Model{{ID: "fallback", Name: "Fallback"}})
	if err != nil || got.Source != "sdk" || len(got.Models) != 1 {
		t.Fatalf("%#v %v", got, err)
	}
	got, err = Models(context.Background(), New(nil), []Model{{ID: "fallback", Name: "Fallback"}})
	if err != nil || got.Source != "fallback" || got.Warning == "" {
		t.Fatalf("%#v %v", got, err)
	}
}
