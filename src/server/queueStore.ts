import { randomUUID } from "node:crypto";
import type { Database } from "bun:sqlite";

export type QueueStatus = "queued" | "in_review" | "changes_requested" | "approved" | "completed";
export type QueueKind = "local" | "remote";

export interface QueueItem {
  id: string;
  title: string;
  body: string;
  workspacePath: string;
  kind: QueueKind;
  remoteUrl: string | null;
  status: QueueStatus;
  position: number;
  agentId: string | null;
  agentProvider: string | null;
  copilotSessionId: string | null;
  acpHost: string | null;
  acpPort: number | null;
  acpSessionId: string | null;
  agentKind: string | null;
  acpState: "unavailable" | "connecting" | "idle" | "busy" | "error";
  acpLastError: string | null;
  acpUpdatedAt: number | null;
  snapshotManifestPath: string | null;
  snapshotManifest: unknown | null;
  feedbackTarget: string | null;
  baseRef: string | null;
  /** Immutable capture of the terminal/cmux/agent origin at submission time. */
  provenance: unknown | null;
  /** Stable digest of the submitted Git worktree state, if known. */
  sourceFingerprint: string | null;
  /** Earlier queue item replaced by this source revision, if any. */
  supersedesId: string | null;
  /** Stable review-stream identity: PR URL, or workspace path plus topic. */
  identityKey: string | null;
  /** Human-selected stable local review topic. */
  reviewTopic: string | null;
  /** A removed item is retained for audit but no longer appears in queues. */
  removedAt: number | null;
  removedReason: string | null;
  decisionBody: string | null;
  createdAt: number;
  updatedAt: number;
}

interface QueueRow {
  id: string; title: string; body: string; workspace_path: string; kind: QueueKind; remote_url: string | null;
  status: QueueStatus; position: number; agent_id: string | null; agent_provider: string | null; copilot_session_id: string | null;
  acp_host: string | null; acp_port: number | null; acp_session_id: string | null; agent_kind: string | null;
  acp_state: "unavailable" | "connecting" | "idle" | "busy" | "error"; acp_last_error: string | null; acp_updated_at: number | null;
  snapshot_manifest_path: string | null; snapshot_manifest_json: string | null; feedback_target: string | null; decision_body: string | null;
  base_ref: string | null; provenance_json: string | null; source_fingerprint: string | null; supersedes_id: string | null;
  identity_key: string | null; review_topic: string | null; removed_at: number | null; removed_reason: string | null;
  created_at: number; updated_at: number;
}

function fromRow(row: QueueRow): QueueItem {
  return {
    id: row.id, title: row.title, body: row.body, workspacePath: row.workspace_path, kind: row.kind,
    remoteUrl: row.remote_url, status: row.status, position: row.position, agentId: row.agent_id,
    agentProvider: row.agent_provider, copilotSessionId: row.copilot_session_id,
    acpHost: row.acp_host, acpPort: row.acp_port, acpSessionId: row.acp_session_id,
    agentKind: row.agent_kind, acpState: row.acp_state ?? "unavailable", acpLastError: row.acp_last_error, acpUpdatedAt: row.acp_updated_at,
    snapshotManifestPath: row.snapshot_manifest_path,
    snapshotManifest: row.snapshot_manifest_json ? JSON.parse(row.snapshot_manifest_json) : null,
    feedbackTarget: row.feedback_target, baseRef: row.base_ref,
    provenance: row.provenance_json ? JSON.parse(row.provenance_json) : null,
    sourceFingerprint: row.source_fingerprint, supersedesId: row.supersedes_id,
    identityKey: row.identity_key, reviewTopic: row.review_topic, removedAt: row.removed_at, removedReason: row.removed_reason,
    decisionBody: row.decision_body, createdAt: row.created_at, updatedAt: row.updated_at,
  };
}

export function listQueue(db: Database, status?: QueueStatus, options: { includeHistory?: boolean; includeRemoved?: boolean } = {}): QueueItem[] {
  const conditions: string[] = [];
  const values: QueueStatus[] = [];
  if (!options.includeRemoved) conditions.push("removed_at IS NULL");
  if (status) { conditions.push("status = ?"); values.push(status); }
  else if (!options.includeHistory) conditions.push("status IN ('queued', 'in_review')");
  const where = conditions.length ? `WHERE ${conditions.join(" AND ")}` : "";
  const rows = db.query(`SELECT * FROM queue_items ${where} ORDER BY position, created_at`).all(...values) as QueueRow[];
  return rows.map(fromRow);
}

export interface EnqueueInput {
  title: string; body?: string; workspacePath: string; kind?: QueueKind; remoteUrl?: string;
  idempotentKey?: string; agentId?: string; agentProvider?: string; copilotSessionId?: string;
  acpHost?: string; acpPort?: number; acpSessionId?: string; agentKind?: string;
  snapshotManifestPath?: string; snapshotManifest?: unknown; feedbackTarget?: string;
  baseRef?: string; provenance?: unknown; sourceFingerprint?: string; supersedesId?: string;
  /** Stable topic for a local stream; remote identities are derived from the PR URL. */
  reviewTopic?: string; identityKey?: string;
}

function canonicalRemoteIdentity(remoteUrl: string): string {
  try {
    const url = new URL(remoteUrl);
    return `pr:${url.hostname.toLowerCase()}${url.pathname.replace(/\/$/, "").toLowerCase()}`;
  } catch { return `pr:${remoteUrl.trim().toLowerCase().replace(/\/$/, "")}`; }
}

export function queueIdentity(input: Pick<EnqueueInput, "workspacePath" | "kind" | "remoteUrl" | "reviewTopic" | "title" | "identityKey">): string {
  if (input.identityKey?.trim()) return input.identityKey.trim();
  if ((input.kind ?? "local") === "remote" && input.remoteUrl) return canonicalRemoteIdentity(input.remoteUrl);
  // A title is a convenient default, but --topic makes the identity explicit
  // and stable when a reviewer changes the title between revisions.
  return `local:${input.workspacePath}:${(input.reviewTopic?.trim() || input.title.trim()).toLowerCase()}`;
}

export interface RefreshRemoteInput extends EnqueueInput {
  remoteUrl: string;
  snapshotManifest?: unknown;
}

function remoteHead(manifest: unknown | undefined): string | undefined {
  if (!manifest || typeof manifest !== "object") return undefined;
  const pr = (manifest as { remotePullRequest?: unknown }).remotePullRequest;
  if (!pr || typeof pr !== "object") return undefined;
  const headSha = (pr as { headSha?: unknown }).headSha;
  return typeof headSha === "string" ? headSha : undefined;
}

export function enqueue(db: Database, input: EnqueueInput): { item: QueueItem; created: boolean } {
  if (input.idempotentKey) {
    const prior = db.query(`SELECT * FROM queue_items WHERE idempotent_key = ?`).get(input.idempotentKey) as QueueRow | null;
    if (prior) return { item: fromRow(prior), created: false };
  }
  const identityKey = queueIdentity(input);
  const predecessor = db.query(`SELECT * FROM queue_items WHERE identity_key = ? ORDER BY created_at DESC LIMIT 1`).get(identityKey) as QueueRow | null;
  // Prevent accidental duplicate submits of an unchanged active review, but
  // allow an explicit resubmit after a decision or removal to start a new
  // review round even when the source happens not to have changed.
  if (predecessor && !predecessor.removed_at && (predecessor.status === "queued" || predecessor.status === "in_review") && predecessor.source_fingerprint && predecessor.source_fingerprint === input.sourceFingerprint) {
    return { item: fromRow(predecessor), created: false };
  }
  const supersedesId = input.supersedesId ?? predecessor?.id;
  if (predecessor && !predecessor.removed_at && (predecessor.status === "queued" || predecessor.status === "in_review")) {
    decideQueueItem(db, predecessor.id, "completed", "Superseded by a newer submission for this review stream.");
  }
  const next = db.query(`SELECT COALESCE(MAX(position), 0) + 1 AS p FROM queue_items`).get() as { p: number };
  const now = Date.now();
  const id = randomUUID();
  db.query(`INSERT INTO queue_items (
    id,idempotent_key,title,body,workspace_path,kind,remote_url,status,position,agent_id,agent_provider,copilot_session_id,
    acp_host,acp_port,acp_session_id,agent_kind,acp_state,acp_updated_at,
    snapshot_manifest_path,snapshot_manifest_json,feedback_target,base_ref,provenance_json,source_fingerprint,supersedes_id,identity_key,review_topic,created_at,updated_at
  ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`).run(
    id, input.idempotentKey ?? null, input.title, input.body ?? "", input.workspacePath, input.kind ?? "local", input.remoteUrl ?? null,
    "queued", next.p, input.agentId ?? null, input.agentProvider ?? null, input.copilotSessionId ?? null,
    input.acpHost ?? null, input.acpPort ?? null, input.acpSessionId ?? null, input.agentKind ?? null,
    input.acpHost && input.acpPort && input.acpSessionId ? "idle" : "unavailable", input.acpHost && input.acpPort && input.acpSessionId ? now : null,
    input.snapshotManifestPath ?? null, input.snapshotManifest ? JSON.stringify(input.snapshotManifest) : null, input.feedbackTarget ?? null, input.baseRef ?? null,
    input.provenance ? JSON.stringify(input.provenance) : null, input.sourceFingerprint ?? null, supersedesId ?? null, identityKey, input.reviewTopic?.trim() || null, now, now,
  );
  return { item: getQueueItem(db, id)!, created: true };
}

/**
 * A PR URL identifies a review stream. Re-submitting it replaces its snapshot
 * and cached worktree details instead of creating duplicate queue rows. A new
 * head SHA requeues the review while keeping its earlier feedback visible.
 */
export function refreshRemoteQueue(db: Database, input: RefreshRemoteInput): { item: QueueItem; created: boolean; headChanged: boolean } {
  const prior = db.query(`SELECT * FROM queue_items WHERE kind = 'remote' AND remote_url = ? ORDER BY updated_at DESC LIMIT 1`).get(input.remoteUrl) as QueueRow | null;
  if (!prior) {
    const result = enqueue(db, { ...input, kind: "remote", identityKey: queueIdentity({ ...input, kind: "remote" }) });
    return { ...result, headChanged: false };
  }
  const before = remoteHead(prior.snapshot_manifest_json ? JSON.parse(prior.snapshot_manifest_json) : undefined);
  const after = remoteHead(input.snapshotManifest);
  const headChanged = !!before && !!after && before !== after;
  // An explicit re-submission of a terminal/removed PR is a fresh review
  // round even when GitHub's head did not move.  Requeue remains available
  // for the distinct case where the exact prior snapshot needs another pass.
  if (headChanged || prior.removed_at || (prior.status !== "queued" && prior.status !== "in_review")) {
    if (!prior.removed_at && (prior.status === "queued" || prior.status === "in_review")) {
      decideQueueItem(db, prior.id, "completed", "Superseded by a newer pull-request head.");
    }
    const result = enqueue(db, { ...input, kind: "remote", identityKey: prior.identity_key ?? canonicalRemoteIdentity(input.remoteUrl), supersedesId: prior.id });
    return { ...result, created: true, headChanged };
  }
  const now = Date.now();
  db.query(`UPDATE queue_items SET
    title=?, body=?, workspace_path=?, kind='remote', remote_url=?,
    agent_id=?, agent_provider=?, copilot_session_id=?,
    acp_host=?, acp_port=?, acp_session_id=?, agent_kind=?, acp_state=?, acp_last_error=NULL, acp_updated_at=?,
    snapshot_manifest_path=?, snapshot_manifest_json=?, base_ref=?,
    provenance_json=?, source_fingerprint=?,
    status=?, decision_body=?, updated_at=?
    WHERE id=?`).run(
    input.title, input.body ?? "", input.workspacePath, input.remoteUrl,
    input.agentId ?? null, input.agentProvider ?? null, input.copilotSessionId ?? null,
    input.acpHost ?? null, input.acpPort ?? null, input.acpSessionId ?? null, input.agentKind ?? null,
    input.acpHost && input.acpPort && input.acpSessionId ? "idle" : "unavailable", input.acpHost && input.acpPort && input.acpSessionId ? now : null,
    input.snapshotManifestPath ?? null, input.snapshotManifest ? JSON.stringify(input.snapshotManifest) : null,
    input.baseRef ?? null, input.provenance ? JSON.stringify(input.provenance) : null, input.sourceFingerprint ?? null,
    headChanged ? "queued" : prior.status, headChanged ? null : prior.decision_body, now, prior.id,
  );
  return { item: getQueueItem(db, prior.id)!, created: false, headChanged };
}

export function getQueueItem(db: Database, id: string): QueueItem | undefined {
  const row = db.query(`SELECT * FROM queue_items WHERE id = ?`).get(id) as QueueRow | null;
  return row ? fromRow(row) : undefined;
}

export function openNext(db: Database): QueueItem | undefined {
  const now = Date.now();
  const row = db.query(`SELECT * FROM queue_items WHERE removed_at IS NULL AND status = 'queued' ORDER BY position, created_at LIMIT 1`).get() as QueueRow | null;
  if (!row) return undefined;
  db.query(`UPDATE queue_items SET status = 'in_review', updated_at = ? WHERE id = ?`).run(now, row.id);
  return getQueueItem(db, row.id);
}

/** Claim one explicit queue item for review without silently selecting another. */
export function openQueueItem(db: Database, id: string): QueueItem | undefined {
  const item = getQueueItem(db, id);
  if (!item || item.removedAt) return undefined;
  if (item.status === "queued") {
    db.query(`UPDATE queue_items SET status='in_review', updated_at=? WHERE id=?`).run(Date.now(), id);
    return getQueueItem(db, id);
  }
  return item.status === "in_review" ? item : undefined;
}

/** Return the same immutable snapshot to the end of the actionable queue. */
export function requeueQueueItem(db: Database, id: string): QueueItem | undefined {
  const item = getQueueItem(db, id);
  if (!item) return undefined;
  const next = db.query(`SELECT COALESCE(MAX(position), 0) + 1 AS p FROM queue_items`).get() as { p: number };
  const now = Date.now();
  db.transaction(() => {
    db.query(`UPDATE queue_items SET status='queued', position=?, decision_body=NULL, updated_at=? WHERE id=?`)
      .run(next.p, now, id);
    db.query(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`)
      .run(id, "requeued", "Requeued by reviewer.", now);
  })();
  return getQueueItem(db, id);
}

export function reorderQueue(db: Database, id: string, position: number): QueueItem | undefined {
  const target = getQueueItem(db, id);
  if (!target) return undefined;
  const siblings = listQueue(db).filter((item) => item.id !== id);
  const clamped = Math.max(1, Math.min(Math.trunc(position), siblings.length + 1));
  siblings.splice(clamped - 1, 0, target);
  const now = Date.now();
  db.transaction(() => siblings.forEach((item, index) => db.query(`UPDATE queue_items SET position = ?, updated_at = ? WHERE id = ?`).run(index + 1, now, item.id)))();
  return getQueueItem(db, id);
}

export function decideQueueItem(db: Database, id: string, decision: "approved" | "changes_requested" | "completed", body?: string): QueueItem | undefined {
  const item = getQueueItem(db, id);
  if (!item) return undefined;
  const now = Date.now();
  db.transaction(() => {
    db.query(`UPDATE queue_items SET status = ?, decision_body = ?, updated_at = ? WHERE id = ?`).run(decision, body ?? null, now, id);
    db.query(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`)
      .run(id, decision, body ?? null, now);
  })();
  return getQueueItem(db, id);
}

/** Remove a snapshot from the active queue without pretending it was reviewed. */
export function removeQueueItem(db: Database, id: string, reason = "Removed from queue without review."): QueueItem | undefined {
  const item = getQueueItem(db, id);
  if (!item) return undefined;
  const now = Date.now();
  db.transaction(() => {
    db.query(`UPDATE queue_items SET removed_at=?,removed_reason=?,updated_at=? WHERE id=?`).run(now, reason, now, id);
    db.query(`INSERT INTO queue_decisions(queue_item_id,status,body,created_at) VALUES(?,?,?,?)`)
      .run(id, "completed", reason, now);
  })();
  return getQueueItem(db, id);
}

export function addFeedback(db: Database, id: string, body: string, path?: string, line?: number): void {
  if (!getQueueItem(db, id)) throw new Error(`Unknown queue item: ${id}`);
  db.query(`INSERT INTO queue_feedback(queue_item_id, body, path, line, created_at) VALUES(?,?,?,?,?)`).run(id, body, path ?? null, line ?? null, Date.now());
  db.query(`UPDATE queue_items SET updated_at = ? WHERE id = ?`).run(Date.now(), id);
}

export interface QueueFeedback {
  id: number;
  body: string;
  path: string | null;
  line: number | null;
  createdAt: number;
  deliveredAt: number | null;
}

/** A durable lifecycle event; feedback delivery remains on each feedback row. */
export interface QueueDecision {
  id: number;
  queueItemId: string;
  status: Extract<QueueStatus, "approved" | "changes_requested" | "completed"> | "requeued";
  body: string | null;
  createdAt: number;
}

export function decisionHistoryForItem(db: Database, id: string): QueueDecision[] {
  return (db.query(`SELECT id,queue_item_id,status,body,created_at FROM queue_decisions WHERE queue_item_id = ? ORDER BY created_at, id`).all(id) as {
    id: number; queue_item_id: string; status: QueueDecision["status"]; body: string | null; created_at: number;
  }[]).map((row) => ({
    id: row.id,
    queueItemId: row.queue_item_id,
    status: row.status,
    body: row.body,
    createdAt: row.created_at,
  }));
}

export function feedbackForItem(db: Database, id: string, options: { undeliveredOnly?: boolean } = {}): QueueFeedback[] {
  const undelivered = options.undeliveredOnly ? " AND delivered_at IS NULL" : "";
  return (db.query(`SELECT id, body, path, line, created_at, delivered_at FROM queue_feedback WHERE queue_item_id = ?${undelivered} ORDER BY id`).all(id) as { id:number;body:string;path:string|null;line:number|null;created_at:number;delivered_at:number|null }[])
    .map((row) => ({ id: row.id, body: row.body, path: row.path, line: row.line, createdAt: row.created_at, deliveredAt: row.delivered_at }));
}

/** Mark feedback only after ACP accepts the combined prompt. */
export function markFeedbackDelivered(db: Database, id: string, feedbackIds: number[]): void {
  if (!feedbackIds.length) return;
  const placeholders = feedbackIds.map(() => "?").join(",");
  db.query(`UPDATE queue_feedback SET delivered_at = ? WHERE queue_item_id = ? AND id IN (${placeholders})`).run(Date.now(), id, ...feedbackIds);
  db.query(`UPDATE queue_items SET updated_at = ? WHERE id = ?`).run(Date.now(), id);
}

export function updateAcpState(
  db: Database,
  id: string,
  state: QueueItem["acpState"],
  lastError: string | null = null,
): QueueItem | undefined {
  const now = Date.now();
  db.query(`UPDATE queue_items SET acp_state=?,acp_last_error=?,acp_updated_at=?,updated_at=? WHERE id=?`).run(state, lastError, now, now, id);
  return getQueueItem(db, id);
}
