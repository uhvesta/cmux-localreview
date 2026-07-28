// Package copilot adapts the official GitHub Copilot Go SDK to localreview's
// persisted /ask configuration. It deliberately does not start a runtime or
// send a prompt during configuration construction.
package copilot

import (
	"context"
	"errors"
	"strings"

	copilotsdk "github.com/github/copilot-sdk/go"
)

// ClientConfig identifies either a managed Copilot runtime or an already
// running TCP ACP/Copilot endpoint. Secrets are supplied by the caller's
// credential provider and are never persisted by this package.
type ClientConfig struct {
	Endpoint         string
	ConnectionToken  string
	WorkingDirectory string
	BaseDirectory    string
	GitHubToken      string
	UseLoggedInUser  *bool
}

// SDKOptions returns the SDK configuration without performing I/O. When
// Endpoint is set the SDK uses the existing runtime instead of spawning one.
func (config ClientConfig) SDKOptions() (*copilotsdk.ClientOptions, error) {
	if strings.TrimSpace(config.WorkingDirectory) == "" {
		return nil, errors.New("Copilot working directory is required")
	}

	options := &copilotsdk.ClientOptions{
		WorkingDirectory: config.WorkingDirectory,
		BaseDirectory:    config.BaseDirectory,
		GitHubToken:      config.GitHubToken,
		UseLoggedInUser:  config.UseLoggedInUser,
	}
	if endpoint := strings.TrimSpace(config.Endpoint); endpoint != "" {
		options.Connection = copilotsdk.URIConnection{
			URL:             endpoint,
			ConnectionToken: config.ConnectionToken,
		}
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
	}, nil
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
