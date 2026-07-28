package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
	"github.com/uhvesta/cmux-localreview/internal/githubauth"
)

type daemonTokenSource struct{ token string }

func (s daemonTokenSource) CopilotToken(context.Context) (string, error) { return s.token, nil }

func TestAskRuntimeFactoryOnlyUsesDedicatedInjectedToken(t *testing.T) {
	called := false
	factory := AskRuntimeFactory{Source: daemonTokenSource{token: "dedicated"}, BaseDirectory: "/base", Build: func(_ context.Context, config copilot.ClientConfig) (*askruntime.Runtime, func() error, error) {
		called = true
		options, err := config.SDKOptions()
		if err != nil || config.GitHubToken != "dedicated" || options.UseLoggedInUser == nil || *options.UseLoggedInUser {
			t.Fatalf("config=%#v", config)
		}
		return askruntime.New(nil), nil, nil
	}}
	runtime, close, err := factory.Open(context.Background(), "/workspace")
	if err != nil || runtime == nil || !called {
		t.Fatalf("runtime=%#v called=%v err=%v", runtime, called, err)
	}
	if err := close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (AskRuntimeFactory{}).Open(context.Background(), "/workspace"); err == nil {
		t.Fatal("missing runtime builder must fail")
	}
}

func TestProductionFactoryUsesOnlyDedicatedCopilotCapability(t *testing.T) {
	auth := githubauth.New(
		authSecrets{githubauth.Service + "/github.com:copilot": `{"accessToken":"dedicated-token","clientId":"Iv1.copilot-fixture"}`},
		authConfig{githubauth.Copilot: "Iv1.copilot-fixture"},
		authTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("token should not hit HTTP") }),
		func(string) error { return nil },
	)
	factory := NewProductionAskRuntimeFactory(auth, "/state")
	if factory.BaseDirectory != filepath.Join("/state", "copilot-sdk") || factory.Build == nil {
		t.Fatalf("factory=%#v", factory)
	}

	// Replace only the process-spawning boundary. Open still exercises the
	// production credential source and proves read/write credentials cannot be
	// substituted for the dedicated Copilot capability.
	factory.Build = func(_ context.Context, config copilot.ClientConfig) (*askruntime.Runtime, func() error, error) {
		if config.GitHubToken != "dedicated-token" || config.WorkingDirectory != "/workspace" {
			t.Fatalf("config=%#v", config)
		}
		return askruntime.New(nil), nil, nil
	}
	if _, _, err := factory.Open(context.Background(), "/workspace"); err != nil {
		t.Fatal(err)
	}
}

func TestStartWiresProductionFactoryWithoutStartingCopilot(t *testing.T) {
	dir := t.TempDir()
	auth := githubauth.New(authSecrets{}, authConfig{}, authTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no network during daemon startup")
	}), func(string) error { return nil })
	d, err := Start(context.Background(), Options{DataDir: dir, GitHubAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.askFactory == nil || d.askFactory.Build == nil {
		t.Fatal("Start did not install the production /ask runtime factory")
	}
	if got, want := d.askFactory.BaseDirectory, filepath.Join(dir, "copilot-sdk"); got != want {
		t.Fatalf("base directory=%q want %q", got, want)
	}
	if info, err := os.Stat(d.askFactory.BaseDirectory); err != nil || !info.IsDir() {
		t.Fatal(err)
	}
}

func TestProductionFactoryReportsMissingCopilotAuthBeforeStartingSDK(t *testing.T) {
	auth := githubauth.New(authSecrets{}, authConfig{githubauth.Copilot: "Iv1.copilot-fixture"}, nil, nil)
	factory := NewProductionAskRuntimeFactory(auth, t.TempDir())
	called := false
	factory.Build = func(context.Context, copilot.ClientConfig) (*askruntime.Runtime, func() error, error) {
		called = true
		return askruntime.New(nil), nil, nil
	}
	if _, _, err := factory.Open(context.Background(), "/workspace"); err == nil || !strings.Contains(err.Error(), "copilot GitHub App is not connected") {
		t.Fatalf("Open error=%v, want explicit missing Copilot authentication", err)
	}
	if called {
		t.Fatal("SDK runtime started without a dedicated Copilot credential")
	}
}
func TestWriteAskSSE(t *testing.T) {
	var b bytes.Buffer
	if err := WriteAskSSE(&b, askruntime.Delta{Event: askruntime.EventStatus, ConversationID: "c", Text: "working"}); err != nil {
		t.Fatal(err)
	}
	if b.String() == "" {
		t.Fatal("empty SSE")
	}
	if err := WriteAskSSE(&b, askruntime.Delta{Event: askruntime.EventKind("bad\n")}); err == nil {
		t.Fatal("accepted unsafe event")
	}
}
