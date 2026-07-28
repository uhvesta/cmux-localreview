package queue

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/store"
)

func TestQueueKeepsActiveReviewStreamUniqueAndRetainsRemoval(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, created, err := Enqueue(db, EnqueueInput{Title: "Parser", WorkspacePath: "/work/parser", ReviewTopic: "parser"})
	if err != nil || !created {
		t.Fatalf("enqueue=%v %v", created, err)
	}
	again, created, err := Enqueue(db, EnqueueInput{Title: "Changed title", WorkspacePath: "/work/parser", ReviewTopic: "parser"})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("idempotency=%#v %v", again, err)
	}
	if _, err = Remove(db, first.ID, "duplicate"); err != nil {
		t.Fatal(err)
	}
	second, created, err := Enqueue(db, EnqueueInput{Title: "Parser", WorkspacePath: "/work/parser", ReviewTopic: "parser"})
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("resubmit=%#v %v", second, err)
	}
}

func TestQueueIdentityNormalizesPRURLsAndTerminalResubmitStartsNewRound(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, created, err := Enqueue(db, EnqueueInput{Title: "PR 2", WorkspacePath: "/cache/pr", Kind: "remote", RemoteURL: "https://GitHub.com/Uhvesta/Mono/pull/2/?tab=files", SourceFingerprint: "head-a", IdempotentKey: "submit-2"})
	if err != nil || !created || first.IdentityKey == nil || *first.IdentityKey != "pr:github.com/uhvesta/mono/pull/2" {
		t.Fatalf("first=%#v created=%v err=%v", first, created, err)
	}
	again, created, err := Enqueue(db, EnqueueInput{Title: "PR 2 revised label", WorkspacePath: "/cache/pr", Kind: "remote", RemoteURL: "https://github.com/uhvesta/mono/pull/2", SourceFingerprint: "head-a"})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("duplicate=%#v created=%v err=%v", again, created, err)
	}
	if _, err = Complete(db, first.ID, Approved, "done"); err != nil {
		t.Fatal(err)
	}
	second, created, err := Enqueue(db, EnqueueInput{Title: "PR 2 new round", WorkspacePath: "/cache/pr", Kind: "remote", RemoteURL: "https://github.com/uhvesta/mono/pull/2", SourceFingerprint: "head-a", IdempotentKey: "submit-2"})
	if err != nil || !created || second.ID == first.ID || second.SupersedesID == nil || *second.SupersedesID != first.ID {
		t.Fatalf("resubmit=%#v created=%v err=%v", second, created, err)
	}
	active, err := List(db, false)
	if err != nil || len(active) != 1 || active[0].ID != second.ID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func TestDeletedItemCannotReopenRequeueReorderOrAcceptFeedback(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	item, _, err := Enqueue(db, EnqueueInput{Title: "skip", WorkspacePath: "/work", ReviewTopic: "skip"})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := Remove(db, item.ID, "not needed")
	if err != nil || removed == nil || removed.RemovedAt == nil {
		t.Fatalf("remove=%#v err=%v", removed, err)
	}
	for _, action := range []func(*sql.DB, string) (*Item, error){Open, Requeue, func(db *sql.DB, id string) (*Item, error) { return Reorder(db, id, 1) }} {
		result, err := action(db, item.ID)
		if err != nil || result != nil {
			t.Fatalf("deleted lifecycle action result=%#v err=%v", result, err)
		}
	}
	if _, err := AddFeedback(db, item.ID, FeedbackInput{Body: "should not deliver"}); err == nil {
		t.Fatal("deleted item accepted feedback")
	}
	if again, err := Remove(db, item.ID, "duplicate"); err != nil || again != nil {
		t.Fatalf("duplicate delete=%#v err=%v", again, err)
	}
	decisions, err := DecisionsForItem(db, item.ID)
	if err != nil || len(decisions) != 1 || decisions[0].Status != string(Completed) {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	history, err := List(db, true)
	if err != nil || len(history) != 1 || history[0].ID != item.ID || history[0].RemovedAt == nil {
		t.Fatalf("removed history=%#v err=%v", history, err)
	}
	active, err := List(db, false)
	if err != nil || len(active) != 0 {
		t.Fatalf("removed active=%#v err=%v", active, err)
	}
}

func TestQueueDetailLifecycleFeedbackAndReproductionFields(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	item, created, err := Enqueue(db, EnqueueInput{
		Title: "Parser", WorkspacePath: "/work/parser", ReviewTopic: "parser", SourceFingerprint: "one",
		SnapshotManifestPath: "/snapshots/parser.json", SnapshotManifest: json.RawMessage(`{"id":"snapshot-1","repos":[{}]}`),
		ACPHost: "127.0.0.1", ACPPort: 4123, ACPSessionID: "session-1", CopilotSessionID: "copilot-1",
	})
	if err != nil || !created {
		t.Fatalf("enqueue=%v created=%v", err, created)
	}
	if item.ACPState != "idle" || string(item.SnapshotManifest) != `{"id":"snapshot-1","repos":[{}]}` {
		t.Fatalf("metadata=%#v", item)
	}
	second, created, err := Enqueue(db, EnqueueInput{Title: "Parser next", WorkspacePath: "/work/parser", ReviewTopic: "parser", SourceFingerprint: "two"})
	if err != nil || !created || second.SupersedesID == nil || *second.SupersedesID != item.ID {
		t.Fatalf("supersede=%#v created=%v err=%v", second, created, err)
	}
	old, err := Get(db, item.ID)
	if err != nil || old.Status != Completed {
		t.Fatalf("old stream must be completed: %#v %v", old, err)
	}
	if _, err = AddFeedback(db, second.ID, FeedbackInput{Body: "Handle edge case.", Path: "packages/parser/a.go", Line: intPointer(8)}); err != nil {
		t.Fatal(err)
	}
	feedback, err := FeedbackForItem(db, second.ID, true)
	if err != nil || len(feedback) != 1 {
		t.Fatalf("feedback=%#v err=%v", feedback, err)
	}
	prompt := FeedbackPrompt(*second, feedback, "Please revise")
	if want := "packages/parser/a.go:8: Handle edge case."; !strings.Contains(prompt, want) {
		t.Fatalf("prompt %q missing %q", prompt, want)
	}
	absPath := filepath.Join(second.WorkspacePath, "apps", "parser", "b.go")
	if got := FeedbackPrompt(*second, []Feedback{{Body: "absolute", Path: &absPath, Line: intPointer(3)}}, ""); !strings.Contains(got, "apps/parser/b.go:3: absolute") || strings.Contains(got, second.WorkspacePath+"/apps") {
		t.Fatalf("workspace-relative prompt=%q", got)
	}
	if err = MarkFeedbackDelivered(db, second.ID, []int64{feedback[0].ID}); err != nil {
		t.Fatal(err)
	}
	undelivered, err := FeedbackForItem(db, second.ID, true)
	if err != nil || len(undelivered) != 0 {
		t.Fatalf("delivery=%#v err=%v", undelivered, err)
	}
	if _, err = Decide(db, second.ID, ChangesRequested, "Please revise"); err != nil {
		t.Fatal(err)
	}
	decisions, err := DecisionsForItem(db, second.ID)
	if err != nil || len(decisions) != 1 || decisions[0].Status != string(ChangesRequested) {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	if _, err = Requeue(db, second.ID); err != nil {
		t.Fatal(err)
	}
	decisions, _ = DecisionsForItem(db, second.ID)
	if len(decisions) != 2 || decisions[1].Status != "requeued" {
		t.Fatalf("requeue history=%#v", decisions)
	}
}

func TestQueueOpenNextAndReorderOnlyActionableItems(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, _, err := Enqueue(db, EnqueueInput{Title: "A", WorkspacePath: "/a"})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Enqueue(db, EnqueueInput{Title: "B", WorkspacePath: "/b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Reorder(db, b.ID, 1); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenNext(db)
	if err != nil || opened.ID != b.ID || opened.Status != InReview {
		t.Fatalf("open next=%#v err=%v", opened, err)
	}
	reopened, err := Open(db, b.ID)
	if err != nil || reopened.ID != b.ID || reopened.Status != InReview {
		t.Fatalf("idempotent open=%#v err=%v", reopened, err)
	}
	decisions, err := DecisionsForItem(db, b.ID)
	if err != nil || len(decisions) != 0 {
		t.Fatalf("idempotent open must not add history: %#v err=%v", decisions, err)
	}
	if _, err = Decide(db, b.ID, Approved, ""); err != nil {
		t.Fatal(err)
	}
	active, err := List(db, false)
	if err != nil || len(active) != 1 || active[0].ID != a.ID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func intPointer(value int) *int { return &value }
