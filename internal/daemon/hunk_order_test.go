package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/hunkorder"
)

func TestHunkPlanIsExplicitImmutableAndStaleWithoutReprompt(t *testing.T) {
	d, review, repo := hunkPlanDaemon(t)
	var mu sync.Mutex
	calls := 0
	d.hunkPlanGenerator = func(_ context.Context, request hunkorder.Request) (hunkorder.Result, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		entries := make([]hunkorder.Entry, 0, len(request.Hunks))
		for index, hunk := range request.Hunks {
			entries = append(entries, hunkorder.Entry{HunkID: hunk.ID, Rank: len(request.Hunks) - index, Rationale: "Review this stable hunk."})
		}
		// Deliberately return reverse rank input: strict validation accepts it and
		// persistence normalizes it to actual plan order.
		return hunkorder.Result{Entries: entries, Questions: []hunkorder.Question{{ID: "q-1", Body: "What invariant changes?", HunkIDs: []string{request.Hunks[0].ID}}}}, nil
	}
	get := func() map[string]any {
		return hunkPlanRequestHTTP(t, d, review, repo, http.MethodGet, "?model=gpt-test&reasoningEffort=high", "")
	}
	firstRead := get()
	if firstRead["state"] != "empty" || hunkPlanCalls(&mu, &calls) != 0 {
		t.Fatalf("initial pure read=%#v calls=%d", firstRead, calls)
	}
	created := hunkPlanRequestHTTP(t, d, review, repo, http.MethodPost, "", `{"model":"gpt-test","reasoningEffort":"high"}`)
	if created["state"] != "ready" || hunkPlanCalls(&mu, &calls) != 1 {
		t.Fatalf("generated=%#v calls=%d", created, calls)
	}
	plan := created["plan"].(map[string]any)
	firstID := plan["id"].(string)
	if plan["request"].(map[string]any)["hunks"] == nil {
		t.Fatalf("plan omits immutable hunk input: %#v", plan)
	}
	readyRead := get()
	if readyRead["state"] != "ready" || hunkPlanCalls(&mu, &calls) != 1 {
		t.Fatalf("reopen unexpectedly generated: %#v calls=%d", readyRead, calls)
	}
	cached := hunkPlanRequestHTTP(t, d, review, repo, http.MethodPost, "", `{"model":"gpt-test","reasoningEffort":"high"}`)
	if cached["cached"] != true || cached["plan"].(map[string]any)["id"] != firstID || hunkPlanCalls(&mu, &calls) != 1 {
		t.Fatalf("implicit duplicate=%#v calls=%d", cached, calls)
	}
	if err := os.WriteFile(filepath.Join(repo.AbsolutePath, "review.txt"), []byte("one\ntwo changed again\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := get()
	if stale["state"] != "stale" || hunkPlanCalls(&mu, &calls) != 1 {
		t.Fatalf("changed diff should be stale not regenerated: %#v calls=%d", stale, calls)
	}
	refreshed := hunkPlanRequestHTTP(t, d, review, repo, http.MethodPost, "", `{"model":"gpt-test","reasoningEffort":"high","refresh":true}`)
	if refreshed["state"] != "ready" || refreshed["plan"].(map[string]any)["id"] == firstID || hunkPlanCalls(&mu, &calls) != 2 {
		t.Fatalf("explicit refresh must create a new immutable plan: %#v calls=%d", refreshed, calls)
	}
	refreshedID := refreshed["plan"].(map[string]any)["id"].(string)
	contextRead := hunkPlanRequestHTTP(t, d, review, repo, http.MethodGet, "/"+refreshedID+"/ask-context", "")
	if contextRead["askContext"] == "" || hunkPlanCalls(&mu, &calls) != 2 {
		t.Fatalf("canonical context read must not generate: %#v calls=%d", contextRead, calls)
	}
	compatibilityRead := hunkPlanRequestHTTP(t, d, review, repo, http.MethodGet, "/ask-context?planId="+refreshedID, "")
	if compatibilityRead["askContext"] == "" || hunkPlanCalls(&mu, &calls) != 2 {
		t.Fatalf("compatibility context read must not generate: %#v calls=%d", compatibilityRead, calls)
	}
}

func TestParseHunkPlanRejectsNonStructuredOrUnknownFields(t *testing.T) {
	if _, err := parseHunkPlan(`{"entries":[],"questions":[],"surprise":true}`); err == nil {
		t.Fatal("unknown output field was accepted")
	}
	if _, err := parseHunkPlan("this is not JSON"); err == nil {
		t.Fatal("non JSON output was accepted")
	}
	if _, err := parseHunkPlan(`{"entries":[],"questions":[]} {"entries":[],"questions":[]}`); err == nil {
		t.Fatal("a second JSON document was accepted")
	}
	if _, err := parseHunkPlan("```json\n{\"entries\":[],\"questions\":[]}\n```"); err == nil {
		t.Fatal("markdown-wrapped JSON was accepted despite the strict output contract")
	}
}

func hunkPlanCalls(mu *sync.Mutex, calls *int) int { mu.Lock(); defer mu.Unlock(); return *calls }

func hunkPlanDaemon(t *testing.T) (*Daemon, workspaceReview, reviewRepo) {
	t.Helper()
	d := askRouteDaemon(t)
	if _, err := d.db.Exec(`INSERT INTO sessions(id,label,started_at) VALUES(81,'hunk plan',1)`); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	git := func(arguments ...string) {
		command := exec.Command("git", append([]string{"-C", repoPath}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoPath, "review.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "review.txt")
	git("commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(repoPath, "review.txt"), []byte("one\ntwo changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	review := workspaceReview{Root: repoPath, SessionID: 81}
	repo := reviewRepo{ID: "repo-hunks", AbsolutePath: repoPath, WorkspaceRelativePath: "."}
	review.Repos = []reviewRepo{repo}
	d.review = &review
	return d, review, repo
}

func hunkPlanRequestHTTP(t *testing.T, d *Daemon, review workspaceReview, repo reviewRepo, method, suffix, body string) map[string]any {
	t.Helper()
	path := "/repos/" + repo.ID + "/api/hunk-review-plan"
	if suffix != "" {
		if suffix[0] == '?' {
			path += suffix
		} else {
			path += suffix
		}
	}
	req := httptest.NewRequest(method, "http://local.test"+path, bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	if !d.hunkPlanHandler(response, req, review, repo) {
		t.Fatalf("route %s was not handled", path)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
