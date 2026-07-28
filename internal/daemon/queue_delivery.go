package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
)

// handleQueueFeedbackDelivery sends only an explicit undelivered formal
// feedback batch to a queue-item-owned Copilot SDK conversation. It never
// relies on a terminal, ACP endpoint, or a focused cmux pane. A delivery is
// recorded only after the SDK admits the request ("delivered" means
// dispatched, not that Copilot's later answer succeeded), making double-clicks
// and retry races safe; an unavailable/busy SDK leaves feedback undelivered.
func (d *Daemon) handleQueueFeedbackDelivery(w http.ResponseWriter, r *http.Request, itemID string) {
	var in struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid feedback delivery"})
		return
	}
	policy := strings.TrimSpace(in.Policy)
	if policy == "" {
		policy = "queue"
	}
	if policy != "queue" && policy != "interrupt" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "delivery policy must be queue or interrupt"})
		return
	}

	d.queueDeliveryMu.Lock()
	if d.queueDeliveries == nil {
		d.queueDeliveries = make(map[string]struct{})
	}
	if _, busy := d.queueDeliveries[itemID]; busy {
		d.queueDeliveryMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "feedback delivery is already in progress for this queue item"})
		return
	}
	d.queueDeliveries[itemID] = struct{}{}
	d.queueDeliveryMu.Unlock()
	defer func() {
		d.queueDeliveryMu.Lock()
		delete(d.queueDeliveries, itemID)
		d.queueDeliveryMu.Unlock()
	}()

	item, err := queueStore.Get(d.db, itemID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if item == nil || item.RemovedAt != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Unknown or removed queue item"})
		return
	}
	feedback, err := queueStore.FeedbackForItem(d.db, itemID, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(feedback) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"delivered": 0, "alreadyDelivered": true, "delivery": "copilot-sdk"})
		return
	}

	ctx := r.Context()
	conversation, err := ask.FindActiveConversationForQueueItem(ctx, d.db, itemID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if conversation == nil {
		conversation, err = ask.CreateConversation(ctx, d.db, ask.CreateConversationInput{QueueItemID: itemID, ReviewSessionID: d.askSessionID()})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	prompt := queueStore.FeedbackPrompt(*item, feedback, dereference(item.DecisionBody))
	user, err := ask.InsertMessage(ctx, d.db, conversation.ID, ask.RoleUser, prompt, false, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	assistant, err := ask.InsertMessage(ctx, d.db, conversation.ID, ask.RoleAssistant, "", true, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := d.startCopilotTurn(conversation, user, assistant, prompt, policy == "interrupt"); err != nil {
		// No automatic retry: settle the visible attempted delivery and leave the
		// feedback records undelivered for an intentional later retry.
		d.persistAskEvent(conversation.ID, assistant.ID, askruntime.Delta{Event: askruntime.EventError, Error: err.Error()})
		status := http.StatusServiceUnavailable
		if errors.Is(err, askruntime.ErrTurnInProgress) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "delivery": "unavailable", "conversation": conversation})
		return
	}
	ids := make([]int64, 0, len(feedback))
	for _, entry := range feedback {
		ids = append(ids, entry.ID)
	}
	if err := queueStore.MarkFeedbackDelivered(d.db, itemID, ids); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"delivered": len(ids), "delivery": "copilot-sdk", "policy": policy, "conversation": conversation, "assistant": assistant})
}
