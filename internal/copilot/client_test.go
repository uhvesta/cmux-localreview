package copilot

import (
	"os"
	"path/filepath"
	"testing"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
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

func TestClientConfigUsesInstalledRuntimeWithoutAdoptingItsLogin(t *testing.T) {
	options, err := (ClientConfig{
		WorkingDirectory: "/tmp/review",
		BaseDirectory:    "/tmp/copilot-home",
		GitHubToken:      "dedicated-test-token",
		CLIPath:          "/opt/local/bin/copilot",
	}).SDKOptions()
	if err != nil {
		t.Fatalf("SDKOptions() error = %v", err)
	}
	connection, ok := options.Connection.(copilotsdk.StdioConnection)
	if !ok || connection.Path != "/opt/local/bin/copilot" {
		t.Fatalf("connection=%#v", options.Connection)
	}
	if options.UseLoggedInUser == nil || *options.UseLoggedInUser || options.GitHubToken != "dedicated-test-token" {
		t.Fatalf("installed runtime must still use only the injected credential: %#v", options)
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
	if config.SessionID != "ask-123" || config.Model != "gpt-5" || config.ReasoningEffort != "high" || config.ContextTier != copilotsdk.ContextTierLongContext || config.Streaming == nil || !*config.Streaming || config.OnPermissionRequest == nil {
		t.Fatalf("config = %#v", config)
	}
}

func TestAskPermissionHandlerAllowsOnlyWorkspaceReads(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "review.go")
	if err := os.WriteFile(inside, []byte("package review"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := (SessionConfig{ID: "ask-123", Model: "gpt-5", WorkingDirectory: workspace, Streaming: true}).SDKConfig()
	if err != nil {
		t.Fatal(err)
	}
	decision, err := config.OnPermissionRequest(copilotsdk.PermissionRequestRead{Path: inside}, copilotsdk.PermissionInvocation{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decision.(*rpc.PermissionDecisionApproveOnce); !ok {
		t.Fatalf("inside read decision=%T", decision)
	}
	decision, err = config.OnPermissionRequest(copilotsdk.PermissionRequestRead{Path: "../outside.go"}, copilotsdk.PermissionInvocation{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decision.(*rpc.PermissionDecisionReject); !ok {
		t.Fatalf("outside read decision=%T", decision)
	}
	decision, err = config.OnPermissionRequest(copilotsdk.PermissionRequestShell{}, copilotsdk.PermissionInvocation{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decision.(*rpc.PermissionDecisionReject); !ok {
		t.Fatalf("shell decision=%T", decision)
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
