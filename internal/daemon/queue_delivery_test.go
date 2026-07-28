package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	queueStore "github.com/uhvesta/cmux-localreview/internal/queue"
)

func TestQueueFeedbackDeliversOnceThroughDedicatedSDKConversation(t *testing.T) {
	d := askRouteDaemon(t)
	session := &askRouteSession{emit: true}
	d.askRuntime = askruntime.New(askRouteBackend{session: session})
	item, _, err := queueStore.Enqueue(d.db, queueStore.EnqueueInput{Title: "Parser", WorkspacePath: "/workspace", ReviewTopic: "parser"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queueStore.AddFeedback(d.db, item.ID, queueStore.FeedbackInput{Body: "Check this boundary", Path: "apps/parser/a.go", Line: intPointer(8)}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/queue/"+item.ID+"/deliver-feedback", bytes.NewBufferString(`{"policy":"queue"}`))
	result := httptest.NewRecorder()
	d.handleQueueFeedbackDelivery(result, request, item.ID)
	if result.Code != http.StatusAccepted || !strings.Contains(result.Body.String(), `"delivery":"copilot-sdk"`) {
		t.Fatalf("delivery=%d %s", result.Code, result.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		feedback, listErr := queueStore.FeedbackForItem(d.db, item.ID, false)
		if listErr == nil && len(feedback) == 1 && feedback[0].DeliveredAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("feedback did not become delivered: %#v err=%v", feedback, listErr)
		}
		time.Sleep(time.Millisecond)
	}
	conversation, err := ask.FindActiveConversationForQueueItem(context.Background(), d.db, item.ID)
	if err != nil || conversation == nil {
		t.Fatalf("conversation=%#v err=%v", conversation, err)
	}
	messages, err := ask.ListMessages(context.Background(), d.db, conversation.ID)
	if err != nil || len(messages) != 2 || messages[0].Role != ask.RoleUser || !strings.Contains(messages[0].Body, "apps/parser/a.go:8") {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	// Retrying after accepted delivery is intentionally a no-op. It must not
	// create another SDK prompt merely because a UI click was repeated.
	again := httptest.NewRecorder()
	d.handleQueueFeedbackDelivery(again, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`)), item.ID)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"alreadyDelivered":true`) {
		t.Fatalf("repeat=%d %s", again.Code, again.Body.String())
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.prompts) != 1 || !strings.Contains(session.prompts[0], "apps/parser/a.go:8") {
		encoded, _ := json.Marshal(session.prompts)
		t.Fatalf("SDK prompts=%s", encoded)
	}
}

func intPointer(value int) *int { return &value }
