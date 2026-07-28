package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
)

// askRuntimeForTurn lazily opens the daemon-owned SDK runtime only after an
// explicit POSTed question. In particular, GET transcript/reload requests
// never reach this function, which is the no-resend-on-reload invariant.
func (d *Daemon) askRuntimeForTurn(ctx context.Context, workingDirectory string) (*askruntime.Runtime, error) {
	d.askMu.Lock()
	current := d.askRuntime
	factory := d.askFactory
	d.askMu.Unlock()
	if current != nil {
		return current, nil
	}
	if factory == nil {
		return nil, errors.New("Copilot /ask is not connected; authenticate Copilot then try again")
	}
	opened, closeRuntime, err := factory.Open(ctx, workingDirectory)
	if err != nil {
		return nil, err
	}
	d.askMu.Lock()
	if d.askRuntime == nil {
		d.askRuntime, d.askClose = opened, closeRuntime
		current = opened
	} else {
		current = d.askRuntime
	}
	d.askMu.Unlock()
	if current != opened && closeRuntime != nil {
		_ = closeRuntime()
	}
	return current, nil
}

func (d *Daemon) askWorkingDirectory() (string, error) {
	d.mu.Lock()
	root := ""
	if d.review != nil {
		root = d.review.Root
	}
	d.mu.Unlock()
	if strings.TrimSpace(root) != "" {
		return root, nil
	}
	// Tests and CLI-created conversations without an opened reviewer can still
	// use an injected runtime. Production SDK calls retain a concrete cwd rather
	// than silently deriving a path from an inline location string.
	return os.Getwd()
}

func askSessionConfig(conversation *ask.Conversation, workingDirectory string) copilot.SessionConfig {
	model := "auto"
	if conversation.Model != nil && strings.TrimSpace(*conversation.Model) != "" {
		model = *conversation.Model
	}
	reasoning := ""
	if conversation.ReasoningEffort != nil {
		reasoning = string(*conversation.ReasoningEffort)
	}
	tier := copilotsdk.ContextTierDefault
	if conversation.ContextTier == nil || *conversation.ContextTier == ask.ContextDefault {
		tier = copilotsdk.ContextTierDefault
	} else if *conversation.ContextTier == ask.ContextLong {
		tier = copilotsdk.ContextTierLongContext
	}
	return copilot.SessionConfig{ID: conversation.ID, Model: model, ReasoningEffort: reasoning, ContextTier: tier, WorkingDirectory: workingDirectory, Streaming: true}
}

// askTurnConfig merges the active workspace picker defaults only into unset
// conversation fields. An explicit conversation selection remains stable when
// the workspace default changes, and a transcript GET never calls this path.
func (d *Daemon) askTurnConfig(conversation *ask.Conversation, workingDirectory string) (copilot.SessionConfig, error) {
	defaults, err := d.askWorkspaceDefaults(context.Background())
	if err != nil {
		return copilot.SessionConfig{}, err
	}
	merged := *conversation
	if merged.Model == nil {
		merged.Model = defaults.Model
	}
	if merged.ReasoningEffort == nil {
		merged.ReasoningEffort = defaults.ReasoningEffort
	}
	if merged.ContextTier == nil {
		merged.ContextTier = defaults.ContextTier
	}
	return askSessionConfig(&merged, workingDirectory), nil
}

// askPrompt is deliberately a fresh, self-contained turn envelope. The SDK
// session retains prior explicit turns itself; reopening the page does not
// reconstruct or resend them. WorkspacePath is the unambiguous path the user
// saw in a multi-repository review, while FilePath preserves GitHub/Repo
// provenance for an inline answer.
func askPrompt(body string, location *ask.Location, fallback *ask.Location) string {
	anchor := location
	if anchor == nil {
		anchor = fallback
	}
	if anchor == nil {
		return strings.TrimSpace(body)
	}
	var out strings.Builder
	out.WriteString("You are answering a local code-review /ask question. Use the supplied workspace and code anchor; do not turn this into formal review feedback.\n")
	if anchor.WorkspacePath != "" {
		fmt.Fprintf(&out, "Workspace-relative path: %s\n", anchor.WorkspacePath)
	}
	if anchor.FilePath != "" {
		fmt.Fprintf(&out, "Repository-relative path: %s\n", anchor.FilePath)
	}
	if anchor.RepoID != "" {
		fmt.Fprintf(&out, "Repository: %s\n", anchor.RepoID)
	}
	if anchor.StartLine > 0 {
		end := anchor.EndLine
		if end == 0 {
			end = anchor.StartLine
		}
		fmt.Fprintf(&out, "Side: %s; lines: %d-%d\n", anchor.Side, anchor.StartLine, end)
	}
	if anchor.SelectedCode != "" {
		out.WriteString("Selected code:\n```\n")
		out.WriteString(anchor.SelectedCode)
		out.WriteString("\n```\n")
	}
	out.WriteString("Question:\n")
	out.WriteString(strings.TrimSpace(body))
	return out.String()
}

func (d *Daemon) addAskWatcher(conversationID string) (chan askruntime.Delta, func()) {
	watcher := make(chan askruntime.Delta, 32)
	d.askMu.Lock()
	if d.askWatchers[conversationID] == nil {
		d.askWatchers[conversationID] = make(map[chan askruntime.Delta]struct{})
	}
	d.askWatchers[conversationID][watcher] = struct{}{}
	d.askMu.Unlock()
	return watcher, func() {
		d.askMu.Lock()
		if watchers := d.askWatchers[conversationID]; watchers != nil {
			if _, exists := watchers[watcher]; exists {
				delete(watchers, watcher)
			}
			if len(watchers) == 0 {
				delete(d.askWatchers, conversationID)
			}
		}
		d.askMu.Unlock()
	}
}

func (d *Daemon) publishAskEvent(event askruntime.Delta) {
	d.askMu.Lock()
	watchers := make([]chan askruntime.Delta, 0, len(d.askWatchers[event.ConversationID]))
	for watcher := range d.askWatchers[event.ConversationID] {
		watchers = append(watchers, watcher)
	}
	d.askMu.Unlock()
	for _, watcher := range watchers {
		select {
		case watcher <- event:
		default:
			// A slow UI can reload its durable transcript. Never let it stall the
			// SDK callback or drop a database update.
		}
	}
}

func (d *Daemon) persistAskEvent(conversationID string, assistantID int64, event askruntime.Delta) {
	ctx := context.Background()
	event.ConversationID = conversationID
	switch event.Event {
	case askruntime.EventStatus:
		d.publishAskEvent(event)
	case askruntime.EventDelta, askruntime.EventReasoningDelta:
		if _, err := ask.AppendPendingMessage(ctx, d.db, assistantID, event.Text); err == nil {
			d.publishAskEvent(event)
		}
	case askruntime.EventDone:
		fallback := ""
		if event.Aborted {
			fallback = "Response cancelled before it completed."
		}
		if _, changed, err := ask.SettlePendingMessage(ctx, d.db, assistantID, fallback); err == nil && changed {
			d.publishAskEvent(event)
		}
	case askruntime.EventError:
		fallback := "Copilot could not complete this response."
		if strings.TrimSpace(event.Error) != "" {
			fallback = "Copilot error: " + event.Error
		}
		if _, changed, err := ask.SettlePendingMessage(ctx, d.db, assistantID, fallback); err == nil && changed {
			d.publishAskEvent(event)
		}
	}
}

func (d *Daemon) startAskTurn(conversation *ask.Conversation, user *ask.Message, assistant *ask.Message) error {
	workingDirectory, err := d.askWorkingDirectory()
	if err != nil {
		return err
	}
	runtime, err := d.askRuntimeForTurn(context.Background(), workingDirectory)
	if err != nil {
		return err
	}
	prompt := askPrompt(user.Body, user.Location, conversation.Context)
	config, err := d.askTurnConfig(conversation, workingDirectory)
	if err != nil {
		return err
	}
	go func() {
		_, sendErr := runtime.Send(context.Background(), conversation.ID, config, prompt, func(event askruntime.Delta) {
			d.persistAskEvent(conversation.ID, assistant.ID, event)
		})
		if sendErr != nil {
			d.persistAskEvent(conversation.ID, assistant.ID, askruntime.Delta{Event: askruntime.EventError, Error: sendErr.Error()})
		}
	}()
	return nil
}
