package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
)

// handleQuestionSetDelivery sends an explicitly selected saved set into one
// existing persistent /ask conversation. It is intentionally POST-only: a
// reload can inspect the resulting transcript but can never re-send a set.
func (d *Daemon) handleQuestionSetDelivery(w http.ResponseWriter, r *http.Request, questionSetID string) {
	var in struct {
		ConversationID string        `json:"conversationId"`
		Mode           string        `json:"mode"`
		Location       *ask.Location `json:"location"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		askError(w, errors.New("invalid question-set delivery"))
		return
	}
	if strings.TrimSpace(in.ConversationID) == "" {
		askError(w, errors.New("question-set delivery needs a conversationId"))
		return
	}
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "combined"
	}
	if mode != "combined" && mode != "sequential" {
		askError(w, errors.New("question-set mode must be combined or sequential"))
		return
	}
	ctx := r.Context()
	set, err := ask.GetQuestionSet(ctx, d.db, questionSetID)
	if err != nil {
		askError(w, err)
		return
	}
	conversation, err := ask.GetConversation(ctx, d.db, in.ConversationID)
	if err != nil {
		askError(w, err)
		return
	}
	if conversation.ArchivedAt != nil {
		askError(w, errors.New("resume the /ask conversation before sending a question set"))
		return
	}
	questions := nonEmptyQuestionBodies(set)
	if len(questions) == 0 {
		askError(w, errors.New("question set has no questions"))
		return
	}
	var user, assistant *ask.Message
	if mode == "combined" {
		user, assistant, err = d.insertAndStartAskTurn(ctx, conversation, combinedQuestionSetPrompt(questions), in.Location, nil)
	} else {
		user, assistant, err = d.startSequentialQuestionSet(ctx, conversation, questions, in.Location)
	}
	if err != nil {
		if errors.Is(err, askruntime.ErrTurnInProgress) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		askError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"mode": mode, "questionSet": set, "conversation": conversation,
		"user": user, "assistant": assistant, "delivery": "streaming",
		"questionsAccepted": len(questions), "remaining": map[bool]int{true: len(questions) - 1, false: 0}[mode == "sequential"],
	})
}

func nonEmptyQuestionBodies(set *ask.QuestionSet) []string {
	result := make([]string, 0, len(set.Questions))
	for _, question := range set.Questions {
		if body := strings.TrimSpace(question.Body); body != "" {
			result = append(result, body)
		}
	}
	return result
}

func combinedQuestionSetPrompt(questions []string) string {
	var body strings.Builder
	body.WriteString("Please answer these review questions in order. Keep each answer clearly numbered.\n\n")
	for index, question := range questions {
		if index > 0 {
			body.WriteByte('\n')
		}
		fmt.Fprintf(&body, "%d. %s", index+1, question)
	}
	return body.String()
}

func (d *Daemon) insertAndStartAskTurn(ctx context.Context, conversation *ask.Conversation, body string, location *ask.Location, afterTerminal func(askruntime.Delta)) (*ask.Message, *ask.Message, error) {
	user, err := ask.InsertMessage(ctx, d.db, conversation.ID, ask.RoleUser, body, false, location)
	if err != nil {
		return nil, nil, err
	}
	assistant, err := ask.InsertMessage(ctx, d.db, conversation.ID, ask.RoleAssistant, "", true, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := d.startAskTurnAfter(conversation, user, assistant, afterTerminal); err != nil {
		// Persist a terminal failure. It remains in the transcript, and no read
		// path ever retries or replays this explicit delivery attempt.
		d.persistAskEvent(conversation.ID, assistant.ID, askruntime.Delta{Event: askruntime.EventError, Error: err.Error()})
		return user, assistant, err
	}
	return user, assistant, nil
}

func (d *Daemon) startSequentialQuestionSet(ctx context.Context, conversation *ask.Conversation, questions []string, location *ask.Location) (*ask.Message, *ask.Message, error) {
	var send func(int) (*ask.Message, *ask.Message, error)
	send = func(index int) (*ask.Message, *ask.Message, error) {
		var after func(askruntime.Delta)
		if index+1 < len(questions) {
			after = func(event askruntime.Delta) {
				if event.Aborted {
					return
				}
				go func() {
					// Runtime clears its busy marker before publishing done. Yielding
					// also handles a synchronous fake SDK session deterministically.
					time.Sleep(time.Millisecond)
					_, _, _ = send(index + 1)
				}()
			}
		}
		return d.insertAndStartAskTurn(context.Background(), conversation, questions[index], location, after)
	}
	return send(0)
}
