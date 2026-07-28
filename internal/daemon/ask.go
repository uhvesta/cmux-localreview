package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
)

func (d *Daemon) askSessionID() *int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.review == nil {
		return nil
	}
	id := d.review.SessionID
	return &id
}

func askError(w http.ResponseWriter, err error) {
	if errors.Is(err, ask.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown /ask record"})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

// handleAsk owns only durable conversational metadata. It deliberately never
// creates an SDK session during GET requests, so reload/reopen is side-effect
// free and cannot replay a prompt.
func (d *Daemon) handleAsk(w http.ResponseWriter, r *http.Request, path string) bool {
	ctx := r.Context()
	if path == "/ask/models" && r.Method == http.MethodGet {
		defaults, defaultsErr := d.askWorkspaceDefaults(ctx)
		if defaultsErr != nil {
			askError(w, defaultsErr)
			return true
		}
		result, err := d.askModels(ctx)
		if err != nil {
			// Keep the model picker usable while clearly identifying this as an
			// offline choice—not an SDK-provided model list.
			writeJSON(w, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "auto", "name": "Copilot default (connect to load models)", "capabilities": map[string]any{"supports": map[string]bool{"reasoningEffort": true, "contextTier": true}}}}, "state": "unavailable", "source": "fallback", "warning": err.Error(), "workspaceDefaults": defaults})
			return true
		}
		models := make([]map[string]any, 0, len(result.Models))
		for _, model := range result.Models {
			models = append(models, map[string]any{"id": model.ID, "name": model.Name, "capabilities": map[string]any{"supports": map[string]bool{"reasoningEffort": true, "contextTier": true}}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models, "state": "ready", "source": result.Source, "warning": result.Warning, "workspaceDefaults": defaults})
		return true
	}
	if path == "/ask/settings" && r.Method == http.MethodGet {
		settings, err := d.askWorkspaceDefaults(ctx)
		if err != nil {
			askError(w, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"workspaceDefaults": settings})
		}
		return true
	}
	if path == "/ask/settings" && (r.Method == http.MethodPut || r.Method == http.MethodPost) {
		var in struct {
			Model           *string              `json:"model"`
			ReasoningEffort *ask.ReasoningEffort `json:"reasoningEffort"`
			ContextTier     *ask.ContextTier     `json:"contextTier"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			askError(w, errors.New("invalid /ask workspace settings"))
			return true
		}
		current, err := d.askWorkspaceDefaults(ctx)
		if err != nil {
			askError(w, err)
			return true
		}
		if strings.TrimSpace(current.WorkspacePath) == "" {
			askError(w, errors.New("open a workspace before saving /ask defaults"))
			return true
		}
		settings, err := ask.UpdateWorkspaceSettings(ctx, d.db, current.WorkspacePath, ask.UpdateWorkspaceSettingsInput{Model: in.Model, ReasoningEffort: in.ReasoningEffort, ContextTier: in.ContextTier})
		if err != nil {
			askError(w, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"workspaceDefaults": settings})
		}
		return true
	}
	if path == "/ask/inline-conversations" && r.Method == http.MethodPost {
		var in struct {
			Model   string        `json:"model"`
			Context *ask.Location `json:"context"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil || in.Context == nil || strings.TrimSpace(in.Context.FilePath) == "" || in.Context.StartLine < 1 {
			askError(w, errors.New("An inline /ask conversation needs filePath and startLine"))
			return true
		}
		session := d.askSessionID()
		conversations, err := ask.ListConversations(ctx, d.db, session, false)
		if err != nil {
			askError(w, err)
			return true
		}
		if len(conversations) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"conversation": conversations[0], "reused": true, "shared": true})
			return true
		}
		conversation, err := ask.CreateConversation(ctx, d.db, ask.CreateConversationInput{ReviewSessionID: session, Model: in.Model, Context: in.Context})
		if err != nil {
			askError(w, err)
		} else {
			writeJSON(w, http.StatusCreated, map[string]any{"conversation": conversation, "reused": false, "shared": true})
		}
		return true
	}
	if path == "/ask/conversations" && r.Method == http.MethodGet {
		// `history=true` is retained as a read-only compatibility spelling for
		// the frozen reviewer UI. Native callers use `includeArchived=true`;
		// both simply reveal durable, archived review rounds and never resume,
		// re-open, or re-send any Copilot work.
		includeArchived := r.URL.Query().Get("includeArchived") == "true" || r.URL.Query().Get("history") == "true"
		items, err := ask.ListConversations(ctx, d.db, d.askSessionID(), includeArchived)
		if err != nil {
			askError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"activeReviewSessionId": d.askSessionID(), "conversations": items})
		return true
	}
	if path == "/ask/conversations" && r.Method == http.MethodPost {
		var in struct {
			Model           string               `json:"model"`
			ReasoningEffort *ask.ReasoningEffort `json:"reasoningEffort"`
			ContextTier     *ask.ContextTier     `json:"contextTier"`
			QueueItemID     string               `json:"queueItemId"`
			Context         *ask.Location        `json:"context"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			askError(w, errors.New("invalid /ask conversation"))
			return true
		}
		conversation, err := ask.CreateConversation(ctx, d.db, ask.CreateConversationInput{QueueItemID: in.QueueItemID, ReviewSessionID: d.askSessionID(), Model: in.Model, ReasoningEffort: in.ReasoningEffort, ContextTier: in.ContextTier, Context: in.Context})
		if err != nil {
			askError(w, err)
			return true
		}
		writeJSON(w, http.StatusCreated, map[string]any{"conversation": conversation})
		return true
	}
	if path == "/ask/conversations/fresh" && r.Method == http.MethodPost {
		var in struct {
			Model           string               `json:"model"`
			ReasoningEffort *ask.ReasoningEffort `json:"reasoningEffort"`
			ContextTier     *ask.ContextTier     `json:"contextTier"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in)
		if session := d.askSessionID(); session != nil {
			if err := ask.ArchiveActive(ctx, d.db, *session); err != nil {
				askError(w, err)
				return true
			}
		}
		conversation, err := ask.CreateConversation(ctx, d.db, ask.CreateConversationInput{ReviewSessionID: d.askSessionID(), Model: in.Model, ReasoningEffort: in.ReasoningEffort, ContextTier: in.ContextTier})
		if err != nil {
			askError(w, err)
			return true
		}
		writeJSON(w, http.StatusCreated, map[string]any{"conversation": conversation})
		return true
	}
	if path == "/ask/question-sets" && r.Method == http.MethodGet {
		items, err := ask.ListQuestionSets(ctx, d.db)
		if err != nil {
			askError(w, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"questionSets": items})
		}
		return true
	}
	if path == "/ask/question-sets" && r.Method == http.MethodPost {
		var in struct {
			Name      string   `json:"name"`
			Questions []string `json:"questions"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) != nil {
			askError(w, errors.New("invalid question set"))
			return true
		}
		item, err := ask.CreateQuestionSet(ctx, d.db, in.Name, in.Questions)
		if err != nil {
			askError(w, err)
		} else {
			writeJSON(w, http.StatusCreated, map[string]any{"questionSet": item})
		}
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 4 && parts[0] == "ask" && parts[1] == "question-sets" && parts[3] == "send" && r.Method == http.MethodPost {
		d.handleQuestionSetDelivery(w, r, parts[2])
		return true
	}
	if len(parts) == 3 && parts[0] == "ask" && parts[1] == "question-sets" {
		id := parts[2]
		if r.Method == http.MethodGet {
			item, err := ask.GetQuestionSet(ctx, d.db, id)
			if err != nil {
				askError(w, err)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"questionSet": item})
			}
			return true
		}
		if r.Method == http.MethodDelete {
			ok, err := ask.DeleteQuestionSet(ctx, d.db, id)
			if err != nil {
				askError(w, err)
			} else if !ok {
				askError(w, ask.ErrNotFound)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return true
		}
		if r.Method == http.MethodPut {
			var in struct {
				Name      string   `json:"name"`
				Questions []string `json:"questions"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				askError(w, errors.New("invalid question set"))
				return true
			}
			item, err := ask.ReplaceQuestionSet(ctx, d.db, id, in.Name, in.Questions)
			if err != nil {
				askError(w, err)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"questionSet": item})
			}
			return true
		}
	}
	if len(parts) == 4 && parts[0] == "ask" && parts[1] == "conversations" {
		id, action := parts[2], parts[3]
		if action == "messages" && r.Method == http.MethodPost {
			var in struct {
				// Body is the durable public API. Prompt is accepted for the
				// reviewer UI shipped with older builds, which posts prompt and
				// expects a same-request SSE response. Keeping both forms avoids
				// a reload or an upgraded static bundle silently dropping a turn.
				Body     string        `json:"body"`
				Prompt   string        `json:"prompt"`
				Location *ask.Location `json:"location"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
				askError(w, errors.New("an /ask message body is required"))
				return true
			}
			if strings.TrimSpace(in.Body) == "" {
				in.Body = in.Prompt
			}
			if strings.TrimSpace(in.Body) == "" {
				askError(w, errors.New("an /ask message body is required"))
				return true
			}
			if _, err := ask.GetConversation(ctx, d.db, id); err != nil {
				askError(w, err)
				return true
			}
			user, err := ask.InsertMessage(ctx, d.db, id, ask.RoleUser, in.Body, false, in.Location)
			if err != nil {
				askError(w, err)
				return true
			}
			// Persist the pending assistant row before invoking any SDK transport.
			// A daemon restart can therefore settle it visibly rather than losing
			// the user's question or silently replaying it on reload.
			assistant, err := ask.InsertMessage(ctx, d.db, id, ask.RoleAssistant, "", true, nil)
			if err != nil {
				askError(w, err)
				return true
			}
			conversation, err := ask.GetConversation(ctx, d.db, id)
			if err != nil {
				askError(w, err)
				return true
			}
			// The current reviewer bundle uses prompt and consumes this request
			// as an SSE stream. Register the watcher before starting the runtime
			// so its first status/delta cannot race the browser connection.
			if strings.TrimSpace(in.Prompt) != "" {
				watcher, remove := d.addAskWatcher(id)
				defer remove()
				if err := d.startAskTurn(conversation, user, assistant); err != nil {
					d.persistAskEvent(id, assistant.ID, askruntime.Delta{Event: askruntime.EventError, Error: err.Error()})
					askError(w, err)
					return true
				}
				d.streamAskEvents(w, r, id, watcher)
				return true
			}
			if err := d.startAskTurn(conversation, user, assistant); err != nil {
				// The question remains visible and terminal rather than becoming a
				// forever-pending row. Crucially we never retry it from a reload.
				d.persistAskEvent(id, assistant.ID, askruntime.Delta{Event: askruntime.EventError, Error: err.Error()})
				settled, _ := ask.GetMessage(ctx, d.db, assistant.ID)
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"user": user, "assistant": settled, "delivery": "unavailable", "error": err.Error()})
				return true
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"user": user, "assistant": assistant, "delivery": "streaming"})
			return true
		}
		if action == "cancel" && r.Method == http.MethodPost {
			if _, err := ask.GetConversation(ctx, d.db, id); err != nil {
				askError(w, err)
				return true
			}
			d.askMu.Lock()
			runtime := d.askRuntime
			d.askMu.Unlock()
			aborted := false
			if runtime != nil {
				var cancelErr error
				aborted, cancelErr = runtime.Cancel(ctx, id)
				if cancelErr != nil {
					askError(w, cancelErr)
					return true
				}
			}
			messages, err := ask.ListMessages(ctx, d.db, id)
			if err != nil {
				askError(w, err)
				return true
			}
			changed := int64(0)
			for _, message := range messages {
				if !message.Pending || message.Role != ask.RoleAssistant {
					continue
				}
				if _, settled, settleErr := ask.SettlePendingMessage(ctx, d.db, message.ID, "Response cancelled before it completed."); settleErr != nil {
					askError(w, settleErr)
					return true
				} else if settled {
					changed++
				}
			}
			if changed > 0 {
				d.publishAskEvent(askruntime.Delta{Event: askruntime.EventDone, ConversationID: id, Aborted: true})
			}
			writeJSON(w, http.StatusOK, map[string]any{"cancelled": changed > 0, "abortRequested": aborted})
			return true
		}
		if action == "resume" && r.Method == http.MethodPost {
			item, err := ask.Resume(ctx, d.db, id)
			if err != nil {
				askError(w, err)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"conversation": item})
			}
			return true
		}
		if (action == "model" || action == "settings") && r.Method == http.MethodPost {
			var in struct {
				Model           *string              `json:"model"`
				ReasoningEffort *ask.ReasoningEffort `json:"reasoningEffort"`
				ContextTier     *ask.ContextTier     `json:"contextTier"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				askError(w, errors.New("invalid /ask settings"))
				return true
			}
			d.askMu.Lock()
			runtime := d.askRuntime
			d.askMu.Unlock()
			if runtime != nil {
				if resetErr := runtime.ResetSession(id); resetErr != nil {
					askError(w, errors.New("wait for Copilot to finish or cancel the current response before changing its model or context"))
					return true
				}
			}
			item, err := ask.UpdateConversation(ctx, d.db, id, ask.UpdateConversationInput{Model: in.Model, ReasoningEffort: in.ReasoningEffort, ContextTier: in.ContextTier})
			if err != nil {
				askError(w, err)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"conversation": item})
			}
			return true
		}
	}
	if len(parts) == 4 && parts[0] == "ask" && parts[1] == "conversations" && parts[3] == "events" && r.Method == http.MethodGet {
		if _, err := ask.GetConversation(ctx, d.db, parts[2]); err != nil {
			askError(w, err)
			return true
		}
		watcher, remove := d.addAskWatcher(parts[2])
		defer remove()
		d.streamAskEvents(w, r, parts[2], watcher)
		return true
	}
	if len(parts) == 3 && parts[0] == "ask" && parts[1] == "conversations" && r.Method == http.MethodGet {
		conversation, err := ask.GetConversation(ctx, d.db, parts[2])
		if err != nil {
			askError(w, err)
			return true
		}
		messages, err := ask.ListMessages(ctx, d.db, conversation.ID)
		if err != nil {
			askError(w, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"conversation": conversation, "messages": messages})
		}
		return true
	}
	return false
}
