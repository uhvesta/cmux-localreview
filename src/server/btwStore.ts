import type { Database } from "bun:sqlite";

export type BtwTransport = "acp" | "terminal";

export interface BtwThreadInput {
  sessionId: number;
  transport: BtwTransport;
  acpProvider?: string;
  repoId?: number;
  filePath?: string;
  startLine?: number;
  endLine?: number;
  /** Explicit registered agent target for terminal delivery. */
  targetAgentId?: string;
}

export interface BtwAnswer {
  id: number;
  body: string;
  pending: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface BtwQuestion {
  id: number;
  body: string;
  createdAt: number;
  answer: BtwAnswer | null;
}

export interface BtwThread {
  id: number;
  transport: BtwTransport;
  acpProvider: string | null;
  acpSessionId: string | null;
  repoId: number | null;
  filePath: string | null;
  startLine: number | null;
  endLine: number | null;
  targetAgentId: string | null;
  createdAt: number;
  questions: BtwQuestion[];
}

export function createThread(db: Database, input: BtwThreadInput): number {
  const result = db
    .query(
      `INSERT INTO btw_threads (session_id, transport, acp_provider, repo_id, file_path, start_line, end_line, target_agent_id, created_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    )
    .run(
      input.sessionId,
      input.transport,
      input.acpProvider ?? null,
      input.repoId ?? null,
      input.filePath ?? null,
      input.startLine ?? null,
      input.endLine ?? null,
      input.targetAgentId ?? null,
      Date.now(),
    );
  return Number(result.lastInsertRowid);
}

export function setThreadAcpSessionId(db: Database, threadId: number, acpSessionId: string): void {
  db.query(`UPDATE btw_threads SET acp_session_id = ? WHERE id = ?`).run(acpSessionId, threadId);
}

export function getThreadAcpSessionId(db: Database, threadId: number): string | null {
  const row = db.query(`SELECT acp_session_id FROM btw_threads WHERE id = ?`).get(threadId) as
    | { acp_session_id: string | null }
    | null;
  return row?.acp_session_id ?? null;
}

export function addQuestion(db: Database, threadId: number, body: string): number {
  const result = db
    .query(`INSERT INTO btw_questions (thread_id, body, created_at) VALUES (?, ?, ?)`)
    .run(threadId, body, Date.now());
  return Number(result.lastInsertRowid);
}

/** Creates a pending answer row for a question (call once, before streaming starts). */
export function createPendingAnswer(db: Database, questionId: number): number {
  const now = Date.now();
  const result = db
    .query(
      `INSERT INTO btw_answers (question_id, body, pending, created_at, updated_at) VALUES (?, '', 1, ?, ?)`,
    )
    .run(questionId, now, now);
  return Number(result.lastInsertRowid);
}

/** Appends a streamed text chunk to an answer in place. */
export function appendAnswerChunk(db: Database, answerId: number, chunk: string): void {
  db.query(`UPDATE btw_answers SET body = body || ?, updated_at = ? WHERE id = ?`).run(
    chunk,
    Date.now(),
    answerId,
  );
}

export function finalizeAnswer(db: Database, answerId: number): void {
  db.query(`UPDATE btw_answers SET pending = 0, updated_at = ? WHERE id = ?`).run(Date.now(), answerId);
}

/** Records a terminal delivery failure so the thread never looks like it is still streaming. */
export function failAnswer(db: Database, answerId: number, error: string): void {
  db.query(`UPDATE btw_answers SET body = ?, pending = 0, updated_at = ? WHERE id = ?`)
    .run(`Delivery failed: ${error}`, Date.now(), answerId);
}

export function listThreads(db: Database, sessionId: number): BtwThread[] {
  const threadRows = db
    .query(
      `SELECT id, transport, acp_provider, acp_session_id, repo_id, file_path, start_line, end_line, target_agent_id, created_at
       FROM btw_threads WHERE session_id = ? ORDER BY created_at ASC`,
    )
    .all(sessionId) as {
    id: number;
    transport: BtwTransport;
    acp_provider: string | null;
    acp_session_id: string | null;
    repo_id: number | null;
    file_path: string | null;
    start_line: number | null;
    end_line: number | null;
    target_agent_id: string | null;
    created_at: number;
  }[];

  return threadRows.map((t) => {
    const questionRows = db
      .query(`SELECT id, body, created_at FROM btw_questions WHERE thread_id = ? ORDER BY created_at ASC`)
      .all(t.id) as { id: number; body: string; created_at: number }[];

    const questions: BtwQuestion[] = questionRows.map((q) => {
      const answerRow = db
        .query(
          `SELECT id, body, pending, created_at, updated_at FROM btw_answers WHERE question_id = ? ORDER BY id DESC LIMIT 1`,
        )
        .get(q.id) as
        | { id: number; body: string; pending: number; created_at: number; updated_at: number }
        | null;

      return {
        id: q.id,
        body: q.body,
        createdAt: q.created_at,
        answer: answerRow
          ? {
              id: answerRow.id,
              body: answerRow.body,
              pending: !!answerRow.pending,
              createdAt: answerRow.created_at,
              updatedAt: answerRow.updated_at,
            }
          : null,
      };
    });

    return {
      id: t.id,
      transport: t.transport,
      acpProvider: t.acp_provider,
      acpSessionId: t.acp_session_id,
      repoId: t.repo_id,
      filePath: t.file_path,
      startLine: t.start_line,
      endLine: t.end_line,
      targetAgentId: t.target_agent_id,
      createdAt: t.created_at,
      questions,
    };
  });
}
