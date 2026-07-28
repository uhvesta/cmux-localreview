import type { Database } from "bun:sqlite";

import {
  createCmuxService,
  createDryRunConnector,
  createSocketConnector,
} from "../../vendor/cmux-hub/cmux.ts";

export type AgentStatus = "connected" | "reconnecting" | "disconnected";

export interface AgentConnectionMetadata {
  /** The cmux surface that owns this agent's working context. */
  surfaceId?: string;
  lastSeenAt?: number;
  lastError?: string;
  reconnectAttempts?: number;
  [key: string]: unknown;
}

export interface RegisteredAgent {
  id: string;
  provider: string;
  command: string | null;
  workspacePath: string | null;
  reviewSessionId: string | null;
  status: string;
  metadata: AgentConnectionMetadata;
  updatedAt: number;
}

type AgentRow = {
  id: string;
  provider: string;
  command: string | null;
  workspace_path: string | null;
  review_session_id: string | null;
  status: string;
  metadata_json: string | null;
  updated_at: number;
};

function metadataFromJson(input: string | null): AgentConnectionMetadata {
  if (!input) return {};
  try {
    const parsed = JSON.parse(input);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? parsed as AgentConnectionMetadata
      : {};
  } catch {
    return {};
  }
}

function fromRow(row: AgentRow): RegisteredAgent {
  return {
    id: row.id,
    provider: row.provider,
    command: row.command,
    workspacePath: row.workspace_path,
    reviewSessionId: row.review_session_id,
    status: row.status,
    metadata: metadataFromJson(row.metadata_json),
    updatedAt: row.updated_at,
  };
}

export class AgentRoutingError extends Error {
  constructor(
    message: string,
    readonly statusCode: number,
  ) {
    super(message);
    this.name = "AgentRoutingError";
  }
}

export function listRegisteredAgents(db: Database): RegisteredAgent[] {
  return (db.query(`SELECT id,provider,command,workspace_path,review_session_id,status,metadata_json,updated_at FROM agent_registry ORDER BY updated_at DESC`).all() as AgentRow[])
    .map(fromRow);
}

export function getRegisteredAgent(db: Database, id: string): RegisteredAgent | undefined {
  const row = db.query(`SELECT id,provider,command,workspace_path,review_session_id,status,metadata_json,updated_at FROM agent_registry WHERE id = ?`).get(id) as AgentRow | null;
  return row ? fromRow(row) : undefined;
}

export function registerAgent(
  db: Database,
  input: {
    id: string;
    provider: string;
    command?: string;
    workspacePath?: string;
    reviewSessionId?: string;
    status?: AgentStatus;
    metadata?: Record<string, unknown>;
    surfaceId?: string;
  },
): RegisteredAgent {
  const existing = getRegisteredAgent(db, input.id);
  const metadata = { ...(existing?.metadata ?? {}), ...(input.metadata ?? {}) } as AgentConnectionMetadata;
  if (input.surfaceId !== undefined) metadata.surfaceId = input.surfaceId;
  // A registration/heartbeat is proof that the source agent is available;
  // do not retain a previous delivery failure as its current state.
  metadata.lastSeenAt = Date.now();
  if ((input.status ?? "connected") === "connected") delete metadata.lastError;
  const now = Date.now();
  db.query(`INSERT INTO agent_registry(id,provider,command,workspace_path,review_session_id,status,metadata_json,updated_at)
    VALUES(?,?,?,?,?,?,?,?)
    ON CONFLICT(id) DO UPDATE SET
      provider=excluded.provider, command=excluded.command,
      workspace_path=excluded.workspace_path, review_session_id=excluded.review_session_id,
      status=excluded.status, metadata_json=excluded.metadata_json, updated_at=excluded.updated_at`).run(
    input.id,
    input.provider,
    input.command ?? existing?.command ?? null,
    input.workspacePath ?? existing?.workspacePath ?? null,
    input.reviewSessionId ?? existing?.reviewSessionId ?? null,
    input.status ?? "connected",
    JSON.stringify(metadata),
    now,
  );
  return getRegisteredAgent(db, input.id)!;
}

function updateConnection(db: Database, agent: RegisteredAgent, status: AgentStatus, metadata: AgentConnectionMetadata): RegisteredAgent {
  db.query(`UPDATE agent_registry SET status = ?, metadata_json = ?, updated_at = ? WHERE id = ?`)
    .run(status, JSON.stringify(metadata), Date.now(), agent.id);
  return getRegisteredAgent(db, agent.id)!;
}

/**
 * Resolves exactly one registered terminal agent. It deliberately never
 * returns an undefined surface id: cmux would otherwise inject into whatever
 * terminal happens to be focused, which is unsafe for review feedback.
 */
export function resolveTerminalTarget(db: Database, agentId: string | undefined, workspacePath: string): RegisteredAgent {
  if (!agentId) {
    throw new AgentRoutingError("A terminal /btw question requires a registered target agent. Select the originating agent first.", 409);
  }
  const agent = getRegisteredAgent(db, agentId);
  if (!agent) throw new AgentRoutingError(`Unknown target agent: ${agentId}`, 404);
  if (agent.workspacePath && agent.workspacePath !== workspacePath) {
    throw new AgentRoutingError(`Agent ${agentId} belongs to ${agent.workspacePath}, not this review workspace.`, 409);
  }
  if (agent.status !== "connected") {
    throw new AgentRoutingError(`Agent ${agentId} is ${agent.status}. Reconnect it before sending /btw.`, 409);
  }
  if (typeof agent.metadata.surfaceId !== "string" || !agent.metadata.surfaceId.trim()) {
    throw new AgentRoutingError(`Agent ${agentId} has no cmux surface binding. Register it with surfaceId before sending /btw.`, 409);
  }
  return agent;
}

export function markAgentDelivered(db: Database, agentId: string): void {
  const agent = getRegisteredAgent(db, agentId);
  if (!agent) return;
  const metadata = { ...agent.metadata, lastSeenAt: Date.now() };
  delete metadata.lastError;
  updateConnection(db, agent, "connected", metadata);
}

export function markAgentDeliveryFailed(db: Database, agentId: string, error: unknown): void {
  const agent = getRegisteredAgent(db, agentId);
  if (!agent) return;
  const metadata = {
    ...agent.metadata,
    lastError: error instanceof Error ? error.message : String(error),
    reconnectAttempts: (typeof agent.metadata.reconnectAttempts === "number" ? agent.metadata.reconnectAttempts : 0) + 1,
  };
  updateConnection(db, agent, "disconnected", metadata);
}

/**
 * Reconnect means verify that cmux is reachable and that the registered
 * surface still exists. It does not send a prompt or mutate the terminal.
 */
export async function reconnectAgent(db: Database, agentId: string, dryRun = false): Promise<RegisteredAgent> {
  const agent = getRegisteredAgent(db, agentId);
  if (!agent) throw new AgentRoutingError(`Unknown agent: ${agentId}`, 404);
  const surfaceId = agent.metadata.surfaceId;
  if (typeof surfaceId !== "string" || !surfaceId.trim()) {
    throw new AgentRoutingError(`Agent ${agentId} has no cmux surface binding.`, 409);
  }
  updateConnection(db, agent, "reconnecting", { ...agent.metadata });
  try {
    const cmux = createCmuxService(dryRun ? createDryRunConnector() : createSocketConnector());
    const response = await cmux.listSurfaces();
    const surfaces = Array.isArray(response)
      ? response
      : response && typeof response === "object" && Array.isArray((response as { surfaces?: unknown[] }).surfaces)
        ? (response as { surfaces: unknown[] }).surfaces
        : undefined;
    // cmux versions have returned both a bare array and { surfaces }. If a
    // future version returns an opaque success payload, reachability itself is
    // still meaningful; only reject an explicit list that lacks our surface.
    if (surfaces && !surfaces.some((surface) => {
      if (typeof surface === "string") return surface === surfaceId;
      return !!surface && typeof surface === "object" &&
        ((surface as { id?: unknown }).id === surfaceId || (surface as { surface_id?: unknown }).surface_id === surfaceId);
    })) {
      throw new Error(`cmux surface ${surfaceId} is no longer available`);
    }
    const fresh = getRegisteredAgent(db, agentId)!;
    return updateConnection(db, fresh, "connected", { ...fresh.metadata, lastSeenAt: Date.now(), reconnectAttempts: 0, lastError: undefined });
  } catch (error) {
    markAgentDeliveryFailed(db, agentId, error);
    throw new AgentRoutingError(`Could not reconnect agent ${agentId}: ${error instanceof Error ? error.message : String(error)}`, 502);
  }
}
