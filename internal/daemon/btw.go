package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uhvesta/cmux-localreview/internal/ask"
	"github.com/uhvesta/cmux-localreview/internal/askruntime"
)

// /btw is a deliberately separate, durable Copilot SDK conversation. Its
// contents cannot be included in formal review feedback or the shared /ask
// transcript. copilotSessionId is an opaque local SDK key, never ACP data.
type btwThread struct {
	ID               int64         `json:"id"`
	Transport        string        `json:"transport"`
	CopilotSessionID *string       `json:"copilotSessionId"`
	RepoID           *int64        `json:"repoId"`
	FilePath         *string       `json:"filePath"`
	StartLine        *int          `json:"startLine"`
	EndLine          *int          `json:"endLine"`
	TargetAgentID    *string       `json:"targetAgentId"`
	CreatedAt        int64         `json:"createdAt"`
	Questions        []btwQuestion `json:"questions"`
}
type btwQuestion struct {
	ID        int64      `json:"id"`
	Body      string     `json:"body"`
	CreatedAt int64      `json:"createdAt"`
	Answer    *btwAnswer `json:"answer"`
}
type btwAnswer struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Pending   bool   `json:"pending"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}
type btwAskRequest struct {
	ThreadID        *int64               `json:"threadId"`
	Transport       string               `json:"transport"`
	RepoID          string               `json:"repoId"`
	FilePath        string               `json:"filePath"`
	StartLine       int                  `json:"startLine"`
	EndLine         int                  `json:"endLine"`
	CodeContent     string               `json:"codeContent"`
	Question        string               `json:"question"`
	AgentID         string               `json:"agentId"`
	Model           string               `json:"model"`
	ReasoningEffort *ask.ReasoningEffort `json:"reasoningEffort"`
	ContextTier     *ask.ContextTier     `json:"contextTier"`
}

func btwError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (d *Daemon) activeBtwSessionID() (int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.review == nil {
		return 0, false
	}
	return d.review.SessionID, true
}
func optionalBtwString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	value := v.String
	return &value
}
func optionalBtwInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	value := v.Int64
	return &value
}
func optionalBtwInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	value := int(v.Int64)
	return &value
}

func listBtwThreads(ctx context.Context, db *sql.DB, sessionID int64) ([]btwThread, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,transport,copilot_session_id,repo_id,file_path,start_line,end_line,target_agent_id,created_at FROM btw_threads WHERE session_id=? ORDER BY created_at,id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []btwThread{}
	for rows.Next() {
		var item btwThread
		var session, file, agentID sql.NullString
		var repoID, start, end sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Transport, &session, &repoID, &file, &start, &end, &agentID, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.CopilotSessionID, item.RepoID, item.FilePath = optionalBtwString(session), optionalBtwInt64(repoID), optionalBtwString(file)
		item.StartLine, item.EndLine, item.TargetAgentID = optionalBtwInt(start), optionalBtwInt(end), optionalBtwString(agentID)
		questions, err := listBtwQuestions(ctx, db, item.ID)
		if err != nil {
			return nil, err
		}
		item.Questions = questions
		result = append(result, item)
	}
	return result, rows.Err()
}
func listBtwQuestions(ctx context.Context, db *sql.DB, threadID int64) ([]btwQuestion, error) {
	rows, err := db.QueryContext(ctx, `SELECT q.id,q.body,q.created_at,a.id,a.body,a.pending,a.created_at,a.updated_at FROM btw_questions q LEFT JOIN btw_answers a ON a.id=(SELECT id FROM btw_answers WHERE question_id=q.id ORDER BY id DESC LIMIT 1) WHERE q.thread_id=? ORDER BY q.created_at,q.id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []btwQuestion{}
	for rows.Next() {
		var question btwQuestion
		var answerID, pending, created, updated sql.NullInt64
		var answerBody sql.NullString
		if err := rows.Scan(&question.ID, &question.Body, &question.CreatedAt, &answerID, &answerBody, &pending, &created, &updated); err != nil {
			return nil, err
		}
		if answerID.Valid {
			question.Answer = &btwAnswer{ID: answerID.Int64, Body: answerBody.String, Pending: pending.Int64 != 0, CreatedAt: created.Int64, UpdatedAt: updated.Int64}
		}
		result = append(result, question)
	}
	return result, rows.Err()
}
func getBtwThread(ctx context.Context, db *sql.DB, id, sessionID int64) (*btwThread, error) {
	items, err := listBtwThreads(ctx, db, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].ID == id {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}
func (d *Daemon) broadcastBtwUpdate(threadID int64) {
	if d.ws != nil {
		d.ws.Broadcast(map[string]any{"type": "btw-update", "threadId": threadID})
	}
}

func (d *Daemon) persistBtwEvent(threadID, answerID int64, event askruntime.Delta) {
	ctx, now := context.Background(), time.Now().UnixMilli()
	switch event.Event {
	case askruntime.EventDelta, askruntime.EventReasoningDelta:
		_, _ = d.db.ExecContext(ctx, `UPDATE btw_answers SET body=body||?,updated_at=? WHERE id=? AND pending=1`, event.Text, now, answerID)
	case askruntime.EventDone:
		fallback := ""
		if event.Aborted {
			fallback = "Response cancelled before it completed."
		}
		_, _ = d.db.ExecContext(ctx, `UPDATE btw_answers SET body=CASE WHEN body='' THEN ? ELSE body END,pending=0,updated_at=? WHERE id=? AND pending=1`, fallback, now, answerID)
	case askruntime.EventError:
		message := "Copilot could not complete this response."
		if strings.TrimSpace(event.Error) != "" {
			message = "Copilot error: " + event.Error
		}
		_, _ = d.db.ExecContext(ctx, `UPDATE btw_answers SET body=CASE WHEN body='' THEN ? ELSE body END,pending=0,updated_at=? WHERE id=? AND pending=1`, message, now, answerID)
	default:
		return
	}
	d.broadcastBtwUpdate(threadID)
}

func (d *Daemon) startBtwTurn(threadID, answerID int64, input btwAskRequest, repo *reviewRepo) error {
	workingDir, err := d.askWorkingDirectory()
	if err != nil {
		return err
	}
	runtime, err := d.askRuntimeForTurn(context.Background(), workingDir)
	if err != nil {
		return err
	}
	key := "btw:" + strconv.FormatInt(threadID, 10)
	location := &ask.Location{FilePath: input.FilePath, StartLine: input.StartLine, EndLine: input.EndLine, SelectedCode: input.CodeContent}
	if repo != nil {
		location.RepoID = repo.ID
		location.WorkspacePath = workspaceBtwPath(*repo, input.FilePath)
	}
	conversation := &ask.Conversation{ID: key, Model: optionalBtwModel(input.Model), ReasoningEffort: input.ReasoningEffort, ContextTier: input.ContextTier}
	prompt := "You are answering a local code-review /btw aside. Answer directly; do not create, export, or submit formal review feedback.\n" + askPrompt(input.Question, location, nil)
	go func() {
		_, sendErr := runtime.Send(context.Background(), key, askSessionConfig(conversation, workingDir), prompt, func(event askruntime.Delta) { d.persistBtwEvent(threadID, answerID, event) })
		if sendErr != nil {
			d.persistBtwEvent(threadID, answerID, askruntime.Delta{Event: askruntime.EventError, Error: sendErr.Error()})
		}
	}()
	return nil
}
func optionalBtwModel(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
func workspaceBtwPath(repo reviewRepo, file string) string {
	if repo.WorkspaceRelativePath == "" || repo.WorkspaceRelativePath == "." {
		return file
	}
	if file == "" {
		return repo.WorkspaceRelativePath
	}
	return strings.TrimSuffix(repo.WorkspaceRelativePath, "/") + "/" + strings.TrimPrefix(file, "/")
}
func positiveOrNil(value int) any {
	if value < 1 {
		return nil
	}
	return value
}

// handleBtw is SDK-native only.  The retired ACP and terminal transports are
// explicit conflicts instead of a dangerous focused-terminal fallback.
func (d *Daemon) handleBtw(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/btw/threads" && path != "/btw/ask" {
		return false
	}
	sessionID, active := d.activeBtwSessionID()
	if !active {
		btwError(w, http.StatusConflict, "Open a review workspace before using /btw; no focused-terminal fallback is available.")
		return true
	}
	if path == "/btw/threads" && r.Method == http.MethodGet {
		if raw := r.URL.Query().Get("sessionId"); raw != "" {
			requested, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || requested < 1 {
				btwError(w, http.StatusBadRequest, "sessionId must be a positive integer")
				return true
			}
			sessionID = requested
		}
		items, err := listBtwThreads(r.Context(), d.db, sessionID)
		if err != nil {
			btwError(w, http.StatusInternalServerError, "Could not load /btw threads")
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"threads": items})
		return true
	}
	if path != "/btw/ask" || r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	var input btwAskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		btwError(w, http.StatusBadRequest, "invalid /btw request")
		return true
	}
	if strings.TrimSpace(input.Question) == "" {
		btwError(w, http.StatusBadRequest, "question is required")
		return true
	}
	if input.Transport == "terminal" {
		btwError(w, http.StatusConflict, "Terminal /btw delivery was removed in the native daemon. Select the SDK-native Copilot target; no focused-terminal fallback is available.")
		return true
	}
	if input.Transport == "acp" {
		btwError(w, http.StatusConflict, "ACP /btw delivery was removed. Use the SDK-native Copilot target instead.")
		return true
	}
	if input.Transport != "" && input.Transport != "copilot" {
		btwError(w, http.StatusBadRequest, "transport must be copilot")
		return true
	}
	var repo *reviewRepo
	var repoDBID any
	if input.RepoID != "" {
		candidate, ok := d.reviewRepo(input.RepoID)
		if !ok {
			btwError(w, http.StatusNotFound, fmt.Sprintf("Unknown repoId: %s", input.RepoID))
			return true
		}
		repo, repoDBID = &candidate, candidate.DBID
	}
	threadID := int64(0)
	if input.ThreadID != nil {
		threadID = *input.ThreadID
		thread, err := getBtwThread(r.Context(), d.db, threadID, sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			btwError(w, http.StatusNotFound, "Unknown /btw thread")
			return true
		}
		if err != nil {
			btwError(w, http.StatusInternalServerError, "Could not load /btw thread")
			return true
		}
		if thread.Transport != "copilot" {
			btwError(w, http.StatusConflict, "This historic /btw thread is not a native Copilot thread and cannot receive a new prompt.")
			return true
		}
	} else {
		now := time.Now().UnixMilli()
		result, err := d.db.ExecContext(r.Context(), `INSERT INTO btw_threads(session_id,transport,repo_id,file_path,start_line,end_line,created_at) VALUES(?,?,?,?,?,?,?)`, sessionID, "copilot", repoDBID, nullable(input.FilePath), positiveOrNil(input.StartLine), positiveOrNil(input.EndLine), now)
		if err != nil {
			btwError(w, http.StatusInternalServerError, "Could not create /btw thread")
			return true
		}
		threadID, _ = result.LastInsertId()
		_, _ = d.db.ExecContext(r.Context(), `UPDATE btw_threads SET copilot_session_id=? WHERE id=?`, "btw:"+strconv.FormatInt(threadID, 10), threadID)
	}
	question, err := d.db.ExecContext(r.Context(), `INSERT INTO btw_questions(thread_id,body,created_at) VALUES(?,?,?)`, threadID, strings.TrimSpace(input.Question), time.Now().UnixMilli())
	if err != nil {
		btwError(w, http.StatusInternalServerError, "Could not save /btw question")
		return true
	}
	questionID, _ := question.LastInsertId()
	now := time.Now().UnixMilli()
	answer, err := d.db.ExecContext(r.Context(), `INSERT INTO btw_answers(question_id,body,pending,created_at,updated_at) VALUES(?,?,1,?,?)`, questionID, "", now, now)
	if err != nil {
		btwError(w, http.StatusInternalServerError, "Could not prepare Copilot response")
		return true
	}
	answerID, _ := answer.LastInsertId()
	if err := d.startBtwTurn(threadID, answerID, input, repo); err != nil {
		d.persistBtwEvent(threadID, answerID, askruntime.Delta{Event: askruntime.EventError, Error: err.Error()})
	}
	d.broadcastBtwUpdate(threadID)
	thread, err := getBtwThread(r.Context(), d.db, threadID, sessionID)
	if err != nil {
		btwError(w, http.StatusInternalServerError, "Could not read /btw thread")
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"thread": thread, "delivery": "streaming", "target": "copilot-sdk"})
	return true
}
