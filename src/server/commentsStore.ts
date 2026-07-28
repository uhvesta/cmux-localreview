import type { Database } from "bun:sqlite";
import { createHash } from "node:crypto";

import type { RepoInfo } from "./workspace.ts";

// Mirrors difit's DiffCommentThread shape (src/types/diff.ts) so the
// existing client comment UI (useDiffComments, CommentThreadCard, ...) needs
// no changes — only the storage behind /api/comments* is swapped.
export interface CommentMessage {
  id: string;
  body: string;
  author?: string;
  createdAt: string;
  updatedAt: string;
}

export interface DiffCommentThread {
  id: string;
  filePath: string;
  createdAt: string;
  updatedAt: string;
  position: {
    side: "old" | "new";
    line: number | { start: number; end: number };
  };
  codeSnapshot?: { content: string };
  messages: CommentMessage[];
  /** Only formal threads may enter export/delivery/GitHub review paths. */
  channel?: "formal" | "ask";
  orphaned?: boolean;
}

export function upsertRepoRow(db: Database, repo: RepoInfo): number {
  const now = Date.now();
  db.query(
    `INSERT INTO repos (workspace_relative_path, git_dir, remote_url, created_at)
     VALUES (?, ?, ?, ?)
     ON CONFLICT(git_dir) DO UPDATE SET
       workspace_relative_path = excluded.workspace_relative_path,
       remote_url = excluded.remote_url`,
  ).run(repo.workspaceRelativePath, repo.gitDir, repo.remoteUrl, now);

  const row = db
    .query(`SELECT id FROM repos WHERE git_dir = ?`)
    .get(repo.gitDir) as { id: number } | null;
  if (!row) throw new Error(`Failed to upsert repo row for ${repo.gitDir}`);
  return row.id;
}

/** Returns the current (non-frozen) review session, creating one if none exists. */
export function getActiveSessionId(db: Database): number {
  const existing = db
    .query(`SELECT id FROM sessions WHERE frozen_at IS NULL ORDER BY id DESC LIMIT 1`)
    .get() as { id: number } | null;
  if (existing) return existing.id;

  const now = Date.now();
  const result = db.query(`INSERT INTO sessions (label, started_at) VALUES (NULL, ?)`).run(now);
  return Number(result.lastInsertRowid);
}

/** Freezes whatever session is currently active, if any (idempotent). */
export function freezeActiveSession(db: Database): void {
  db.query(`UPDATE sessions SET frozen_at = ? WHERE frozen_at IS NULL`).run(Date.now());
}

/**
 * "New Review" (SPEC.md §5): freezes the current session and starts the
 * next one. Comments/btw threads are session-scoped, so frozen sessions
 * remain browsable/exportable via their own id but stop accumulating new
 * activity.
 */
export function startNewSession(db: Database, label?: string): number {
  freezeActiveSession(db);
  const result = db
    .query(`INSERT INTO sessions (label, started_at) VALUES (?, ?)`)
    .run(label ?? null, Date.now());
  return Number(result.lastInsertRowid);
}

export interface SessionSummary {
  id: number;
  label: string | null;
  startedAt: number;
  frozenAt: number | null;
  commentCount: number;
  btwThreadCount: number;
}

export function listSessions(db: Database): SessionSummary[] {
  const rows = db
    .query(`SELECT id, label, started_at, frozen_at FROM sessions ORDER BY id DESC`)
    .all() as { id: number; label: string | null; started_at: number; frozen_at: number | null }[];

  return rows.map((row) => {
    const commentCount = (
      db.query(`SELECT COUNT(*) as c FROM comments WHERE session_id = ?`).get(row.id) as { c: number }
    ).c;
    const btwThreadCount = (
      db.query(`SELECT COUNT(*) as c FROM btw_threads WHERE session_id = ?`).get(row.id) as { c: number }
    ).c;
    return {
      id: row.id,
      label: row.label,
      startedAt: row.started_at,
      frozenAt: row.frozen_at,
      commentCount,
      btwThreadCount,
    };
  });
}

function lineRange(line: DiffCommentThread["position"]["line"]): { start: number; end: number } {
  return typeof line === "number" ? { start: line, end: line } : line;
}

function hashContent(content: string): string {
  return createHash("sha256").update(content).digest("hex");
}

interface CommentRow {
  thread_id: string;
  file_path: string;
  side: string;
  start_line: number;
  end_line: number;
  messages_json: string;
  anchor_content: string | null;
  orphaned: number;
  created_at: number;
  updated_at: number;
  channel?: "formal" | "ask";
}

function rowToThread(row: CommentRow): DiffCommentThread {
  const messages = JSON.parse(row.messages_json) as CommentMessage[];
  const line =
    row.start_line === row.end_line ? row.start_line : { start: row.start_line, end: row.end_line };
  return {
    id: row.thread_id,
    filePath: row.file_path,
    createdAt: new Date(row.created_at).toISOString(),
    updatedAt: new Date(row.updated_at).toISOString(),
    position: {
      side: row.side === "old" ? "old" : "new",
      line,
    },
    codeSnapshot: row.anchor_content != null ? { content: row.anchor_content } : undefined,
    messages,
    channel: row.channel === "ask" ? "ask" : "formal",
    orphaned: !!row.orphaned,
  };
}

export function listThreads(db: Database, sessionId: number, repoDbId: number): DiffCommentThread[] {
  const rows = db
    .query(
      `SELECT thread_id, file_path, side, start_line, end_line, messages_json, anchor_content, orphaned, created_at, updated_at, channel
       FROM comments WHERE session_id = ? AND repo_id = ? ORDER BY created_at ASC`,
    )
    .all(sessionId, repoDbId) as CommentRow[];
  return rows.map(rowToThread);
}

export interface HistoricalReviewThread extends DiffCommentThread {
  reviewSessionId: number;
  reviewLabel: string | null;
  workspaceRelativePath: string;
}

/**
 * Older formal-review threads remain immutable history. They are fetched
 * separately from the active difit comment state so a client save can never
 * copy them into the current review round.
 */
export function listHistoricalReviewThreads(db: Database, activeSessionId: number): HistoricalReviewThread[] {
  const rows = db.query(
    `SELECT c.thread_id, c.file_path, c.side, c.start_line, c.end_line,
            c.messages_json, c.anchor_content, c.orphaned, c.created_at,
            c.updated_at, c.channel, c.session_id, s.label AS session_label,
            r.workspace_relative_path
       FROM comments c
       JOIN sessions s ON s.id = c.session_id
       JOIN repos r ON r.id = c.repo_id
      WHERE c.session_id != ?
      ORDER BY c.session_id DESC, c.created_at ASC`,
  ).all(activeSessionId) as (CommentRow & { session_id: number; session_label: string | null; workspace_relative_path: string })[];
  return rows.map((row) => ({
    ...rowToThread(row),
    reviewSessionId: row.session_id,
    reviewLabel: row.session_label,
    workspaceRelativePath: row.workspace_relative_path,
  }));
}

export function upsertThread(
  db: Database,
  sessionId: number,
  repoDbId: number,
  thread: DiffCommentThread,
): void {
  const { start, end } = lineRange(thread.position.line);
  const now = Date.now();
  const firstBody = thread.messages[0]?.body ?? "";
  const anchorContent = thread.codeSnapshot?.content ?? null;

  db.query(
    `INSERT INTO comments (
       session_id, repo_id, file_path, side, start_line, end_line, body,
       anchor_content_hash, anchor_content, orphaned, thread_id, messages_json, created_at, updated_at, channel
     ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)
     ON CONFLICT(session_id, repo_id, thread_id) DO UPDATE SET
       file_path = excluded.file_path,
       side = excluded.side,
       start_line = excluded.start_line,
       end_line = excluded.end_line,
       body = excluded.body,
       anchor_content_hash = excluded.anchor_content_hash,
       anchor_content = excluded.anchor_content,
       messages_json = excluded.messages_json,
       updated_at = excluded.updated_at,
       channel = excluded.channel`,
  ).run(
    sessionId,
    repoDbId,
    thread.filePath,
    thread.position.side === "old" ? "old" : "new",
    start,
    end,
    firstBody,
    anchorContent != null ? hashContent(anchorContent) : "",
    anchorContent,
    thread.id,
    JSON.stringify(thread.messages),
    thread.createdAt ? Date.parse(thread.createdAt) || now : now,
    now,
    thread.channel === "ask" ? "ask" : "formal",
  );
}

export function deleteThread(
  db: Database,
  sessionId: number,
  repoDbId: number,
  threadId: string,
): boolean {
  const result = db
    .query(`DELETE FROM comments WHERE session_id = ? AND repo_id = ? AND thread_id = ?`)
    .run(sessionId, repoDbId, threadId);
  return result.changes > 0;
}

/** Prevent an already-resolved client-generated id from being revived by a stale tab. */
export function tombstoneThread(db: Database, sessionId: number, repoDbId: number, threadId: string): void {
  db.query(
    `INSERT INTO comment_tombstones(session_id, repo_id, thread_id, deleted_at)
     VALUES (?, ?, ?, ?)
     ON CONFLICT(session_id, repo_id, thread_id) DO NOTHING`,
  ).run(sessionId, repoDbId, threadId, Date.now());
}

export function isThreadTombstoned(db: Database, sessionId: number, repoDbId: number, threadId: string): boolean {
  return db.query(
    `SELECT 1 AS present FROM comment_tombstones WHERE session_id = ? AND repo_id = ? AND thread_id = ?`,
  ).get(sessionId, repoDbId, threadId) !== null;
}

/**
 * Re-anchoring check (SPEC.md §3): a comment survives a diff refresh as long
 * as its anchored snapshot text still matches the corresponding line range
 * in the current file; otherwise it's flagged orphaned (kept, never
 * dropped). Threads with no snapshot (plain line-number-only comments) are
 * never considered orphaned — there's nothing to re-anchor against.
 */
export async function refreshOrphanFlags(
  db: Database,
  sessionId: number,
  repoDbId: number,
  currentContentAt: (
    filePath: string,
    side: "old" | "new",
    startLine: number,
    endLine: number,
  ) => Promise<string | undefined> | string | undefined,
): Promise<void> {
  const rows = db
    .query(
      `SELECT thread_id, file_path, side, start_line, end_line, anchor_content FROM comments
       WHERE session_id = ? AND repo_id = ?`,
    )
    .all(sessionId, repoDbId) as (Pick<
    CommentRow,
    "thread_id" | "file_path" | "side" | "start_line" | "end_line"
  > & { anchor_content: string | null })[];

  for (const row of rows) {
    if (row.anchor_content == null) {
      db.query(
        `UPDATE comments SET orphaned = 0 WHERE session_id = ? AND repo_id = ? AND thread_id = ?`,
      ).run(sessionId, repoDbId, row.thread_id);
      continue;
    }

    const current = await currentContentAt(
      row.file_path,
      row.side === "old" ? "old" : "new",
      row.start_line,
      row.end_line,
    );
    const orphaned = current === undefined || current !== row.anchor_content;
    db.query(
      `UPDATE comments SET orphaned = ? WHERE session_id = ? AND repo_id = ? AND thread_id = ?`,
    ).run(orphaned ? 1 : 0, sessionId, repoDbId, row.thread_id);
  }
}
