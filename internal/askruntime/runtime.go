// Package askruntime runs persistent /ask conversations through the official
// Copilot Go SDK. It has no HTTP dependency: daemon routes can translate its
// Delta callbacks directly into SSE events.
package askruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
)

var ErrTurnInProgress = errors.New("/ask conversation is already responding")

type EventKind string

const (
	EventDelta          EventKind = "delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventStatus         EventKind = "status"
	EventDone           EventKind = "done"
	EventError          EventKind = "error"
)

// Delta is deliberately SSE-shaped. HTTP uses Event as the SSE event name
// and JSON-encodes the remainder as the data payload.
type Delta struct {
	Event          EventKind `json:"event"`
	ConversationID string    `json:"conversationId"`
	Text           string    `json:"text,omitempty"`
	MessageID      string    `json:"messageId,omitempty"`
	Aborted        bool      `json:"aborted,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type RuntimeEvent struct {
	Kind      EventKind
	Text      string
	MessageID string
	Aborted   bool
	Error     error
}

// Session is a narrow interface over an SDK session. It permits deterministic
// tests without credentials while SDKSession remains the production adapter.
type Session interface {
	SendPrompt(context.Context, string) (string, error)
	Abort(context.Context) error
	On(func(RuntimeEvent)) func()
}

type Backend interface {
	ListModels(context.Context) ([]copilotsdk.ModelInfo, error)
	OpenSession(context.Context, copilot.SessionConfig) (Session, error)
}

// SDKBackend is the production implementation. The supplied client must use
// the localreview credential provider and not an inherited gh/CLI login.
type SDKBackend struct{ Client *copilotsdk.Client }

func (backend SDKBackend) ListModels(ctx context.Context) ([]copilotsdk.ModelInfo, error) {
	if backend.Client == nil {
		return nil, errors.New("Copilot SDK client is required")
	}
	return backend.Client.ListModels(ctx)
}

func (backend SDKBackend) OpenSession(ctx context.Context, config copilot.SessionConfig) (Session, error) {
	session, err := copilot.CreateSession(ctx, backend.Client, config)
	if err != nil {
		return nil, err
	}
	return sdkSession{raw: session}, nil
}

type sdkSession struct{ raw *copilotsdk.Session }

func (session sdkSession) SendPrompt(ctx context.Context, prompt string) (string, error) {
	return session.raw.SendPrompt(ctx, prompt)
}
func (session sdkSession) Abort(ctx context.Context) error { return session.raw.Abort(ctx) }
func (session sdkSession) On(callback func(RuntimeEvent)) func() {
	return session.raw.On(func(event copilotsdk.SessionEvent) {
		switch data := event.Data.(type) {
		case *copilotsdk.AssistantMessageDeltaData:
			callback(RuntimeEvent{Kind: EventDelta, Text: data.DeltaContent, MessageID: data.MessageID})
		case *copilotsdk.AssistantReasoningDeltaData:
			callback(RuntimeEvent{Kind: EventReasoningDelta, Text: data.DeltaContent, MessageID: data.ReasoningID})
		case *copilotsdk.AssistantIdleData:
			callback(RuntimeEvent{Kind: EventDone, Aborted: data.Aborted != nil && *data.Aborted})
		case *copilotsdk.SessionErrorData:
			callback(RuntimeEvent{Kind: EventError, Error: errors.New(data.Message)})
		}
	})
}

type turn struct {
	session     Session
	unsubscribe func()
	sink        func(Delta)
}

// Runtime holds one live SDK session per persisted conversation. It never
// automatically re-sends prior questions on a page refresh: callers must
// explicitly invoke Send for a new user turn.
type Runtime struct {
	backend  Backend
	mu       sync.Mutex
	sessions map[string]Session
	turns    map[string]*turn
}

func New(backend Backend) *Runtime {
	return &Runtime{backend: backend, sessions: map[string]Session{}, turns: map[string]*turn{}}
}

func (runtime *Runtime) ListModels(ctx context.Context) ([]copilotsdk.ModelInfo, error) {
	if runtime.backend == nil {
		return nil, errors.New("Copilot /ask backend is unavailable")
	}
	return runtime.backend.ListModels(ctx)
}

func (runtime *Runtime) IsBusy(conversationID string) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	_, ok := runtime.turns[conversationID]
	return ok
}

// Send claims one turn, opens the SDK session only when first needed, and
// streams deltas through sink. A session remains available for later explicit
// turns, preserving its model context without replaying prompt history.
func (runtime *Runtime) Send(ctx context.Context, conversationID string, config copilot.SessionConfig, prompt string, sink func(Delta)) (string, error) {
	if conversationID == "" {
		return "", errors.New("/ask conversation ID is required")
	}
	if prompt == "" {
		return "", errors.New("/ask prompt is required")
	}
	if sink == nil {
		sink = func(Delta) {}
	}
	runtime.mu.Lock()
	if _, busy := runtime.turns[conversationID]; busy {
		runtime.mu.Unlock()
		return "", ErrTurnInProgress
	}
	session := runtime.sessions[conversationID]
	runtime.mu.Unlock()
	if session == nil {
		if runtime.backend == nil {
			return "", errors.New("Copilot /ask backend is unavailable")
		}
		var err error
		session, err = runtime.backend.OpenSession(ctx, config)
		if err != nil {
			return "", fmt.Errorf("open Copilot /ask session: %w", err)
		}
		runtime.mu.Lock()
		// Another caller could have opened it while OpenSession waited. That
		// caller wins, and this turn must not silently create a second context.
		if _, busy := runtime.turns[conversationID]; busy {
			runtime.mu.Unlock()
			return "", ErrTurnInProgress
		}
		if existing := runtime.sessions[conversationID]; existing != nil {
			session = existing
		} else {
			runtime.sessions[conversationID] = session
		}
		runtime.mu.Unlock()
	}

	entry := &turn{session: session, sink: sink}
	entry.unsubscribe = session.On(func(event RuntimeEvent) { runtime.handleEvent(conversationID, entry, event) })
	runtime.mu.Lock()
	if _, busy := runtime.turns[conversationID]; busy {
		runtime.mu.Unlock()
		entry.unsubscribe()
		return "", ErrTurnInProgress
	}
	runtime.turns[conversationID] = entry
	runtime.mu.Unlock()
	sink(Delta{Event: EventStatus, ConversationID: conversationID, Text: "working"})
	messageID, err := session.SendPrompt(ctx, prompt)
	if err != nil {
		runtime.finish(conversationID, entry, RuntimeEvent{Kind: EventError, Error: err})
		return "", err
	}
	return messageID, nil
}

// Cancel cancels only an active turn, never storing a latent abort that could
// accidentally cancel a later question.
func (runtime *Runtime) Cancel(ctx context.Context, conversationID string) (bool, error) {
	runtime.mu.Lock()
	entry := runtime.turns[conversationID]
	runtime.mu.Unlock()
	if entry == nil {
		return false, nil
	}
	if err := entry.session.Abort(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (runtime *Runtime) handleEvent(conversationID string, entry *turn, event RuntimeEvent) {
	switch event.Kind {
	case EventDelta, EventReasoningDelta:
		entry.sink(Delta{Event: event.Kind, ConversationID: conversationID, Text: event.Text, MessageID: event.MessageID})
	case EventDone, EventError:
		runtime.finish(conversationID, entry, event)
	}
}

func (runtime *Runtime) finish(conversationID string, entry *turn, event RuntimeEvent) {
	runtime.mu.Lock()
	if runtime.turns[conversationID] != entry {
		runtime.mu.Unlock()
		return
	}
	delete(runtime.turns, conversationID)
	runtime.mu.Unlock()
	if entry.unsubscribe != nil {
		entry.unsubscribe()
	}
	if event.Kind == EventError {
		entry.sink(Delta{Event: EventError, ConversationID: conversationID, Error: event.Error.Error()})
		return
	}
	entry.sink(Delta{Event: EventDone, ConversationID: conversationID, Aborted: event.Aborted})
}
