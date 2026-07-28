package e2ecopilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
)

func TestFixtureListsModelsStreamsAndCancels(t *testing.T) {
	backend := NewBackend()
	models, err := backend.ListModels(context.Background())
	if err != nil || len(models) < 2 || models[0].ID != "gpt-5" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	session, err := backend.OpenSession(context.Background(), copilot.SessionConfig{Model: "claude-sonnet-4.6"})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan askruntime.RuntimeEvent, 32)
	remove := session.On(func(event askruntime.RuntimeEvent) { events <- event })
	defer remove()
	if _, err := session.SendPrompt(context.Background(), "Question:\nExplain this fixture"); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == askruntime.EventDelta {
				body.WriteString(event.Text)
			}
			if event.Kind == askruntime.EventDone {
				if !strings.Contains(body.String(), "claude-sonnet-4.6") || !strings.Contains(body.String(), "Explain this fixture") {
					t.Fatalf("body=%q", body.String())
				}
				goto cancelled
			}
		case <-deadline:
			t.Fatal("fixture did not finish")
		}
	}

cancelled:
	if _, err := session.SendPrompt(context.Background(), "Question:\nThis response must be cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := session.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Kind == askruntime.EventDone {
			t.Fatal("cancelled fixture turn emitted a completion")
		}
	case <-time.After(180 * time.Millisecond):
		// Expected: abort suppresses later deltas/completion. Runtime itself
		// emits and persists the terminal aborted event.
	}
}
