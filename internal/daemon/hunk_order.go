package daemon

// This file implements the deliberately explicit Copilot-guided hunk review
// plan.  It is intentionally separate from the normal /ask transcript: a
// plan is immutable review metadata, while an /ask conversation is an
// interactive, long-lived chat.  In particular, every GET below is a SQLite
// read plus a local Git diff parse -- it never opens an SDK session or sends a
// prompt.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	copilotsdk "github.com/github/copilot-sdk/go"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
	"github.com/uhvesta/cmux-localreview/internal/copilot"
	"github.com/uhvesta/cmux-localreview/internal/gitdiff"
	"github.com/uhvesta/cmux-localreview/internal/hunkorder"
)

// HunkPlanGenerator is a narrow test/embedding seam. Production leaves it nil
// and uses the daemon-owned official Copilot SDK runtime below. A read route
// never calls either path.
type HunkPlanGenerator func(context.Context, hunkorder.Request) (hunkorder.Result, error)

type hunkPlanInput struct {
	Model            string `json:"model"`
	ReasoningEffort  string `json:"reasoningEffort,omitempty"`
	ContextTier      string `json:"contextTier,omitempty"`
	Base             string `json:"base,omitempty"`
	Target           string `json:"target,omitempty"`
	IgnoreWhitespace bool   `json:"ignoreWhitespace,omitempty"`
	Refresh          bool   `json:"refresh,omitempty"`
}

func hunkPlanInputFromRequest(r *http.Request) hunkPlanInput {
	query := r.URL.Query()
	return hunkPlanInput{Model: query.Get("model"), ReasoningEffort: query.Get("reasoningEffort"), ContextTier: query.Get("contextTier"), Base: query.Get("base"), Target: query.Get("target"), IgnoreWhitespace: query.Get("ignoreWhitespace") == "true"}
}

func hunkPlanSelection(input hunkPlanInput) gitdiff.Selection {
	return gitdiff.Selection{BaseCommitish: input.Base, TargetCommitish: input.Target, IgnoreWhitespace: input.IgnoreWhitespace}
}

func hunkPlanRequest(review workspaceReview, repo reviewRepo, input hunkPlanInput) (hunkorder.Request, error) {
	settings := hunkorder.Settings{ReasoningEffort: strings.TrimSpace(input.ReasoningEffort), ContextTier: strings.TrimSpace(input.ContextTier)}
	settingsKey, err := hunkorder.CanonicalSettingsKey(settings)
	if err != nil {
		return hunkorder.Request{}, err
	}
	diff, err := gitdiff.Parse(repo.AbsolutePath, hunkPlanSelection(input))
	if err != nil {
		return hunkorder.Request{}, err
	}
	request := hunkorder.Request{Key: hunkorder.Key{ReviewSessionID: review.SessionID, RepoID: repo.ID, Model: strings.TrimSpace(input.Model), SettingsKey: settingsKey}}
	for _, file := range diff.Files {
		for _, chunk := range file.Chunks {
			patch := hunkPatch(chunk)
			seed, _ := json.Marshal(struct {
				Path                                   string
				Header                                 string
				OldStart, OldLines, NewStart, NewLines int
				Patch                                  string
			}{file.Path, chunk.Header, chunk.OldStart, chunk.OldLines, chunk.NewStart, chunk.NewLines, patch})
			digest := sha256.Sum256(seed)
			request.Hunks = append(request.Hunks, hunkorder.Hunk{ID: "h-" + hex.EncodeToString(digest[:8]), Path: file.Path, Header: chunk.Header, OldStart: chunk.OldStart, OldLines: chunk.OldLines, NewStart: chunk.NewStart, NewLines: chunk.NewLines, Patch: patch})
		}
	}
	fingerprintSource, _ := json.Marshal(struct {
		Selection gitdiff.Selection
		Hunks     []hunkorder.Hunk
	}{hunkPlanSelection(input), request.Hunks})
	fingerprint := sha256.Sum256(fingerprintSource)
	request.DiffFingerprint = hex.EncodeToString(fingerprint[:])
	if err := hunkorder.ValidateRequest(request); err != nil {
		return hunkorder.Request{}, err
	}
	return request, nil
}

func hunkPatch(chunk gitdiff.Chunk) string {
	var out strings.Builder
	out.WriteString(chunk.Header)
	out.WriteByte('\n')
	for _, line := range chunk.Lines {
		prefix := " "
		if line.Type == "add" {
			prefix = "+"
		}
		if line.Type == "delete" {
			prefix = "-"
		}
		out.WriteString(prefix)
		out.WriteString(line.Content)
		out.WriteByte('\n')
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func (d *Daemon) hunkPlanHandler(w http.ResponseWriter, r *http.Request, review workspaceReview, repo reviewRepo) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// GET /.../hunk-review-plan/{planID}/ask-context is only a persisted-plan
	// lookup. Do not overload normal plan reads with hidden /ask activity.
	if len(parts) == 5 && parts[3] == "hunk-review-plan" && parts[4] == "ask-context" && r.Method == http.MethodGet {
		plan, err := hunkorder.GetByID(r.Context(), d.db, r.URL.Query().Get("planId"))
		if errors.Is(err, hunkorder.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Review plan not found"})
			return true
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return true
		}
		if plan.ReviewSessionID != review.SessionID || plan.RepoID != repo.ID {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Review plan not found for this repository"})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "askContext": hunkPlanAskContext(plan)})
		return true
	}
	if len(parts) != 4 || parts[3] != "hunk-review-plan" {
		return false
	}
	switch r.Method {
	case http.MethodGet:
		input := hunkPlanInputFromRequest(r)
		if strings.TrimSpace(input.Model) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Choose a Copilot model before loading a review plan"})
			return true
		}
		request, err := hunkPlanRequest(review, repo, input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return true
		}
		plan, err := hunkorder.Get(r.Context(), d.db, request.Key)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"state": plan.State, "currentFingerprint": request.DiffFingerprint, "plan": plan})
			return true
		}
		if !errors.Is(err, hunkorder.ErrNotFound) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return true
		}
		latest, latestErr := hunkorder.Latest(r.Context(), d.db, review.SessionID, repo.ID, request.Model, request.SettingsKey)
		if latestErr == nil && latest.DiffFingerprint != request.DiffFingerprint {
			writeJSON(w, http.StatusOK, map[string]any{"state": "stale", "stale": true, "currentFingerprint": request.DiffFingerprint, "stalePlan": latest})
			return true
		}
		if latestErr != nil && !errors.Is(latestErr, hunkorder.ErrNotFound) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": latestErr.Error()})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": "empty", "currentFingerprint": request.DiffFingerprint})
		return true
	case http.MethodPost:
		var input hunkPlanInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid review-plan request"})
			return true
		}
		request, err := hunkPlanRequest(review, repo, input)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return true
		}
		if !input.Refresh {
			if existing, err := hunkorder.Get(r.Context(), d.db, request.Key); err == nil {
				writeJSON(w, http.StatusOK, map[string]any{"state": existing.State, "cached": true, "currentFingerprint": request.DiffFingerprint, "plan": existing})
				return true
			} else if !errors.Is(err, hunkorder.ErrNotFound) {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return true
			}
		}
		result, generationErr := d.generateHunkPlan(r.Context(), review.Root, request)
		plan, saveErr := hunkorder.Save(r.Context(), d.db, request, &result, generationErr)
		if saveErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": saveErr.Error()})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": plan.State, "cached": false, "currentFingerprint": request.DiffFingerprint, "plan": plan})
		return true
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Use GET to read or POST to explicitly generate a review plan"})
		return true
	}
}

func (d *Daemon) generateHunkPlan(ctx context.Context, workingDirectory string, request hunkorder.Request) (hunkorder.Result, error) {
	d.hunkPlanMu.Lock()
	defer d.hunkPlanMu.Unlock()
	if d.hunkPlanGenerator != nil {
		return d.hunkPlanGenerator(ctx, request)
	}
	runtime, err := d.askRuntimeForTurn(ctx, workingDirectory)
	if err != nil {
		return hunkorder.Result{}, err
	}
	conversationID := "localreview-hunk-plan-" + request.DiffFingerprint[:16] + "-" + shortPlanHash(request.Model+request.SettingsKey)
	if err := runtime.ResetSession(conversationID); err != nil {
		return hunkorder.Result{}, fmt.Errorf("review plan is already generating: %w", err)
	}
	settings := hunkorder.Settings{}
	_ = json.Unmarshal([]byte(request.SettingsKey), &settings)
	tier := copilotsdk.ContextTierDefault
	if settings.ContextTier == "long" {
		tier = copilotsdk.ContextTierLongContext
	}
	turnCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	output := strings.Builder{}
	done := make(chan error, 1)
	_, err = runtime.Send(turnCtx, conversationID, copilot.SessionConfig{ID: conversationID, Model: request.Model, ReasoningEffort: settings.ReasoningEffort, ContextTier: tier, WorkingDirectory: workingDirectory, Streaming: true}, hunkPlanPrompt(request), func(event askruntime.Delta) {
		switch event.Event {
		case askruntime.EventDelta:
			output.WriteString(event.Text)
		case askruntime.EventError:
			select {
			case done <- errors.New(event.Error):
			default:
			}
		case askruntime.EventDone:
			if event.Aborted {
				select {
				case done <- errors.New("Copilot cancelled the review-plan generation"):
				default:
				}
			} else {
				select {
				case done <- nil:
				default:
				}
			}
		}
	})
	if err != nil {
		return hunkorder.Result{}, err
	}
	select {
	case err := <-done:
		if err != nil {
			return hunkorder.Result{}, err
		}
		return parseHunkPlan(output.String())
	case <-turnCtx.Done():
		_, _ = runtime.Cancel(context.Background(), conversationID)
		return hunkorder.Result{}, errors.New("Copilot review-plan generation timed out; refresh explicitly to retry")
	}
}

func shortPlanHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func hunkPlanPrompt(request hunkorder.Request) string {
	var out strings.Builder
	out.WriteString("Prepare a read-only local code-review plan. Return ONLY one JSON object; no Markdown or prose outside JSON. Schema: {\\\"entries\\\":[{\\\"hunkId\\\":string,\\\"rank\\\":number,\\\"rationale\\\":string}],\\\"questions\\\":[{\\\"id\\\":string,\\\"body\\\":string,\\\"hunkIds\\\":[string]}]}. Include every hunk ID exactly once, with ranks 1..N. Rationale must be concrete. Questions are optional and must reference only supplied hunk IDs. Do not make changes or produce formal review comments.\n")
	fmt.Fprintf(&out, "Immutable diff fingerprint: %s\n", request.DiffFingerprint)
	for _, hunk := range request.Hunks {
		fmt.Fprintf(&out, "\nHUNK %s\nPATH %s\n%s\n", hunk.ID, hunk.Path, hunk.Patch)
	}
	return out.String()
}

func parseHunkPlan(raw string) (hunkorder.Result, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "```json"))
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "```"))
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "```"))
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result hunkorder.Result
	if err := decoder.Decode(&result); err != nil {
		return hunkorder.Result{}, fmt.Errorf("Copilot did not return a valid structured review plan: %w", err)
	}
	if decoder.More() {
		return hunkorder.Result{}, errors.New("Copilot returned trailing data after the structured review plan")
	}
	return result, nil
}

func hunkPlanAskContext(plan *hunkorder.Record) string {
	if plan == nil || plan.Request == nil {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "You are following up on immutable Copilot review plan %s for diff %s. This remains /ask discussion, not formal review feedback.\n", plan.ID, plan.DiffFingerprint)
	if plan.Result != nil {
		for _, entry := range plan.Result.Entries {
			fmt.Fprintf(&out, "Priority %d: %s — %s\n", entry.Rank, entry.HunkID, entry.Rationale)
		}
	}
	for _, hunk := range plan.Request.Hunks {
		fmt.Fprintf(&out, "\nHUNK %s (%s)\n%s\n", hunk.ID, hunk.Path, hunk.Patch)
	}
	return out.String()
}
