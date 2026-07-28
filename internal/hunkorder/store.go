// Package hunkorder owns durable, explicitly generated Copilot review-order
// guidance. It deliberately has no SDK dependency: callers decide when to
// generate, while every read remains a pure SQLite lookup.
package hunkorder

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrNotFound = errors.New("hunk order guidance not found")

type Settings struct {
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ContextTier     string `json:"contextTier,omitempty"`
}

// Key is the complete immutable identity of one generated ordering. A review
// reload supplies the same key and only reads this record; a changed diff has
// a different fingerprint and therefore cannot accidentally reuse advice for
// other code.
type Key struct {
	ReviewSessionID int64
	RepoID          string
	DiffFingerprint string
	Model           string
	SettingsKey     string
}

type Hunk struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Header   string `json:"header"`
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	Patch    string `json:"patch"`
}

type Entry struct {
	HunkID    string `json:"hunkId"`
	Rank      int    `json:"rank"`
	Rationale string `json:"rationale"`
}

type Question struct {
	ID      string   `json:"id"`
	Body    string   `json:"body"`
	HunkIDs []string `json:"hunkIds,omitempty"`
}

type Result struct {
	Entries   []Entry    `json:"entries"`
	Questions []Question `json:"questions"`
}

type Record struct {
	ID string `json:"id"`
	Key
	PromptVersion string   `json:"promptVersion"`
	Request       *Request `json:"request,omitempty"`
	State         string   `json:"state"`
	Result        *Result  `json:"result,omitempty"`
	Error         string   `json:"error,omitempty"`
	GeneratedAt   int64    `json:"generatedAt"`
}

type Request struct {
	Key
	Hunks []Hunk `json:"hunks"`
}

type Generator interface {
	Generate(context.Context, Request) (Result, error)
}

func CanonicalSettingsKey(settings Settings) (string, error) {
	settings.ReasoningEffort = strings.TrimSpace(settings.ReasoningEffort)
	settings.ContextTier = strings.TrimSpace(settings.ContextTier)
	encoded, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func ValidateRequest(request Request) error {
	if request.ReviewSessionID < 1 || strings.TrimSpace(request.RepoID) == "" || strings.TrimSpace(request.DiffFingerprint) == "" || strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.SettingsKey) == "" {
		return errors.New("review session, repository, diff fingerprint, model, and settings are required")
	}
	if len(request.Hunks) == 0 {
		return errors.New("there are no changed hunks to prioritize")
	}
	seen := map[string]bool{}
	for _, hunk := range request.Hunks {
		if strings.TrimSpace(hunk.ID) == "" || strings.TrimSpace(hunk.Path) == "" || seen[hunk.ID] {
			return errors.New("each review hunk needs a unique ID and path")
		}
		seen[hunk.ID] = true
	}
	return nil
}

func ValidateResult(request Request, result Result) (Result, error) {
	if len(result.Entries) != len(request.Hunks) {
		return Result{}, fmt.Errorf("Copilot returned %d hunk ranks for %d review hunks", len(result.Entries), len(request.Hunks))
	}
	wanted := map[string]bool{}
	for _, hunk := range request.Hunks {
		wanted[hunk.ID] = true
	}
	seenRank, seenHunk := map[int]bool{}, map[string]bool{}
	for index := range result.Entries {
		entry := &result.Entries[index]
		if !wanted[entry.HunkID] || seenHunk[entry.HunkID] || entry.Rank < 1 || entry.Rank > len(request.Hunks) || seenRank[entry.Rank] {
			return Result{}, errors.New("Copilot returned an invalid hunk ordering; refresh explicitly to try again")
		}
		seenHunk[entry.HunkID], seenRank[entry.Rank] = true, true
		entry.Rationale = strings.TrimSpace(entry.Rationale)
		if entry.Rationale == "" {
			return Result{}, errors.New("Copilot returned a hunk without a rationale; refresh explicitly to try again")
		}
	}
	seenQuestion := map[string]bool{}
	for index := range result.Questions {
		question := &result.Questions[index]
		question.ID, question.Body = strings.TrimSpace(question.ID), strings.TrimSpace(question.Body)
		if question.ID == "" || question.Body == "" || seenQuestion[question.ID] {
			return Result{}, errors.New("Copilot returned an invalid review question; refresh explicitly to try again")
		}
		seenQuestion[question.ID] = true
		for _, hunkID := range question.HunkIDs {
			if !wanted[hunkID] {
				return Result{}, errors.New("Copilot linked a review question to an unknown hunk; refresh explicitly to try again")
			}
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Rank < result.Entries[j].Rank })
	return result, nil
}

// Get never generates work. It is safe to call while navigating/reopening a
// review and returns ErrNotFound when guidance has not been explicitly asked
// for yet.
func Get(ctx context.Context, db *sql.DB, key Key) (*Record, error) {
	var state, resultJSON, problem, requestJSON sql.NullString
	record := &Record{Key: key}
	err := db.QueryRowContext(ctx, `SELECT id,prompt_version,state,request_json,result_json,error,generated_at FROM hunk_review_plans WHERE review_session_id=? AND repo_id=? AND diff_fingerprint=? AND model=? AND settings_key=? ORDER BY generated_at DESC,id DESC LIMIT 1`, key.ReviewSessionID, key.RepoID, key.DiffFingerprint, key.Model, key.SettingsKey).Scan(&record.ID, &record.PromptVersion, &state, &requestJSON, &resultJSON, &problem, &record.GeneratedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	record.State, record.Error = state.String, problem.String
	if err := decodeRequest(requestJSON.String, record); err != nil {
		return nil, err
	}
	if resultJSON.Valid && strings.TrimSpace(resultJSON.String) != "" {
		result := &Result{}
		if err := json.Unmarshal([]byte(resultJSON.String), result); err != nil {
			return nil, fmt.Errorf("decode persisted hunk ordering: %w", err)
		}
		record.Result = result
	}
	return record, nil
}

// Latest returns a prior result for the selected model/settings. It exists so
// callers can display an actionable stale state when the diff fingerprint has
// changed, never so that old advice is silently applied to new code.
func Latest(ctx context.Context, db *sql.DB, sessionID int64, repoID, model, settingsKey string) (*Record, error) {
	row := db.QueryRowContext(ctx, `SELECT id,diff_fingerprint,prompt_version,state,request_json,result_json,error,generated_at FROM hunk_review_plans WHERE review_session_id=? AND repo_id=? AND model=? AND settings_key=? ORDER BY generated_at DESC,id DESC LIMIT 1`, sessionID, repoID, model, settingsKey)
	var fingerprint, state, resultJSON, problem, requestJSON sql.NullString
	record := &Record{Key: Key{ReviewSessionID: sessionID, RepoID: repoID, Model: model, SettingsKey: settingsKey}}
	if err := row.Scan(&record.ID, &fingerprint, &record.PromptVersion, &state, &requestJSON, &resultJSON, &problem, &record.GeneratedAt); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	record.DiffFingerprint, record.State, record.Error = fingerprint.String, state.String, problem.String
	if err := decodeRequest(requestJSON.String, record); err != nil {
		return nil, err
	}
	if resultJSON.Valid && strings.TrimSpace(resultJSON.String) != "" {
		result := &Result{}
		if err := json.Unmarshal([]byte(resultJSON.String), result); err != nil {
			return nil, fmt.Errorf("decode persisted hunk ordering: %w", err)
		}
		record.Result = result
	}
	return record, nil
}

// GetByID reads one immutable plan for a follow-up /ask envelope. It never
// runs Copilot and callers must still check its review/session ownership.
func GetByID(ctx context.Context, db *sql.DB, id string) (*Record, error) {
	var key Key
	var state, resultJSON, problem, requestJSON sql.NullString
	record := &Record{ID: id}
	err := db.QueryRowContext(ctx, `SELECT review_session_id,repo_id,diff_fingerprint,model,settings_key,prompt_version,state,request_json,result_json,error,generated_at FROM hunk_review_plans WHERE id=?`, id).Scan(&key.ReviewSessionID, &key.RepoID, &key.DiffFingerprint, &key.Model, &key.SettingsKey, &record.PromptVersion, &state, &requestJSON, &resultJSON, &problem, &record.GeneratedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	record.Key, record.State, record.Error = key, state.String, problem.String
	if err := decodeRequest(requestJSON.String, record); err != nil {
		return nil, err
	}
	if resultJSON.Valid && strings.TrimSpace(resultJSON.String) != "" {
		result := &Result{}
		if err := json.Unmarshal([]byte(resultJSON.String), result); err != nil {
			return nil, fmt.Errorf("decode persisted hunk ordering: %w", err)
		}
		record.Result = result
	}
	return record, nil
}

// Save stores a terminal outcome for an explicit generation request. Both an
// unavailable runtime and invalid output are durable, visible outcomes; they
// can be retried only by another explicit POST.
func Save(ctx context.Context, db *sql.DB, request Request, result *Result, problem error) (*Record, error) {
	if err := ValidateRequest(request); err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	record := &Record{ID: id, Key: request.Key, PromptVersion: "v1", Request: &request, GeneratedAt: time.Now().UnixMilli()}
	var resultJSON any
	if problem != nil {
		record.State, record.Error = "error", safeError(problem)
	} else if result == nil {
		record.State, record.Error = "error", "Copilot returned no structured review plan. Refresh explicitly to try again."
	} else {
		validated, err := ValidateResult(request, *result)
		if err != nil {
			record.State, record.Error = "error", safeError(err)
		} else {
			record.State, record.Result = "ready", &validated
			encoded, err := json.Marshal(validated)
			if err != nil {
				return nil, err
			}
			resultJSON = string(encoded)
		}
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO hunk_review_plans(id,review_session_id,repo_id,diff_fingerprint,model,settings_key,prompt_version,state,request_json,result_json,error,generated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, record.ID, request.ReviewSessionID, request.RepoID, request.DiffFingerprint, request.Model, request.SettingsKey, record.PromptVersion, record.State, string(requestJSON), resultJSON, nullable(record.Error), record.GeneratedAt)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func decodeRequest(raw string, record *Record) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("persisted hunk review plan is missing its immutable request")
	}
	request := &Request{}
	if err := json.Unmarshal([]byte(raw), request); err != nil {
		return fmt.Errorf("decode persisted hunk review plan request: %w", err)
	}
	record.Request = request
	return nil
}

func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func safeError(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "Copilot could not generate hunk-order guidance."
	}
	if len(message) > 500 {
		return message[:500]
	}
	return message
}
