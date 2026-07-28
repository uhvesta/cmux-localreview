package copilot

import (
	"testing"

	copilotsdk "github.com/github/copilot-sdk/go"
)

func TestClientConfigBuildsExternalRuntimeOptionsWithoutIO(t *testing.T) {
	loggedIn := false
	options, err := (ClientConfig{
		Endpoint:         "127.0.0.1:4101",
		ConnectionToken:  "test-connection-token",
		WorkingDirectory: "/tmp/review",
		BaseDirectory:    "/tmp/copilot-home",
		UseLoggedInUser:  &loggedIn,
	}).SDKOptions()
	if err != nil {
		t.Fatalf("SDKOptions() error = %v", err)
	}
	connection, ok := options.Connection.(copilotsdk.URIConnection)
	if !ok {
		t.Fatalf("Connection = %T, want URIConnection", options.Connection)
	}
	if connection.URL != "127.0.0.1:4101" || connection.ConnectionToken != "test-connection-token" {
		t.Fatalf("connection = %#v", connection)
	}
	if options.WorkingDirectory != "/tmp/review" || options.BaseDirectory != "/tmp/copilot-home" || options.UseLoggedInUser != &loggedIn {
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
	if _, err := (SessionConfig{WorkingDirectory: "/tmp/review"}).SDKConfig(); err == nil {
		t.Fatal("SDKConfig() error = nil, want missing model error")
	}
}
