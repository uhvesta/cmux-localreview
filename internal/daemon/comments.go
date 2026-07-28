package daemon

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Comment snapshots are saved by more than one browser tab.  Keep their
// revision separate from the general UI-state keys so a stale autosave cannot
// resurrect a thread another reviewer just resolved.  Unlike the old Bun
// process-local counter, this value survives daemon/browser restarts.
func commentRevisionKey(sessionID, repoID int64) string {
	return fmt.Sprintf("comments-revision:%d:%d", sessionID, repoID)
}

func readCommentRevision(query interface {
	QueryRow(string, ...any) *sql.Row
}, sessionID, repoID int64) (int, error) {
	var revision int
	err := query.QueryRow(`SELECT revision FROM ui_state WHERE key=?`, commentRevisionKey(sessionID, repoID)).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

// advanceCommentRevision must be called in the same transaction as the
// comment mutation. expected may be nil for clients which do not implement
// optimistic concurrency yet. stale is true when no mutation was performed.
func advanceCommentRevision(tx *sql.Tx, sessionID, repoID int64, expected *int) (next int, stale bool, err error) {
	current, err := readCommentRevision(tx, sessionID, repoID)
	if err != nil {
		return 0, false, err
	}
	if expected != nil && *expected != current {
		return current, true, nil
	}
	next = current + 1
	_, err = tx.Exec(`INSERT INTO ui_state(key,value,updated_at,revision) VALUES(?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at,revision=excluded.revision`,
		commentRevisionKey(sessionID, repoID), `null`, time.Now().UnixMilli(), next)
	return next, false, err
}

type durableCommentMessage struct {
	ID        string `json:"id,omitempty"`
	Body      string `json:"body"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func commentChannel(requested string, messages []durableCommentMessage) string {
	if requested == "ask" {
		return "ask"
	}
	if requested == "formal" {
		return "formal"
	}
	for _, message := range messages {
		body := strings.ToLower(strings.TrimSpace(message.Body))
		if body == "/ask" || strings.HasPrefix(body, "/ask ") {
			return "ask"
		}
	}
	return "formal"
}

// commentThreads is deliberately the one projection used by both GET and a
// stale POST response.  The channel is persisted rather than inferred at
// export time: /ask content can therefore never leak into formal feedback.
func (d *Daemon) commentThreads(review workspaceReview, repo reviewRepo) ([]map[string]any, error) {
	rows, err := d.db.Query(`SELECT thread_id,file_path,side,start_line,end_line,messages_json,created_at,updated_at,anchor_content,channel,orphaned
		FROM comments WHERE session_id=? AND repo_id=? ORDER BY created_at,id`, review.SessionID, repo.DBID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	threads := []map[string]any{}
	for rows.Next() {
		var id, file, side, channel string
		var start, end, created, updated int64
		var messages, content sql.NullString
		var orphaned int
		if err := rows.Scan(&id, &file, &side, &start, &end, &messages, &created, &updated, &content, &channel, &orphaned); err != nil {
			return nil, err
		}
		var line any = start
		if start != end {
			line = map[string]int64{"start": start, "end": end}
		}
		var parsed any = []any{}
		if messages.Valid {
			_ = json.Unmarshal([]byte(messages.String), &parsed)
		}
		thread := map[string]any{"id": id, "filePath": file, "createdAt": time.UnixMilli(created).UTC().Format(time.RFC3339Nano), "updatedAt": time.UnixMilli(updated).UTC().Format(time.RFC3339Nano), "position": map[string]any{"side": side, "line": line}, "messages": parsed, "channel": channel, "orphaned": orphaned != 0}
		if content.Valid {
			thread["codeSnapshot"] = map[string]string{"content": content.String}
		}
		threads = append(threads, thread)
	}
	return threads, rows.Err()
}
