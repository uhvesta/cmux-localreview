package askruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/copilot"
)

// TokenSource is the daemon-only Copilot credential boundary. Implementations
// may read the dedicated `copilot` GitHub OAuth App capability from the OS secret
// store, but must never fall back to gh, environment tokens, or a logged-in
// Copilot CLI session.
type TokenSource interface {
	CopilotToken(context.Context) (string, error)
}

// ClientOptions constructs explicit SDK options for a fresh /ask chat. It
// always disables SDK ambient-login discovery: the sole accepted authority is
// the injected daemon credential source.
func ClientOptions(ctx context.Context, source TokenSource, workingDirectory, baseDirectory string) (copilot.ClientConfig, error) {
	if source == nil {
		return copilot.ClientConfig{}, errors.New("dedicated Copilot credential provider is unavailable")
	}
	token, err := source.CopilotToken(ctx)
	if err != nil {
		return copilot.ClientConfig{}, fmt.Errorf("load dedicated Copilot credential: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return copilot.ClientConfig{}, errors.New("dedicated Copilot credential provider returned no token")
	}
	return copilot.ClientConfig{WorkingDirectory: workingDirectory, BaseDirectory: baseDirectory, GitHubToken: token}, nil
}

type ModelResult struct {
	Models  []Model `json:"models"`
	Source  string  `json:"source"` // sdk or fallback
	Warning string  `json:"warning,omitempty"`
}
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Models returns live SDK model choices when possible. A caller-provided static
// fallback only preserves the picker UI during a temporary outage; it does not
// claim authentication works and no request is made until Send is explicit.
func Models(ctx context.Context, runtime *Runtime, fallback []Model) (ModelResult, error) {
	if runtime == nil {
		return ModelResult{}, errors.New("Copilot /ask runtime is unavailable")
	}
	models, err := runtime.ListModels(ctx)
	if err != nil {
		if len(fallback) == 0 {
			return ModelResult{}, err
		}
		return ModelResult{Models: append([]Model(nil), fallback...), Source: "fallback", Warning: err.Error()}, nil
	}
	out := make([]Model, 0, len(models))
	for _, m := range models {
		out = append(out, Model{ID: m.ID, Name: m.Name})
	}
	return ModelResult{Models: out, Source: "sdk"}, nil
}

// WriteSSE serializes an ask event without allowing arbitrary event-name
// injection. HTTP handlers set Content-Type and flushing policy themselves.
func WriteSSE(writer io.Writer, event Delta) error {
	if !validEvent(event.Event) {
		return fmt.Errorf("invalid /ask SSE event %q", event.Event)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.Event, payload)
	return err
}
func validEvent(kind EventKind) bool {
	return kind == EventDelta || kind == EventReasoningDelta || kind == EventStatus || kind == EventDone || kind == EventError
}
