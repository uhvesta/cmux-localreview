package daemon

// This acceptance fixture uses the public native HTTP API rather than calling
// the hunk-plan helper directly. It is intentionally backed by a deterministic
// structured Copilot generator: it proves the user-visible lifecycle without
// requiring credentials or a live model in CI.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/uhvesta/cmux-localreview/internal/hunkorder"
)

func TestHunkReviewPlanHTTPAcceptanceLifecycle(t *testing.T) {
	d, _, repo := hunkPlanDaemon(t)
	var lock sync.Mutex
	generationCount := 0
	d.hunkPlanGenerator = func(_ context.Context, request hunkorder.Request) (hunkorder.Result, error) {
		lock.Lock()
		generationCount++
		lock.Unlock()
		entries := make([]hunkorder.Entry, len(request.Hunks))
		for index, hunk := range request.Hunks {
			entries[index] = hunkorder.Entry{HunkID: hunk.ID, Rank: len(request.Hunks) - index, Rationale: "Fixture prioritization for " + hunk.Path}
		}
		return hunkorder.Result{Entries: entries}, nil
	}
	server := httptest.NewServer(http.HandlerFunc(d.apiHandler))
	t.Cleanup(server.Close)
	endpoint := server.URL + "/api/repos/" + repo.ID + "/api/hunk-review-plan"
	read := func() map[string]any {
		return hunkPlanHTTP(t, http.MethodGet, endpoint+"?model=fixture-model&reasoningEffort=medium", nil)
	}
	post := func(refresh bool) map[string]any {
		body, _ := json.Marshal(map[string]any{"model": "fixture-model", "reasoningEffort": "medium", "refresh": refresh})
		return hunkPlanHTTP(t, http.MethodPost, endpoint, body)
	}
	calls := func() int { lock.Lock(); defer lock.Unlock(); return generationCount }

	// Opening/reopening the review only reads the record. It must not start a
	// hidden Copilot session or resend the immutable diff.
	if got := read(); got["state"] != "empty" || calls() != 0 {
		t.Fatalf("initial open=%#v calls=%d", got, calls())
	}
	if got := read(); got["state"] != "empty" || calls() != 0 {
		t.Fatalf("reopen=%#v calls=%d", got, calls())
	}

	created := post(false)
	if created["state"] != "ready" || created["cached"] != false || calls() != 1 {
		t.Fatalf("explicit generate=%#v calls=%d", created, calls())
	}
	plan := created["plan"].(map[string]any)
	planID := plan["id"].(string)
	result := plan["result"].(map[string]any)
	entries := result["entries"].([]any)
	if len(entries) == 0 || entries[0].(map[string]any)["rank"].(float64) != 1 {
		t.Fatalf("structured plan was not normalized for plan navigation: %#v", result)
	}
	// The canonical context endpoint drives a later explicit /ask follow-up; it
	// is itself a read and cannot secretly generate or replay a question.
	contextPayload := hunkPlanHTTP(t, http.MethodGet, endpoint+"/"+planID+"/ask-context", nil)
	if contextPayload["askContext"] == "" || calls() != 1 {
		t.Fatalf("plan context=%#v calls=%d", contextPayload, calls())
	}
	if got := read(); got["state"] != "ready" || calls() != 1 {
		t.Fatalf("post-generate reopen=%#v calls=%d", got, calls())
	}

	// A changed diff never applies/recomputes the old plan. The UI may show it
	// as stale, but only the explicit Recompute control may call Copilot again.
	if err := os.WriteFile(filepath.Join(repo.AbsolutePath, "review.txt"), []byte("one\ntwo changed for stale fixture\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["state"] != "stale" || got["stale"] != true || calls() != 1 {
		t.Fatalf("stale read=%#v calls=%d", got, calls())
	}
	refreshed := post(true)
	if refreshed["state"] != "ready" || refreshed["cached"] != false || calls() != 2 {
		t.Fatalf("explicit refresh=%#v calls=%d", refreshed, calls())
	}
	if refreshed["plan"].(map[string]any)["id"] == planID {
		t.Fatalf("refresh mutated immutable plan %q", planID)
	}
}

func hunkPlanHTTP(t *testing.T, method, target string, body []byte) map[string]any {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s %s=%d %s", method, target, response.StatusCode, payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
