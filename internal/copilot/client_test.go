package copilot

import (
	"testing"

	copilotsdk "github.com/github/copilot-sdk/go"
)

func TestClientConfigBuildsDedicatedSDKRuntimeOptionsWithoutIO(t *testing.T) {
	options, err := (ClientConfig{
		WorkingDirectory: "/tmp/review",
		BaseDirectory:    "/tmp/copilot-home",
		GitHubToken:      "dedicated-test-token",
	}).SDKOptions()
	if err != nil {
		t.Fatalf("SDKOptions() error = %v", err)
	}
	if _, isURI := options.Connection.(copilotsdk.URIConnection); isURI {
		t.Fatalf("Connection = URIConnection; ACP/URI runtimes are forbidden")
	}
	if options.WorkingDirectory != "/tmp/review" || options.BaseDirectory != "/tmp/copilot-home" || options.GitHubToken != "dedicated-test-token" || options.UseLoggedInUser == nil || *options.UseLoggedInUser || options.Mode != copilotsdk.ModeCopilotCli {
		t.Fatalf("options = %#v", options)
	}
}

func TestSessionConfigBuildsModelAndStreamingControlsWithoutIO(t *testing.T) {
	config, err := (SessionConfig{
		ID:               "ask-123",
		Model:            "gpt-5",
		ReasoningEffort:  "high",
		ContextTier:      copilotsdk.ContextTierLongContext,
		WorkingDirectory: "/tmp/review",
		Streaming:        true,
	}).SDKConfig()
	if err != nil {
		t.Fatalf("SDKConfig() error = %v", err)
	}
	if config.SessionID != "ask-123" || config.Model != "gpt-5" || config.ReasoningEffort != "high" || config.ContextTier != copilotsdk.ContextTierLongContext || config.Streaming == nil || !*config.Streaming {
		t.Fatalf("config = %#v", config)
	}
}

func TestConfigsRequireWorkingDirectoryAndModel(t *testing.T) {
	if _, err := (ClientConfig{}).SDKOptions(); err == nil {
		t.Fatal("SDKOptions() error = nil, want missing working directory error")
	}
	if _, err := (ClientConfig{WorkingDirectory: "/tmp/review"}).SDKOptions(); err == nil {
		t.Fatal("SDKOptions() error = nil, want missing dedicated runtime/auth configuration")
	}
	if _, err := (SessionConfig{WorkingDirectory: "/tmp/review"}).SDKConfig(); err == nil {
		t.Fatal("SDKConfig() error = nil, want missing model error")
	}
}
