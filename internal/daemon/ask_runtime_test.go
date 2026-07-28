package daemon

import (
	"bytes"
	"context"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
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
