package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
	"github.com/uhvesta/cmux-localreview/internal/githubauth"
)

// GitHubCopilotTokenSource bridges only the dedicated Copilot capability into
// /ask. Read/write GitHub App credentials are structurally unavailable here.
// It neither consults gh nor accepts an ambient Copilot CLI login.
type GitHubCopilotTokenSource struct{ Auth *githubauth.ServiceClient }

func (s GitHubCopilotTokenSource) CopilotToken(ctx context.Context) (string, error) {
	if s.Auth == nil {
		return "", errors.New("dedicated Copilot GitHub App is unavailable")
	}
	return s.Auth.Token(ctx, githubauth.Copilot)
}

// AskRuntimeBuilder injects runtime construction so the daemon can keep a
// durable runtime without making GET/reload requests create Copilot sessions.
// Production wiring can start the official SDK client here; tests provide a
// deterministic Backend instead.
type AskRuntimeBuilder func(context.Context, copilot.ClientConfig) (*askruntime.Runtime, func() error, error)
type AskRuntimeFactory struct {
	Source         askruntime.TokenSource
	Build          AskRuntimeBuilder
	FallbackModels []askruntime.Model
	BaseDirectory  string
}

func (f AskRuntimeFactory) Open(ctx context.Context, workingDirectory string) (*askruntime.Runtime, func() error, error) {
	if f.Build == nil {
		return nil, nil, errors.New("Copilot /ask runtime is unavailable")
	}
	config, err := askruntime.ClientOptions(ctx, f.Source, workingDirectory, f.BaseDirectory)
	if err != nil {
		return nil, nil, err
	}
	runtime, close, err := f.Build(ctx, config)
	if err != nil {
		return nil, nil, fmt.Errorf("start Copilot /ask runtime: %w", err)
	}
	if runtime == nil {
		return nil, nil, errors.New("Copilot /ask runtime builder returned nil")
	}
	if close == nil {
		close = func() error { return nil }
	}
	return runtime, close, nil
}
func (f AskRuntimeFactory) Models(ctx context.Context, runtime *askruntime.Runtime) (askruntime.ModelResult, error) {
	return askruntime.Models(ctx, runtime, f.FallbackModels)
}

// WriteAskSSE delegates the whitelist/event escaping boundary to askruntime.
func WriteAskSSE(writer io.Writer, event askruntime.Delta) error {
	return askruntime.WriteSSE(writer, event)
}
