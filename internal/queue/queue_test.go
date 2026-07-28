package queue

import (
	"github.com/uhvesta/cmux-localreview/internal/store"
	"path/filepath"
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
