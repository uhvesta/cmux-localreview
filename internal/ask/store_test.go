package ask

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "localreview.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestConversationMessagesPersistLocationAndStaySeparateFromFormalFeedback(t *testing.T) {
	db, ctx := openTestDB(t), context.Background()
	effort, tier := ReasoningHigh, ContextLong
	conversation, err := CreateConversation(ctx, db, CreateConversationInput{
		Model: "gpt-5", ReasoningEffort: &effort, ContextTier: &tier,
		Context: &Location{RepoID: "repo-1", FilePath: "src/main.go", WorkspacePath: "services/api/src/main.go", Side: "current", StartLine: 12, EndLine: 15, SelectedCode: "return err"},
	})
	if err != nil {
		t.Fatal(err)
	}
	question, err := InsertMessage(ctx, db, conversation.ID, RoleUser, "/ask explain this", false, &Location{RepoID: "repo-1", FilePath: "src/main.go", WorkspacePath: "services/api/src/main.go", Side: "current", StartLine: 12, EndLine: 15, SelectedCode: "return err"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InsertMessage(ctx, db, conversation.ID, RoleAssistant, "It propagates the failure.", false, nil); err != nil {
		t.Fatal(err)
	}
	messages, err := ListMessages(ctx, db, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID != question.ID || messages[0].Location == nil || messages[0].Location.WorkspacePath != "services/api/src/main.go" {
		t.Fatalf("messages = %#v", messages)
	}
	var formalComments, formalFeedback int
	if err := db.QueryRow(`SELECT count(*) FROM comments`).Scan(&formalComments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM queue_feedback`).Scan(&formalFeedback); err != nil {
		t.Fatal(err)
	}
	if formalComments != 0 || formalFeedback != 0 {
		t.Fatalf("/ask leaked into formal data: comments=%d feedback=%d", formalComments, formalFeedback)
	}
}

func TestArchivedConversationsCanResumeWithoutLosingHistory(t *testing.T) {
	db, ctx := openTestDB(t), context.Background()
	if _, err := db.Exec(`INSERT INTO sessions(label,started_at) VALUES('review',1)`); err != nil {
		t.Fatal(err)
	}
	sessionID := int64(1)
	first, err := CreateConversation(ctx, db, CreateConversationInput{ReviewSessionID: &sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InsertMessage(ctx, db, first.ID, RoleUser, "old context", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := ArchiveActive(ctx, db, sessionID); err != nil {
		t.Fatal(err)
	}
	second, err := CreateConversation(ctx, db, CreateConversationInput{ReviewSessionID: &sessionID})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(ctx, db, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ArchivedAt != nil {
		t.Fatalf("resumed conversation is still archived: %#v", resumed)
	}
	other, err := GetConversation(ctx, db, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.ArchivedAt == nil {
		t.Fatal("previous active conversation was not archived")
	}
	messages, err := ListMessages(ctx, db, first.ID)
	if err != nil || len(messages) != 1 || messages[0].Body != "old context" {
		t.Fatalf("history = %#v, err=%v", messages, err)
	}
}

func TestSettleInterruptedMessagesAndQuestionSetReplacement(t *testing.T) {
	db, ctx := openTestDB(t), context.Background()
	conversation, err := CreateConversation(ctx, db, CreateConversationInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InsertMessage(ctx, db, conversation.ID, RoleAssistant, "", true, nil); err != nil {
		t.Fatal(err)
	}
	settled, err := SettleInterruptedMessages(ctx, db)
	if err != nil || settled != 1 {
		t.Fatalf("settled=%d err=%v", settled, err)
	}
	messages, _ := ListMessages(ctx, db, conversation.ID)
	if messages[0].Pending || messages[0].Body != "Response interrupted before it completed." {
		t.Fatalf("message = %#v", messages[0])
	}
	set, err := CreateQuestionSet(ctx, db, "Initial pass", []string{"Architecture?", "Tests?"})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := ReplaceQuestionSet(ctx, db, set.ID, "Final pass", []string{"Security?", "Docs?", "Release?"})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ID != set.ID || replaced.Name != "Final pass" || len(replaced.Questions) != 3 || replaced.Questions[2].Position != 2 {
		t.Fatalf("set = %#v", replaced)
	}
	deleted, err := DeleteQuestionSet(ctx, db, set.ID)
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
}
