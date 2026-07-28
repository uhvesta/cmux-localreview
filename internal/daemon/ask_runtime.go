package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
	"github.com/uhvesta/cmux-localreview/internal/githubauth"
)

// askWorkspaceDefaults reads the active review workspace only. It deliberately
// does not fall back to a process cwd: picker selections must never leak from
// an arbitrary terminal directory into another reviewed workspace. Before a
// workspace is opened it returns an empty, non-persisted default record so
// Queue Home and injected-runtime tests can still render/use Copilot auto.
func (d *Daemon) askWorkspaceDefaults(ctx context.Context) (*ask.WorkspaceSettings, error) {
	d.mu.Lock()
	workspace := ""
	if d.review != nil {
		workspace = d.review.Root
	}
	d.mu.Unlock()
	if workspace == "" {
		return &ask.WorkspaceSettings{}, nil
	}
	return ask.GetWorkspaceSettings(ctx, d.db, workspace)
}

// GitHubCopilotTokenSource bridges only the dedicated Copilot capability into
// /ask. Read/write GitHub OAuth App credentials are structurally unavailable here.
// It neither consults gh nor accepts an ambient Copilot CLI login.
type GitHubCopilotTokenSource struct{ Auth *githubauth.ServiceClient }

func (s GitHubCopilotTokenSource) CopilotToken(ctx context.Context) (string, error) {
	if s.Auth == nil {
		return "", errors.New("dedicated Copilot GitHub OAuth App is unavailable")
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

// NewProductionAskRuntimeFactory supplies the daemon's only production /ask
// transport.  The SDK receives a token exclusively through the dedicated
// Copilot GitHub OAuth App capability; ClientConfig explicitly disables the SDK's
// logged-in-user discovery, so this never inherits gh or Copilot CLI state.
//
// Construction is intentionally inert.  The SDK child process is started
// only by Open, which is reached by an explicit prompt or model-picker refresh
// and never by transcript/review reloads.
func NewProductionAskRuntimeFactory(auth *githubauth.ServiceClient, dataDirectory string) *AskRuntimeFactory {
	return &AskRuntimeFactory{
		Source:        GitHubCopilotTokenSource{Auth: auth},
		BaseDirectory: filepath.Join(dataDirectory, "copilot-sdk"),
		Build:         buildProductionAskRuntime,
	}
}

// buildProductionAskRuntime is deliberately small so the only point that can
// spawn the official Copilot SDK runtime is auditable.  A failed Start closes
// the client immediately; callers therefore receive a concrete unavailable or
// authentication error instead of a fake successful /ask session.
func buildProductionAskRuntime(ctx context.Context, config copilot.ClientConfig) (*askruntime.Runtime, func() error, error) {
	client, err := copilot.NewClient(config)
	if err != nil {
		return nil, nil, err
	}
	if err := client.Start(ctx); err != nil {
		_ = client.Stop()
		return nil, nil, err
	}
	runtime := askruntime.New(askruntime.SDKBackend{Client: client})
	return runtime, client.Stop, nil
}

func (f AskRuntimeFactory) Open(ctx context.Context, workingDirectory string) (*askruntime.Runtime, func() error, error) {
	if f.Build == nil {
		return nil, nil, errors.New("Copilot /ask runtime is unavailable")
	}
	config, err := askruntime.ClientOptions(ctx, f.Source, workingDirectory, f.BaseDirectory)
	if err != nil {
		return nil, nil, err
	}
	// Prefer an installed current CLI when available. This remains an official
	// Go SDK child process with the dedicated token injected below; it never
	// adopts the CLI's login or ACP state. The SDK's embedded CLI stays the
	// fallback for release installs that have not installed Copilot yet.
	config.CLIPath = copilot.PreferredCLIPath()
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

// askModels opens at most the SDK client, never an SDK conversation/session.
// The client is retained for the next explicit turn so model-picker refreshes
// do not repeatedly consume authentication or spawn hidden conversations.
func (d *Daemon) askModels(ctx context.Context) (askruntime.ModelResult, error) {
	d.askMu.Lock()
	runtime := d.askRuntime
	factory := d.askFactory
	d.askMu.Unlock()
	if factory == nil {
		return askruntime.ModelResult{}, errors.New("Copilot /ask is not connected; authenticate Copilot to load models")
	}
	if runtime == nil {
		workingDirectory, err := d.askWorkingDirectory()
		if err != nil {
			return askruntime.ModelResult{}, err
		}
		opened, closeRuntime, err := factory.Open(ctx, workingDirectory)
		if err != nil {
			return askruntime.ModelResult{}, err
		}
		d.askMu.Lock()
		if d.askRuntime == nil {
			d.askRuntime, d.askClose, runtime = opened, closeRuntime, opened
		} else {
			runtime = d.askRuntime
		}
		d.askMu.Unlock()
		if runtime != opened && closeRuntime != nil {
			_ = closeRuntime()
		}
	}
	return factory.Models(ctx, runtime)
}

// WriteAskSSE delegates the whitelist/event escaping boundary to askruntime.
func WriteAskSSE(writer io.Writer, event askruntime.Delta) error {
	return askruntime.WriteSSE(writer, event)
}
