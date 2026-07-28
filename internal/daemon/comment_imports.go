package daemon

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// commentImport mirrors difit's portable import format. A reply is matched to
// the newest persisted thread at the same file/side/line range; it never
// creates an unanchored formal review comment by itself.
type commentImport struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Channel  string `json:"channel"`
	FilePath string `json:"filePath"`
	Position struct {
		Side string          `json:"side"`
		Line json.RawMessage `json:"line"`
	} `json:"position"`
	Body         string `json:"body"`
	Author       string `json:"author"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	CodeSnapshot struct {
		Content string `json:"content"`
	} `json:"codeSnapshot"`
}

// normalizeCommentImports accepts the portable durable format as the native
// contract, plus the compact {id,file,line,body} rows emitted by the frozen
// TypeScript reviewer. Keeping this translation at the HTTP boundary lets a
// restored older browser import its comments without reintroducing a second
// storage model.
func normalizeCommentImports(raw json.RawMessage) ([]commentImport, error) {
	var rows []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
	} else {
		rows = []json.RawMessage{raw}
	}
	entries := make([]commentImport, 0, len(rows))
	for _, row := range rows {
		var entry commentImport
		if err := json.Unmarshal(row, &entry); err != nil {
			return nil, err
		}
		if entry.Type == "" {
			var legacy struct {
				ID   string `json:"id"`
				File string `json:"file"`
				Line int64  `json:"line"`
				Body string `json:"body"`
			}
			if err := json.Unmarshal(row, &legacy); err != nil {
				return nil, err
			}
			if legacy.File != "" && legacy.Line > 0 && strings.TrimSpace(legacy.Body) != "" {
				entry.Type = "thread"
				entry.ID = legacy.ID
				entry.Channel = "formal"
				entry.FilePath = legacy.File
				entry.Position.Side = "new"
				entry.Position.Line, _ = json.Marshal(legacy.Line)
				entry.Body = legacy.Body
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

type importedMessage struct {
	ID        string `json:"id,omitempty"`
	Body      string `json:"body"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func importLines(raw json.RawMessage) (int64, int64, error) {
	var single int64
	if json.Unmarshal(raw, &single) == nil && single > 0 {
		return single, single, nil
	}
	var span struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	}
	if json.Unmarshal(raw, &span) != nil || span.Start < 1 || span.End < span.Start {
		return 0, 0, errors.New("invalid comment import field: position.line")
	}
	return span.Start, span.End, nil
}

func importID(value commentImport, start, end int64) string {
	if strings.TrimSpace(value.ID) != "" {
		return strings.TrimSpace(value.ID)
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%s", value.FilePath, value.Position.Side, start, end, value.Author, value.Body, value.CreatedAt)))
	return "import-" + hex.EncodeToString(hash[:12])
}

func validateImport(value commentImport) (int64, int64, error) {
	if value.Type != "thread" && value.Type != "reply" {
		return 0, 0, errors.New("invalid comment import field: type")
	}
	if strings.TrimSpace(value.FilePath) == "" {
		return 0, 0, errors.New("invalid comment import field: filePath")
	}
	if value.Position.Side != "old" && value.Position.Side != "new" {
		return 0, 0, errors.New("invalid comment import field: position.side")
	}
	if strings.TrimSpace(value.Body) == "" {
		return 0, 0, errors.New("invalid comment import field: body")
	}
	return importLines(value.Position.Line)
}

func (d *Daemon) importComments(review *workspaceReview, repo reviewRepo, entries []commentImport) (int, []string, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	// Imports mutate the same durable thread collection as browser snapshots,
	// so they must invalidate a tab's optimistic revision too.
	if _, _, err := advanceCommentRevision(tx, review.SessionID, repo.DBID, nil); err != nil {
		return 0, nil, err
	}
	now := time.Now().UnixMilli()
	changed := 0
	warnings := []string{}
	for _, entry := range entries {
		start, end, err := validateImport(entry)
		if err != nil {
			return 0, nil, err
		}
		if _, err := repoRelativePath(entry.FilePath); err != nil {
			return 0, nil, err
		}
		if entry.Type == "reply" {
			var threadID, raw string
			err = tx.QueryRow(`SELECT thread_id,COALESCE(messages_json,'[]') FROM comments WHERE session_id=? AND repo_id=? AND file_path=? AND side=? AND start_line=? AND end_line=? ORDER BY updated_at DESC,id DESC LIMIT 1`, review.SessionID, repo.DBID, entry.FilePath, entry.Position.Side, start, end).Scan(&threadID, &raw)
			if errors.Is(err, sql.ErrNoRows) {
				warnings = append(warnings, fmt.Sprintf("Skipped reply import for %s:%s:%d because no matching thread was found.", entry.FilePath, entry.Position.Side, start))
				continue
			}
			if err != nil {
				return 0, nil, err
			}
			var messages []importedMessage
			_ = json.Unmarshal([]byte(raw), &messages)
			messageID := importID(entry, start, end)
			duplicate := false
			for _, existing := range messages {
				if existing.ID == messageID || (existing.Body == entry.Body && existing.Author == entry.Author && existing.CreatedAt == entry.CreatedAt) {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			messages = append(messages, importedMessage{ID: messageID, Body: strings.TrimSpace(entry.Body), Author: entry.Author, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt})
			next, _ := json.Marshal(messages)
			if _, err = tx.Exec(`UPDATE comments SET messages_json=?,updated_at=? WHERE session_id=? AND repo_id=? AND thread_id=?`, string(next), now, review.SessionID, repo.DBID, threadID); err != nil {
				return 0, nil, err
			}
			changed++
			continue
		}
		threadID := importID(entry, start, end)
		var tombstoned int
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM comment_tombstones WHERE session_id=? AND repo_id=? AND thread_id=?)`, review.SessionID, repo.DBID, threadID).Scan(&tombstoned); err != nil {
			return 0, nil, err
		}
		if tombstoned != 0 {
			continue
		}
		message := importedMessage{ID: threadID, Body: strings.TrimSpace(entry.Body), Author: entry.Author, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt}
		messages, _ := json.Marshal([]importedMessage{message})
		hash := sha256.Sum256([]byte(entry.CodeSnapshot.Content))
		channel := commentChannel(entry.Channel, []durableCommentMessage{{Body: message.Body}})
		result, err := tx.Exec(`INSERT INTO comments(session_id,repo_id,file_path,side,start_line,end_line,body,anchor_content_hash,created_at,updated_at,thread_id,messages_json,anchor_content,channel) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,repo_id,thread_id) DO NOTHING`, review.SessionID, repo.DBID, entry.FilePath, entry.Position.Side, start, end, message.Body, fmt.Sprintf("%x", hash[:]), now, now, threadID, string(messages), nullable(entry.CodeSnapshot.Content), channel)
		if err != nil {
			return 0, nil, err
		}
		n, _ := result.RowsAffected()
		changed += int(n)
	}
	if err = tx.Commit(); err != nil {
		return 0, nil, err
	}
	return changed, warnings, nil
}
