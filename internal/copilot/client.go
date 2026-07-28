// Package copilot adapts the official GitHub Copilot Go SDK to localreview's
// persisted /ask configuration. It deliberately does not start a runtime or
// send a prompt during configuration construction.
package copilot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

// ClientConfig identifies a fresh, daemon-owned Copilot SDK runtime. Secrets
// are supplied by the caller's dedicated credential provider and are never
// persisted by this package. It deliberately has no URI/TCP/ACP connection
// fields: /ask sessions are SDK-native, local processes.
type ClientConfig struct {
	WorkingDirectory string
	BaseDirectory    string
	GitHubToken      string
}

// SDKOptions returns the SDK configuration without performing I/O. The SDK is
// explicitly forbidden from discovering a user's Copilot CLI or gh login;
// only GitHubToken supplied by the daemon credential boundary is accepted.
func (config ClientConfig) SDKOptions() (*copilotsdk.ClientOptions, error) {
	if strings.TrimSpace(config.WorkingDirectory) == "" {
		return nil, errors.New("Copilot working directory is required")
	}
	if strings.TrimSpace(config.BaseDirectory) == "" {
		return nil, errors.New("Copilot base directory is required")
	}
	if strings.TrimSpace(config.GitHubToken) == "" {
		return nil, errors.New("dedicated Copilot GitHub token is required")
	}
	loggedIn := false

	options := &copilotsdk.ClientOptions{
		WorkingDirectory: config.WorkingDirectory,
		BaseDirectory:    config.BaseDirectory,
		GitHubToken:      config.GitHubToken,
		UseLoggedInUser:  &loggedIn,
		Mode:             copilotsdk.ModeCopilotCli,
	}
	return options, nil
}

// NewClient constructs a not-yet-started SDK client. Calling this function
// never authenticates, starts a child process, or sends a prompt.
func NewClient(config ClientConfig) (*copilotsdk.Client, error) {
	options, err := config.SDKOptions()
	if err != nil {
		return nil, err
	}
	return copilotsdk.NewClient(options), nil
}

// SessionConfig contains the model controls selected for one persistent /ask
// conversation.
type SessionConfig struct {
	ID               string
	Model            string
	ReasoningEffort  string
	ContextTier      copilotsdk.ContextTier
	WorkingDirectory string
	Streaming        bool
}

// SDKConfig converts the saved UI choices to the official SDK session shape.
// It does not create a remote session.
func (config SessionConfig) SDKConfig() (*copilotsdk.SessionConfig, error) {
	if strings.TrimSpace(config.WorkingDirectory) == "" {
		return nil, errors.New("Copilot session working directory is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("Copilot model is required")
	}
	streaming := config.Streaming
	return &copilotsdk.SessionConfig{
		SessionID:        config.ID,
		ClientName:       "cmux-localreview",
		Model:            config.Model,
		ReasoningEffort:  config.ReasoningEffort,
		ContextTier:      config.ContextTier,
		WorkingDirectory: config.WorkingDirectory,
		Streaming:        &streaming,
		// /ask is a read-only reviewer.  Do not leave permissions pending in a
		// headless daemon: allow a normal read under the reviewed workspace and
		// reject every write, shell, network, MCP, memory, or sandbox-bypass
		// request. This keeps an inline question capable of inspecting its
		// supplied path without granting an agent ambient machine authority.
		OnPermissionRequest: reviewReadPermission(config.WorkingDirectory),
	}, nil
}

func reviewReadPermission(workingDirectory string) copilotsdk.PermissionHandlerFunc {
	root, err := filepath.Abs(workingDirectory)
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
			root = resolved
		}
	}
	return func(request copilotsdk.PermissionRequest, _ copilotsdk.PermissionInvocation) (rpc.PermissionDecision, error) {
		read, ok := request.(copilotsdk.PermissionRequestRead)
		if !ok {
			if pointer, pointerOK := request.(*copilotsdk.PermissionRequestRead); pointerOK && pointer != nil {
				read, ok = *pointer, true
			}
		}
		if !ok || err != nil || (read.RequestSandboxBypass != nil && *read.RequestSandboxBypass) || !pathInside(root, read.Path) {
			feedback := "cmux-localreview /ask permits only ordinary reads inside the reviewed workspace"
			return &rpc.PermissionDecisionReject{Feedback: &feedback}, nil
		}
		return &rpc.PermissionDecisionApproveOnce{}, nil
	}
}

func pathInside(root, requested string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(requested) == "" {
		return false
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	// Resolve existing links before containment checking so a workspace symlink
	// cannot turn an apparently local file read into an external one.
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	rel, err := filepath.Rel(root, abs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CreateSession is the explicit I/O boundary used when a reviewer asks to
// start a fresh /ask conversation. The caller owns authentication and errors.
func CreateSession(ctx context.Context, client *copilotsdk.Client, config SessionConfig) (*copilotsdk.Session, error) {
	if client == nil {
		return nil, errors.New("Copilot client is required")
	}
	sdkConfig, err := config.SDKConfig()
	if err != nil {
		return nil, err
	}
	return client.CreateSession(ctx, sdkConfig)
}
