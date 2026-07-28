// Package ask owns durable /ask conversations and question sets.
//
// Its only tables are ask_conversations, ask_messages, question_sets, and
// questions. Formal comments, queue feedback, exports, and GitHub publishing
// intentionally cannot obtain /ask data from this package.
package ask

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("ask record not found")

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

type ReasoningEffort string

const (
	ReasoningLow    ReasoningEffort = "low"
	ReasoningMedium ReasoningEffort = "medium"
	ReasoningHigh   ReasoningEffort = "high"
	ReasoningXHigh  ReasoningEffort = "xhigh"
)

type ContextTier string

const (
	ContextDefault ContextTier = "default"
	ContextLong    ContextTier = "long_context"
)

// Location is persisted only with an /ask message. FilePath is repository
// relative and WorkspacePath is relative to the review workspace, allowing a
// multi-repository workspace to retain unambiguous inline question context.
type Location struct {
	RepoID        string `json:"repoId,omitempty"`
	FilePath      string `json:"filePath,omitempty"`
	WorkspacePath string `json:"workspacePath,omitempty"`
	Side          string `json:"side,omitempty"`
	StartLine     int    `json:"startLine,omitempty"`
	EndLine       int    `json:"endLine,omitempty"`
	SelectedCode  string `json:"selectedCode,omitempty"`
}

type Conversation struct {
	ID               string           `json:"id"`
	QueueItemID      *string          `json:"queueItemId"`
	ReviewSessionID  *int64           `json:"reviewSessionId"`
	ArchivedAt       *int64           `json:"archivedAt"`
	Model            *string          `json:"model"`
	ReasoningEffort  *ReasoningEffort `json:"reasoningEffort"`
	ContextTier      *ContextTier     `json:"contextTier"`
	CopilotSessionID *string          `json:"copilotSessionId"`
	Context          *Location        `json:"context"`
	CreatedAt        int64            `json:"createdAt"`
	UpdatedAt        int64            `json:"updatedAt"`
}

// WorkspaceSettings is the durable picker default for one review workspace.
// Empty fields mean "let Copilot decide". Conversation fields remain explicit
// overrides, so changing this record affects only conversations that have not
// picked their own value.
type WorkspaceSettings struct {
	WorkspacePath   string           `json:"workspacePath"`
	Model           *string          `json:"model"`
	ReasoningEffort *ReasoningEffort `json:"reasoningEffort"`
	ContextTier     *ContextTier     `json:"contextTier"`
	UpdatedAt       int64            `json:"updatedAt"`
}

type UpdateWorkspaceSettingsInput struct {
	Model           *string
	ReasoningEffort *ReasoningEffort
	ContextTier     *ContextTier
}

type Message struct {
	ID             int64     `json:"id"`
	ConversationID string    `json:"conversationId"`
	Role           Role      `json:"role"`
	Body           string    `json:"body"`
	Pending        bool      `json:"pending"`
	Location       *Location `json:"location"`
	CreatedAt      int64     `json:"createdAt"`
}

type Question struct {
	ID            int64  `json:"id"`
	QuestionSetID string `json:"questionSetId"`
	Body          string `json:"body"`
	Position      int    `json:"position"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

type QuestionSet struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Questions []Question `json:"questions"`
	CreatedAt int64      `json:"createdAt"`
	UpdatedAt int64      `json:"updatedAt"`
}

type CreateConversationInput struct {
	QueueItemID      string
	ReviewSessionID  *int64
	Model            string
	ReasoningEffort  *ReasoningEffort
	ContextTier      *ContextTier
	CopilotSessionID string
	Context          *Location
}

type UpdateConversationInput struct {
	Model            *string
	ReasoningEffort  *ReasoningEffort
	ContextTier      *ContextTier
	CopilotSessionID *string
}

// GetWorkspaceSettings returns a stable, empty default record when the
// workspace has never configured /ask. That lets callers render a picker
// without treating first use as an error or silently persisting a selection.
func GetWorkspaceSettings(ctx context.Context, db *sql.DB, workspacePath string) (*WorkspaceSettings, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("/ask workspace path is required")
	}
	var model, reasoning, tier sql.NullString
	item := &WorkspaceSettings{WorkspacePath: workspacePath}
	err := db.QueryRowContext(ctx, `SELECT model,reasoning_effort,context_tier,updated_at FROM ask_workspace_settings WHERE workspace_path=?`, workspacePath).Scan(&model, &reasoning, &tier, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, nil
	}
	if err != nil {
		return nil, err
	}
	item.Model = optionalString(model)
	if reasoning.Valid && strings.TrimSpace(reasoning.String) != "" {
		value := ReasoningEffort(reasoning.String)
		item.ReasoningEffort = &value
	}
	if tier.Valid && strings.TrimSpace(tier.String) != "" {
		value := ContextTier(tier.String)
		item.ContextTier = &value
	}
	if err := validateModelSettings(item.ReasoningEffort, item.ContextTier); err != nil {
		return nil, fmt.Errorf("stored /ask workspace settings: %w", err)
	}
	return item, nil
}

// UpdateWorkspaceSettings patches only supplied picker defaults. Passing an
// empty model explicitly clears the default and restores Copilot's auto mode.
func UpdateWorkspaceSettings(ctx context.Context, db *sql.DB, workspacePath string, input UpdateWorkspaceSettingsInput) (*WorkspaceSettings, error) {
	current, err := GetWorkspaceSettings(ctx, db, workspacePath)
	if err != nil {
		return nil, err
	}
	if err := validateModelSettings(input.ReasoningEffort, input.ContextTier); err != nil {
		return nil, err
	}
	if input.Model != nil {
		if strings.TrimSpace(*input.Model) == "" {
			current.Model = nil
		} else {
			value := strings.TrimSpace(*input.Model)
			current.Model = &value
		}
	}
	if input.ReasoningEffort != nil {
		current.ReasoningEffort = input.ReasoningEffort
	}
	if input.ContextTier != nil {
		current.ContextTier = input.ContextTier
	}
	now := time.Now().UnixMilli()
	_, err = db.ExecContext(ctx, `INSERT INTO ask_workspace_settings(workspace_path,model,reasoning_effort,context_tier,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(workspace_path) DO UPDATE SET model=excluded.model,reasoning_effort=excluded.reasoning_effort,context_tier=excluded.context_tier,updated_at=excluded.updated_at`, current.WorkspacePath, nullablePointerString(current.Model), optionalReasoning(current.ReasoningEffort), optionalTier(current.ContextTier), now)
	if err != nil {
		return nil, err
	}
	return GetWorkspaceSettings(ctx, db, workspacePath)
}

func CreateConversation(ctx context.Context, db *sql.DB, input CreateConversationInput) (*Conversation, error) {
	if err := validateModelSettings(input.ReasoningEffort, input.ContextTier); err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	contextJSON, err := marshalLocation(input.Context)
	if err != nil {
		return nil, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO ask_conversations(id, queue_item_id, review_session_id, model, reasoning_effort, context_tier, copilot_session_id, context_json, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, nullString(input.QueueItemID), input.ReviewSessionID, nullString(input.Model), optionalReasoning(input.ReasoningEffort), optionalTier(input.ContextTier), nullString(input.CopilotSessionID), contextJSON, now, now)
	if err != nil {
		return nil, err
	}
	return GetConversation(ctx, db, id)
}

func GetConversation(ctx context.Context, db *sql.DB, id string) (*Conversation, error) {
	row := db.QueryRowContext(ctx, `SELECT id,queue_item_id,review_session_id,archived_at,model,reasoning_effort,context_tier,copilot_session_id,context_json,created_at,updated_at FROM ask_conversations WHERE id=?`, id)
	conversation, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return conversation, err
}

func ListConversations(ctx context.Context, db *sql.DB, reviewSessionID *int64, includeArchived bool) ([]Conversation, error) {
	where, args := "", []any{}
	if reviewSessionID != nil {
		where = " WHERE review_session_id=?"
		args = append(args, *reviewSessionID)
	}
	if !includeArchived {
		if where == "" {
			where = " WHERE archived_at IS NULL"
		} else {
			where += " AND archived_at IS NULL"
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id,queue_item_id,review_session_id,archived_at,model,reasoning_effort,context_tier,copilot_session_id,context_json,created_at,updated_at FROM ask_conversations`+where+` ORDER BY updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Conversation{}
	for rows.Next() {
		item, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *item)
	}
	return result, rows.Err()
}

// ArchiveActive archives every live conversation of this review session. A
// new review round can safely use a fresh SDK session without deleting history.
func ArchiveActive(ctx context.Context, db *sql.DB, reviewSessionID int64) error {
	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, `UPDATE ask_conversations SET archived_at=?,updated_at=? WHERE review_session_id=? AND archived_at IS NULL`, now, now, reviewSessionID)
	return err
}

// Resume archives other live conversations in the same review session, then
// restores exactly this historic conversation as the active context.
func Resume(ctx context.Context, db *sql.DB, id string) (*Conversation, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sessionID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT review_session_id FROM ask_conversations WHERE id=?`, id).Scan(&sessionID); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	if sessionID.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE ask_conversations SET archived_at=?,updated_at=? WHERE review_session_id=? AND archived_at IS NULL AND id<>?`, now, now, sessionID.Int64, id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ask_conversations SET archived_at=NULL,updated_at=? WHERE id=?`, now, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetConversation(ctx, db, id)
}

func UpdateConversation(ctx context.Context, db *sql.DB, id string, input UpdateConversationInput) (*Conversation, error) {
	current, err := GetConversation(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if err := validateModelSettings(input.ReasoningEffort, input.ContextTier); err != nil {
		return nil, err
	}
	model := current.Model
	if input.Model != nil {
		model = input.Model
	}
	reasoning := current.ReasoningEffort
	if input.ReasoningEffort != nil {
		reasoning = input.ReasoningEffort
	}
	tier := current.ContextTier
	if input.ContextTier != nil {
		tier = input.ContextTier
	}
	session := current.CopilotSessionID
	if input.CopilotSessionID != nil {
		session = input.CopilotSessionID
	}
	_, err = db.ExecContext(ctx, `UPDATE ask_conversations SET model=?,reasoning_effort=?,context_tier=?,copilot_session_id=?,updated_at=? WHERE id=?`, model, reasoning, tier, session, time.Now().UnixMilli(), id)
	if err != nil {
		return nil, err
	}
	return GetConversation(ctx, db, id)
}

func InsertMessage(ctx context.Context, db *sql.DB, conversationID string, role Role, body string, pending bool, location *Location) (*Message, error) {
	if role != RoleUser && role != RoleAssistant && role != RoleSystem {
		return nil, fmt.Errorf("invalid /ask role %q", role)
	}
	if strings.TrimSpace(body) == "" && !pending {
		return nil, errors.New("/ask message body is required")
	}
	locationJSON, err := marshalLocation(location)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	result, err := db.ExecContext(ctx, `INSERT INTO ask_messages(conversation_id,role,body,pending,location_json,created_at) VALUES(?,?,?,?,?,?)`, conversationID, role, body, boolInt(pending), locationJSON, now)
	if err != nil {
		return nil, err
	}
	if _, err = db.ExecContext(ctx, `UPDATE ask_conversations SET updated_at=? WHERE id=?`, now, conversationID); err != nil {
		return nil, err
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetMessage(ctx, db, messageID)
}

func GetMessage(ctx context.Context, db *sql.DB, id int64) (*Message, error) {
	message, err := scanMessage(db.QueryRowContext(ctx, `SELECT id,conversation_id,role,body,pending,location_json,created_at FROM ask_messages WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return message, err
}

// AppendPendingMessage records one streamed assistant fragment. The row is
// addressed by its immutable message ID rather than "the latest pending row",
// so an older delayed SDK callback can never be attached to a newer prompt.
// It returns ErrNotFound when the row was already settled (for example after a
// cancellation or daemon restart); callers should treat that as a harmless
// stale callback, not resurrect the response.
func AppendPendingMessage(ctx context.Context, db *sql.DB, id int64, fragment string) (*Message, error) {
	if fragment == "" {
		return GetMessage(ctx, db, id)
	}
	result, err := db.ExecContext(ctx, `UPDATE ask_messages SET body=body||? WHERE id=? AND role=? AND pending=1`, fragment, id, RoleAssistant)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, ErrNotFound
	}
	message, err := GetMessage(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE ask_conversations SET updated_at=? WHERE id=?`, time.Now().UnixMilli(), message.ConversationID); err != nil {
		return nil, err
	}
	return message, nil
}

// SettlePendingMessage finishes exactly one streaming assistant row. It is
// intentionally idempotent: cancellation/error races are normal in a
// streaming UI and must not make a later reload replay a completed turn.
func SettlePendingMessage(ctx context.Context, db *sql.DB, id int64, fallback string) (*Message, bool, error) {
	result, err := db.ExecContext(ctx, `UPDATE ask_messages SET pending=0,body=CASE WHEN body='' THEN ? ELSE body END WHERE id=? AND role=? AND pending=1`, fallback, id, RoleAssistant)
	if err != nil {
		return nil, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	message, err := GetMessage(ctx, db, id)
	if err != nil {
		return nil, false, err
	}
	if changed > 0 {
		if _, err := db.ExecContext(ctx, `UPDATE ask_conversations SET updated_at=? WHERE id=?`, time.Now().UnixMilli(), message.ConversationID); err != nil {
			return nil, false, err
		}
	}
	return message, changed > 0, nil
}

func ListMessages(ctx context.Context, db *sql.DB, conversationID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,conversation_id,role,body,pending,location_json,created_at FROM ask_messages WHERE conversation_id=? ORDER BY id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Message{}
	for rows.Next() {
		item, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *item)
	}
	return result, rows.Err()
}

func SettleInterruptedMessages(ctx context.Context, db *sql.DB) (int64, error) {
	result, err := db.ExecContext(ctx, `UPDATE ask_messages SET pending=0,body=CASE WHEN body='' THEN 'Response interrupted before it completed.' ELSE body END WHERE pending=1`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func CreateQuestionSet(ctx context.Context, db *sql.DB, name string, bodies []string) (*QuestionSet, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("question set name is required")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO question_sets(id,name,created_at,updated_at) VALUES(?,?,?,?)`, id, name, now, now); err != nil {
		return nil, err
	}
	for position, body := range bodies {
		if err := insertQuestion(ctx, tx, id, body, position, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return GetQuestionSet(ctx, db, id)
}

// ReplaceQuestionSet atomically replaces ordered prompts while preserving its
// identity, which keeps a selected named set stable in the UI.
func ReplaceQuestionSet(ctx context.Context, db *sql.DB, id, name string, bodies []string) (*QuestionSet, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("question set name is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE question_sets SET name=?,updated_at=? WHERE id=?`, name, time.Now().UnixMilli(), id)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM questions WHERE question_set_id=?`, id); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	for position, body := range bodies {
		if err := insertQuestion(ctx, tx, id, body, position, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return GetQuestionSet(ctx, db, id)
}

func DeleteQuestionSet(ctx context.Context, db *sql.DB, id string) (bool, error) {
	result, err := db.ExecContext(ctx, `DELETE FROM question_sets WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func GetQuestionSet(ctx context.Context, db *sql.DB, id string) (*QuestionSet, error) {
	return getQuestionSet(ctx, db, id)
}

func ListQuestionSets(ctx context.Context, db *sql.DB) ([]QuestionSet, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM question_sets ORDER BY updated_at DESC,name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []QuestionSet{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := getQuestionSet(ctx, db, id)
		if err != nil {
			return nil, err
		}
		result = append(result, *item)
	}
	return result, rows.Err()
}

func getQuestionSet(ctx context.Context, db *sql.DB, id string) (*QuestionSet, error) {
	var item QuestionSet
	err := db.QueryRowContext(ctx, `SELECT id,name,created_at,updated_at FROM question_sets WHERE id=?`, id).Scan(&item.ID, &item.Name, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,question_set_id,body,position,created_at,updated_at FROM questions WHERE question_set_id=? ORDER BY position,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var question Question
		if err := rows.Scan(&question.ID, &question.QuestionSetID, &question.Body, &question.Position, &question.CreatedAt, &question.UpdatedAt); err != nil {
			return nil, err
		}
		item.Questions = append(item.Questions, question)
	}
	return &item, rows.Err()
}

func insertQuestion(ctx context.Context, tx *sql.Tx, id, body string, position int, now int64) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return errors.New("question text is required")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO questions(question_set_id,body,position,created_at,updated_at) VALUES(?,?,?,?,?)`, id, body, position, now, now)
	return err
}

type scanner interface{ Scan(...any) error }

func scanConversation(row scanner) (*Conversation, error) {
	var item Conversation
	var contextJSON sql.NullString
	var reasoning, tier sql.NullString
	err := row.Scan(&item.ID, &item.QueueItemID, &item.ReviewSessionID, &item.ArchivedAt, &item.Model, &reasoning, &tier, &item.CopilotSessionID, &contextJSON, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if reasoning.Valid {
		value := ReasoningEffort(reasoning.String)
		item.ReasoningEffort = &value
	}
	if tier.Valid {
		value := ContextTier(tier.String)
		item.ContextTier = &value
	}
	location, err := unmarshalLocation(contextJSON)
	if err != nil {
		return nil, err
	}
	item.Context = location
	return &item, nil
}
func scanMessage(row scanner) (*Message, error) {
	var item Message
	var pending int
	var locationJSON sql.NullString
	err := row.Scan(&item.ID, &item.ConversationID, &item.Role, &item.Body, &pending, &locationJSON, &item.CreatedAt)
	if err != nil {
		return nil, err
	}
	item.Pending = pending != 0
	location, err := unmarshalLocation(locationJSON)
	if err != nil {
		return nil, err
	}
	item.Location = location
	return &item, nil
}
func marshalLocation(location *Location) (any, error) {
	if location == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(location)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}
func unmarshalLocation(value sql.NullString) (*Location, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	var location Location
	if err := json.Unmarshal([]byte(value.String), &location); err != nil {
		return nil, fmt.Errorf("decode /ask location: %w", err)
	}
	return &location, nil
}
func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func optionalString(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value.String)
	return &trimmed
}
func nullablePointerString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}
func optionalReasoning(value *ReasoningEffort) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
func optionalTier(value *ContextTier) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func validateModelSettings(reasoning *ReasoningEffort, tier *ContextTier) error {
	if reasoning != nil && *reasoning != ReasoningLow && *reasoning != ReasoningMedium && *reasoning != ReasoningHigh && *reasoning != ReasoningXHigh {
		return fmt.Errorf("invalid reasoning effort %q", *reasoning)
	}
	if tier != nil && *tier != ContextDefault && *tier != ContextLong {
		return fmt.Errorf("invalid context tier %q", *tier)
	}
	return nil
}
