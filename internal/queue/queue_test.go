package queue

import (
	"encoding/json"
	"github.com/uhvesta/cmux-localreview/internal/store"
	"path/filepath"
	"strings"
	"testing"
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
	if _, err = Decide(db, b.ID, Approved, ""); err != nil {
		t.Fatal(err)
	}
	active, err := List(db, false)
	if err != nil || len(active) != 1 || active[0].ID != a.ID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func intPointer(value int) *int { return &value }
