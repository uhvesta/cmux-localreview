package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/ask"
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
	if path == "/ask/conversations" && r.Method == http.MethodGet {
		items, err := ask.ListConversations(ctx, d.db, d.askSessionID(), r.URL.Query().Get("includeArchived") == "true")
		if err != nil {
			askError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversations": items})
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
			item, err := ask.UpdateConversation(ctx, d.db, id, ask.UpdateConversationInput{Model: in.Model, ReasoningEffort: in.ReasoningEffort, ContextTier: in.ContextTier})
			if err != nil {
				askError(w, err)
			} else {
				writeJSON(w, http.StatusOK, map[string]any{"conversation": item})
			}
			return true
		}
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
