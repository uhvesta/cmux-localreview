// Package e2ecopilot provides a deterministic SDK-shaped Copilot transport
// for acceptance tests. It is intentionally reachable only from the separate
// localreviewd-e2e binary; production localreviewd and release archives do
// not link this package.
package e2ecopilot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
)

// TokenSource is deliberately a non-secret marker used by the isolated E2E
// executable. It is not accepted by the production binary, which always
// obtains a dedicated capability from the OS secret store.
type TokenSource struct{}

func (TokenSource) CopilotToken(context.Context) (string, error) {
	return "e2e-fixture-not-a-credential", nil
}

// Backend implements the same narrow adapter boundary as the official SDK.
// Its model list, asynchronous deltas, idle completion, and abort behaviour
// make browser/Electron validation exercise the real daemon persistence and
// SSE paths without making an external Copilot request.
type Backend struct{}

func NewBackend() *Backend { return &Backend{} }

func (*Backend) ListModels(context.Context) ([]copilotsdk.ModelInfo, error) {
	return []copilotsdk.ModelInfo{
		{ID: "gpt-5", Name: "GPT-5 (E2E fixture)"},
		{ID: "claude-sonnet-4.6", Name: "Claude Sonnet 4.6 (E2E fixture)"},
		{ID: "gpt-5-mini", Name: "GPT-5 mini (E2E fixture)"},
	}, nil
}

func (*Backend) OpenSession(_ context.Context, config copilot.SessionConfig) (askruntime.Session, error) {
	model := strings.TrimSpace(config.Model)
	if model == "" || model == "auto" {
		model = "gpt-5"
	}
	return &session{model: model}, nil
}

type session struct {
	mu       sync.Mutex
	model    string
	callback func(askruntime.RuntimeEvent)
	turn     int
	cancel   chan struct{}
}

func (s *session) On(callback func(askruntime.RuntimeEvent)) func() {
	s.mu.Lock()
	s.callback = callback
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.callback != nil {
			s.callback = nil
		}
		s.mu.Unlock()
	}
}

func (s *session) SendPrompt(_ context.Context, prompt string) (string, error) {
	s.mu.Lock()
	s.turn++
	turn := s.turn
	if s.cancel != nil {
		close(s.cancel)
	}
	cancel := make(chan struct{})
	s.cancel = cancel
	model := s.model
	s.mu.Unlock()

	go s.stream(cancel, turn, model, prompt)
	return fmt.Sprintf("fixture-%d", turn), nil
}

func (s *session) Abort(context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		close(s.cancel)
		s.cancel = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *session) emit(event askruntime.RuntimeEvent) {
	s.mu.Lock()
	callback := s.callback
	s.mu.Unlock()
	if callback != nil {
		callback(event)
	}
}

func (s *session) stream(cancel <-chan struct{}, turn int, model, prompt string) {
	// Keep this visibly streamed (rather than one immediate fake answer), so
	// the browser can show its live state and exercise the cancellation button.
	answer := fmt.Sprintf("Fixture Copilot using %s, turn %d: %s", model, turn, questionFromPrompt(prompt))
	words := strings.Fields(answer)
	for index, word := range words {
		select {
		case <-cancel:
			return
		case <-time.After(55 * time.Millisecond):
		}
		if index > 0 {
			word = " " + word
		}
		s.emit(askruntime.RuntimeEvent{Kind: askruntime.EventDelta, Text: word, MessageID: fmt.Sprintf("fixture-%d", turn)})
	}
	s.mu.Lock()
	if s.cancel == cancel {
		s.cancel = nil
	}
	s.mu.Unlock()
	s.emit(askruntime.RuntimeEvent{Kind: askruntime.EventDone})
}

func questionFromPrompt(prompt string) string {
	const marker = "Question:\n"
	question := prompt
	if index := strings.LastIndex(prompt, marker); index >= 0 {
		question = prompt[index+len(marker):]
	}
	question = strings.Join(strings.Fields(question), " ")
	if question == "" {
		return "I received an empty question."
	}
	if len(question) > 120 {
		question = question[:117] + "..."
	}
	return "I received: " + question
}
