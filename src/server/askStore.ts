import { randomUUID } from "node:crypto";
import type { Database } from "bun:sqlite";

export type AskMessageRole = "user" | "assistant" | "system";

export interface AskConversation {
  id: string;
  queueItemId: string | null;
  model: string | null;
  copilotSessionId: string | null;
  createdAt: number;
  updatedAt: number;
}

export interface AskMessage {
  id: number;
  conversationId: string;
  role: AskMessageRole;
  body: string;
  pending: boolean;
  createdAt: number;
}

interface AskConversationRow {
  id: string;
  queue_item_id: string | null;
  model: string | null;
  copilot_session_id: string | null;
  created_at: number;
  updated_at: number;
}

interface AskMessageRow {
  id: number;
  conversation_id: string;
  role: AskMessageRole;
  body: string;
  pending: number;
  created_at: number;
}

function conversationFromRow(row: AskConversationRow): AskConversation {
  return {
    id: row.id,
    queueItemId: row.queue_item_id,
    model: row.model,
    copilotSessionId: row.copilot_session_id,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

function messageFromRow(row: AskMessageRow): AskMessage {
  return {
    id: row.id,
    conversationId: row.conversation_id,
    role: row.role,
    body: row.body,
    pending: row.pending === 1,
    createdAt: row.created_at,
  };
}

export function createAskConversation(
  db: Database,
  input: { queueItemId?: string; model?: string; copilotSessionId?: string },
): AskConversation {
  const now = Date.now();
  const id = randomUUID();
  db.query(
    `INSERT INTO ask_conversations(id, queue_item_id, model, copilot_session_id, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?)`,
  ).run(id, input.queueItemId ?? null, input.model ?? null, input.copilotSessionId ?? null, now, now);
  return getAskConversation(db, id)!;
}

export function getAskConversation(db: Database, id: string): AskConversation | undefined {
  const row = db.query(`SELECT * FROM ask_conversations WHERE id = ?`).get(id) as
    | AskConversationRow
    | null;
  return row ? conversationFromRow(row) : undefined;
}

export function listAskConversations(db: Database, queueItemId?: string): AskConversation[] {
  const rows = db
    .query(
      queueItemId
        ? `SELECT * FROM ask_conversations WHERE queue_item_id = ? ORDER BY updated_at DESC`
        : `SELECT * FROM ask_conversations ORDER BY updated_at DESC`,
    )
    .all(...(queueItemId ? [queueItemId] : [])) as AskConversationRow[];
  return rows.map(conversationFromRow);
}

export function updateAskConversation(
  db: Database,
  id: string,
  input: { model?: string; copilotSessionId?: string },
): AskConversation | undefined {
  const current = getAskConversation(db, id);
  if (!current) return undefined;
  db.query(
    `UPDATE ask_conversations
       SET model = ?, copilot_session_id = ?, updated_at = ?
     WHERE id = ?`,
  ).run(input.model ?? current.model, input.copilotSessionId ?? current.copilotSessionId, Date.now(), id);
  return getAskConversation(db, id);
}

export function listAskMessages(db: Database, conversationId: string): AskMessage[] {
  const rows = db
    .query(`SELECT * FROM ask_messages WHERE conversation_id = ? ORDER BY id`)
    .all(conversationId) as AskMessageRow[];
  return rows.map(messageFromRow);
}

export function insertAskMessage(
  db: Database,
  input: { conversationId: string; role: AskMessageRole; body: string; pending?: boolean },
): AskMessage {
  const result = db
    .query(
      `INSERT INTO ask_messages(conversation_id, role, body, pending, created_at)
       VALUES (?, ?, ?, ?, ?)`,
    )
    .run(input.conversationId, input.role, input.body, input.pending ? 1 : 0, Date.now());
  db.query(`UPDATE ask_conversations SET updated_at = ? WHERE id = ?`).run(
    Date.now(),
    input.conversationId,
  );
  return messageFromRow(
    db.query(`SELECT * FROM ask_messages WHERE id = ?`).get(result.lastInsertRowid) as AskMessageRow,
  );
}

export function updateAskMessage(
  db: Database,
  id: number,
  input: { body: string; pending?: boolean },
): void {
  db.query(`UPDATE ask_messages SET body = ?, pending = ? WHERE id = ?`).run(
    input.body,
    input.pending ? 1 : 0,
    id,
  );
}
