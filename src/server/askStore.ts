import { randomUUID } from "node:crypto";
import type { Database } from "bun:sqlite";

export type AskMessageRole = "user" | "assistant" | "system";

/**
 * Code context is stored only with /ask data.  Queue feedback/export paths
 * intentionally do not read this table, so a research conversation cannot
 * accidentally become formal review feedback.
 */
export interface AskLocation {
  repoId?: string;
  filePath?: string;
  side?: "base" | "current";
  startLine?: number;
  endLine?: number;
  selectedCode?: string;
}

export interface AskConversation {
  id: string;
  queueItemId: string | null;
  reviewSessionId: number | null;
  archivedAt: number | null;
  model: string | null;
  reasoningEffort: "low" | "medium" | "high" | "xhigh" | null;
  contextTier: "default" | "long_context" | null;
  copilotSessionId: string | null;
  context: AskLocation | null;
  createdAt: number;
  updatedAt: number;
}

export interface AskMessage {
  id: number;
  conversationId: string;
  role: AskMessageRole;
  body: string;
  pending: boolean;
  location: AskLocation | null;
  createdAt: number;
}

interface AskConversationRow {
  id: string;
  queue_item_id: string | null;
  review_session_id: number | null;
  archived_at: number | null;
  model: string | null;
  reasoning_effort: "low" | "medium" | "high" | "xhigh" | null;
  context_tier: "default" | "long_context" | null;
  copilot_session_id: string | null;
  context_json: string | null;
  created_at: number;
  updated_at: number;
}

interface AskMessageRow {
  id: number;
  conversation_id: string;
  role: AskMessageRole;
  body: string;
  pending: number;
  location_json: string | null;
  created_at: number;
}

function conversationFromRow(row: AskConversationRow): AskConversation {
  return {
    id: row.id,
    queueItemId: row.queue_item_id,
    reviewSessionId: row.review_session_id,
    archivedAt: row.archived_at,
    model: row.model,
    reasoningEffort: row.reasoning_effort,
    contextTier: row.context_tier,
    copilotSessionId: row.copilot_session_id,
    context: parseLocation(row.context_json),
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

function parseLocation(value: string | null): AskLocation | null {
  if (!value) return null;
  try {
    const parsed = JSON.parse(value) as unknown;
    return parsed && typeof parsed === "object" ? parsed as AskLocation : null;
  } catch {
    return null;
  }
}

function messageFromRow(row: AskMessageRow): AskMessage {
  return {
    id: row.id,
    conversationId: row.conversation_id,
    role: row.role,
    body: row.body,
    pending: row.pending === 1,
    location: parseLocation(row.location_json),
    createdAt: row.created_at,
  };
}

export function createAskConversation(
  db: Database,
  input: { queueItemId?: string; reviewSessionId?: number; model?: string; reasoningEffort?: AskConversation["reasoningEffort"]; contextTier?: AskConversation["contextTier"]; copilotSessionId?: string; context?: AskLocation },
): AskConversation {
  const now = Date.now();
  const id = randomUUID();
  db.query(
    `INSERT INTO ask_conversations(id, queue_item_id, review_session_id, model, reasoning_effort, context_tier, copilot_session_id, context_json, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
  ).run(id, input.queueItemId ?? null, input.reviewSessionId ?? null, input.model ?? null, input.reasoningEffort ?? null, input.contextTier ?? null, input.copilotSessionId ?? null, input.context ? JSON.stringify(input.context) : null, now, now);
  return getAskConversation(db, id)!;
}

export function getAskConversation(db: Database, id: string): AskConversation | undefined {
  const row = db.query(`SELECT * FROM ask_conversations WHERE id = ?`).get(id) as
    | AskConversationRow
    | null;
  return row ? conversationFromRow(row) : undefined;
}

export function listAskConversations(
  db: Database,
  options: { queueItemId?: string; reviewSessionId?: number; includeArchived?: boolean } = {},
): AskConversation[] {
  const conditions: string[] = [];
  const args: (string | number)[] = [];
  if (options.queueItemId) { conditions.push("queue_item_id = ?"); args.push(options.queueItemId); }
  if (options.reviewSessionId !== undefined) { conditions.push("review_session_id = ?"); args.push(options.reviewSessionId); }
  if (!options.includeArchived) conditions.push("archived_at IS NULL");
  const rows = db.query(
    `SELECT * FROM ask_conversations${conditions.length ? ` WHERE ${conditions.join(" AND ")}` : ""} ORDER BY updated_at DESC`,
  ).all(...args) as AskConversationRow[];
  return rows.map(conversationFromRow);
}

/** One review round has one live shared Copilot context. */
export function getActiveAskConversation(db: Database, reviewSessionId: number): AskConversation | undefined {
  return listAskConversations(db, { reviewSessionId })
    .find((conversation) => !conversation.context?.filePath);
}

export function archiveActiveAskConversations(db: Database, reviewSessionId: number): void {
  db.query(`UPDATE ask_conversations SET archived_at = ?, updated_at = ? WHERE review_session_id = ? AND archived_at IS NULL`)
    .run(Date.now(), Date.now(), reviewSessionId);
}

export function resumeAskConversation(db: Database, id: string): AskConversation | undefined {
  const conversation = getAskConversation(db, id);
  if (!conversation) return undefined;
  if (conversation.reviewSessionId !== null) archiveActiveAskConversations(db, conversation.reviewSessionId);
  db.query(`UPDATE ask_conversations SET archived_at = NULL, updated_at = ? WHERE id = ?`).run(Date.now(), id);
  return getAskConversation(db, id);
}

export function updateAskConversation(
  db: Database,
  id: string,
  input: { model?: string | null; reasoningEffort?: AskConversation["reasoningEffort"]; contextTier?: AskConversation["contextTier"]; copilotSessionId?: string | null },
): AskConversation | undefined {
  const current = getAskConversation(db, id);
  if (!current) return undefined;
  // `undefined` means leave the saved choice alone; explicit `null` clears a
  // previously selected optional setting when the reviewer switches models.
  const pick = <T>(key: keyof typeof input, fallback: T): T =>
    Object.hasOwn(input, key) ? input[key] as T : fallback;
  db.query(
    `UPDATE ask_conversations
       SET model = ?, reasoning_effort = ?, context_tier = ?, copilot_session_id = ?, updated_at = ?
     WHERE id = ?`,
  ).run(
    pick("model", current.model),
    pick("reasoningEffort", current.reasoningEffort),
    pick("contextTier", current.contextTier),
    pick("copilotSessionId", current.copilotSessionId),
    Date.now(),
    id,
  );
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
  input: { conversationId: string; role: AskMessageRole; body: string; pending?: boolean; location?: AskLocation },
): AskMessage {
  const result = db
    .query(
      `INSERT INTO ask_messages(conversation_id, role, body, pending, location_json, created_at)
       VALUES (?, ?, ?, ?, ?, ?)`,
    )
    .run(input.conversationId, input.role, input.body, input.pending ? 1 : 0, input.location ? JSON.stringify(input.location) : null, Date.now());
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

/**
 * A daemon or browser may disappear while an SDK turn is streaming. On the
 * next start, retain the transcript but make its interrupted state explicit
 * instead of showing a permanent spinner.
 */
export function settleInterruptedAskMessages(db: Database): number {
  const result = db.query(
    `UPDATE ask_messages
        SET pending = 0,
            body = CASE WHEN body = '' THEN 'Response interrupted before it completed.' ELSE body END
      WHERE pending = 1`,
  ).run();
  return result.changes;
}
