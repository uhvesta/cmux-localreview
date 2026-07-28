package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/store"
)

func askRouteDaemon(t *testing.T) *Daemon {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Daemon{db: db}
}

func askRequest(t *testing.T, daemon *Daemon, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewBufferString(body))
	result := httptest.NewRecorder()
	if !daemon.handleAsk(result, req, path) {
		t.Fatalf("route %s was not handled", path)
	}
	return result
}

func TestAskMessageAndCancelAreDurableWithoutSDKNetwork(t *testing.T) {
	d := askRouteDaemon(t)
	conversation, err := ask.CreateConversation(context.Background(), d.db, ask.CreateConversationInput{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"body":"Explain this line","location":{"repoId":"repo-1","filePath":"cmd/main.go","workspacePath":"services/api/cmd/main.go","side":"current","startLine":9,"endLine":11,"selectedCode":"return err"}}`
	created := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/messages", body)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var payload struct {
		Delivery  string      `json:"delivery"`
		Assistant ask.Message `json:"assistant"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Delivery != "pending-runtime" || !payload.Assistant.Pending {
		t.Fatalf("payload=%#v", payload)
	}
	messages, err := ask.ListMessages(context.Background(), d.db, conversation.ID)
	if err != nil || len(messages) != 2 || messages[0].Location == nil || messages[0].Location.WorkspacePath != "services/api/cmd/main.go" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	cancelled := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/cancel", ``)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	messages, err = ask.ListMessages(context.Background(), d.db, conversation.ID)
	if err != nil || messages[1].Pending || messages[1].Body != "Response cancelled before it completed." {
		t.Fatalf("after cancel=%#v err=%v", messages, err)
	}
	// A second cancellation is a safe no-op, so reload/retry cannot cancel a
	// later prompt merely because the earlier request was repeated.
	again := askRequest(t, d, http.MethodPost, "/ask/conversations/"+conversation.ID+"/cancel", ``)
	if again.Code != http.StatusOK || !bytes.Contains(again.Body.Bytes(), []byte(`"cancelled":false`)) {
		t.Fatalf("repeat cancel=%s", again.Body.String())
	}
}

func TestInlineConversationPersistsInitialAnchor(t *testing.T) {
	d := askRouteDaemon(t)
	response := askRequest(t, d, http.MethodPost, "/ask/inline-conversations", `{"model":"gpt-5","context":{"repoId":"repo-1","filePath":"lib/check.go","workspacePath":"nested/lib/check.go","side":"current","startLine":4,"endLine":5,"selectedCode":"return valid"}}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Conversation ask.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Conversation.Context == nil || payload.Conversation.Context.WorkspacePath != "nested/lib/check.go" || payload.Conversation.Context.SelectedCode != "return valid" {
		t.Fatalf("conversation=%#v", payload.Conversation)
	}
}
