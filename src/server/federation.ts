import { createServer } from "node:net";
import { randomUUID } from "node:crypto";
import type { Database } from "bun:sqlite";

export interface FederationNode {
  id: string;
  label: string;
  sshTarget: string;
  remotePort: number;
  token: string;
  enabled: boolean;
  lastConnectedAt: number | null;
  lastError: string | null;
  createdAt: number;
  updatedAt: number;
}

interface FederationRow {
  id: string; label: string; ssh_target: string; remote_port: number; token: string; enabled: number;
  last_connected_at: number | null; last_error: string | null; created_at: number; updated_at: number;
}

function rowToNode(row: FederationRow): FederationNode {
  return { id: row.id, label: row.label, sshTarget: row.ssh_target, remotePort: row.remote_port, token: row.token, enabled: !!row.enabled, lastConnectedAt: row.last_connected_at, lastError: row.last_error, createdAt: row.created_at, updatedAt: row.updated_at };
}

export function listFederationNodes(db: Database): FederationNode[] {
  return (db.query(`SELECT * FROM federation_nodes ORDER BY label,id`).all() as FederationRow[]).map(rowToNode);
}

export function getFederationNode(db: Database, id: string): FederationNode | undefined {
  const row = db.query(`SELECT * FROM federation_nodes WHERE id=?`).get(id) as FederationRow | null;
  return row ? rowToNode(row) : undefined;
}

export function upsertFederationNode(db: Database, input: { id?: string; label: string; sshTarget: string; remotePort: number; token: string; enabled?: boolean }): FederationNode {
  if (!input.label || !input.sshTarget || !input.token || !Number.isInteger(input.remotePort) || input.remotePort < 1 || input.remotePort > 65_535) throw new Error("label, sshTarget, bearer token, and a valid remotePort are required");
  const id = input.id ?? randomUUID(); const now = Date.now();
  db.query(`INSERT INTO federation_nodes(id,label,ssh_target,remote_port,token,enabled,created_at,updated_at)
    VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET label=excluded.label,ssh_target=excluded.ssh_target,remote_port=excluded.remote_port,token=excluded.token,enabled=excluded.enabled,updated_at=excluded.updated_at`)
    .run(id, input.label, input.sshTarget, input.remotePort, input.token, input.enabled === false ? 0 : 1, now, now);
  return getFederationNode(db, id)!;
}

export function removeFederationNode(db: Database, id: string): boolean {
  return db.query(`DELETE FROM federation_nodes WHERE id=?`).run(id).changes > 0;
}

/** Persisted disconnect: disabled nodes are never lazily re-opened by queue aggregation. */
export function setFederationNodeEnabled(db: Database, id: string, enabled: boolean): FederationNode | undefined {
  const result = db.query(`UPDATE federation_nodes SET enabled=?,updated_at=? WHERE id=?`).run(enabled ? 1 : 0, Date.now(), id);
  return result.changes > 0 ? getFederationNode(db, id) : undefined;
}

function reservePort(): Promise<number> {
  return new Promise((resolvePromise, reject) => {
    const socket = createServer();
    socket.once("error", reject);
    socket.listen(0, "127.0.0.1", () => {
      const address = socket.address();
      if (!address || typeof address === "string") { socket.close(); reject(new Error("Could not reserve a loopback port")); return; }
      socket.close((error) => error ? reject(error) : resolvePromise(address.port));
    });
  });
}

interface LiveTunnel { port: number; process: ReturnType<typeof Bun.spawn>; ready: Promise<void>; connected: boolean; }

export interface FederationNodeRuntimeStatus {
  id: string;
  state: "disconnected" | "connecting" | "connected" | "error" | "disabled";
  localPort: number | null;
  cachedResponses: number;
  lastConnectedAt: number | null;
  lastError: string | null;
}

/**
 * A node is not contacted until an aggregate/proxy request needs it.  The SSH
 * process exposes the remote loopback-only daemon through another loopback
 * port; neither daemon becomes publicly reachable.
 */
export class FederationTunnelManager {
  private readonly tunnels = new Map<string, LiveTunnel>();
  private readonly responseCache = new Map<string, { expiresAt: number; value: unknown }>();

  constructor(
    private readonly db: Database,
    // Injectable for the loopback integration fixture. Production always uses
    // the system `ssh` binary and never accepts this value over HTTP.
    private readonly sshCommand = "ssh",
  ) {}

  /** Runtime-only health; credentials are intentionally never exposed. */
  status(id: string): FederationNodeRuntimeStatus {
    const node = getFederationNode(this.db, id);
    if (!node) throw new Error(`Unknown federation node: ${id}`);
    if (!node.enabled) return { id, state: "disabled", localPort: null, cachedResponses: 0, lastConnectedAt: node.lastConnectedAt, lastError: node.lastError };
    const live = this.tunnels.get(id);
    const cachedResponses = [...this.responseCache.keys()].filter((key) => key.startsWith(`${id}:`)).length;
    if (!live) {
      return { id, state: node.lastError ? "error" : "disconnected", localPort: null, cachedResponses, lastConnectedAt: node.lastConnectedAt, lastError: node.lastError };
    }
    if (live.process.exitCode !== null) {
      return { id, state: "error", localPort: null, cachedResponses, lastConnectedAt: node.lastConnectedAt, lastError: node.lastError ?? `SSH tunnel exited with code ${live.process.exitCode}` };
    }
    return { id, state: live.connected ? "connected" : "connecting", localPort: live.port, cachedResponses, lastConnectedAt: node.lastConnectedAt, lastError: node.lastError };
  }

  async connect(id: string): Promise<{ port: number }> {
    const existing = this.tunnels.get(id);
    if (existing && existing.process.exitCode === null) { await existing.ready; return { port: existing.port }; }
    this.stop(id);
    const node = getFederationNode(this.db, id);
    if (!node) throw new Error(`Unknown federation node: ${id}`);
    if (!node.enabled) throw new Error(`Federation node ${node.label} is disabled`);
    const port = await reservePort();
    const child = Bun.spawn({ cmd: [this.sshCommand, "-N", "-o", "ExitOnForwardFailure=yes", "-o", "BatchMode=yes", "-L", `127.0.0.1:${port}:127.0.0.1:${node.remotePort}`, node.sshTarget], stdin: "ignore", stdout: "pipe", stderr: "pipe" });
    const live: LiveTunnel = { port, process: child, connected: false, ready: Promise.resolve() };
    const ready = this.waitForHealth(id, port, node.token).then(() => {
      this.db.query(`UPDATE federation_nodes SET last_connected_at=?,last_error=NULL,updated_at=? WHERE id=?`).run(Date.now(), Date.now(), id);
      live.connected = true;
    }).catch(async (error) => {
      this.db.query(`UPDATE federation_nodes SET last_error=?,updated_at=? WHERE id=?`).run(String(error), Date.now(), id);
      child.kill(); this.tunnels.delete(id); throw error;
    });
    live.ready = ready;
    this.tunnels.set(id, live);
    await ready;
    return { port };
  }

  private async waitForHealth(id: string, port: number, token: string): Promise<void> {
    const deadline = Date.now() + 8_000;
    while (Date.now() < deadline) {
      try { const response = await fetch(`http://127.0.0.1:${port}/health`, { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(750) }); if (response.ok) return; } catch { /* tunnel still negotiating */ }
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 125));
    }
    throw new Error(`Timed out opening SSH tunnel for federation node ${id}`);
  }

  async request<T>(id: string, path: string, init: RequestInit = {}, cacheMs = 1_000): Promise<T> {
    const cacheKey = `${id}:${path}`;
    if ((init.method ?? "GET") === "GET") {
      const cached = this.responseCache.get(cacheKey);
      if (cached && cached.expiresAt > Date.now()) return cached.value as T;
    }
    const node = getFederationNode(this.db, id);
    if (!node) throw new Error(`Unknown federation node: ${id}`);
    const { port } = await this.connect(id);
    const headers = new Headers(init.headers); headers.set("Authorization", `Bearer ${node.token}`);
    const response = await fetch(`http://127.0.0.1:${port}${path}`, { ...init, headers, signal: init.signal ?? AbortSignal.timeout(10_000) });
    const raw = await response.text();
    let body: unknown = raw; try { body = raw ? JSON.parse(raw) : undefined; } catch { /* non-json response */ }
    if (!response.ok) throw new Error(`Node ${node.label} returned ${response.status}: ${typeof body === "object" && body && "error" in body ? String((body as { error: unknown }).error) : raw}`);
    if ((init.method ?? "GET") === "GET") this.responseCache.set(cacheKey, { expiresAt: Date.now() + cacheMs, value: body });
    return body as T;
  }

  stop(id: string): void {
    const live = this.tunnels.get(id); if (!live) return;
    live.process.kill(); this.tunnels.delete(id);
    for (const key of this.responseCache.keys()) if (key.startsWith(`${id}:`)) this.responseCache.delete(key);
  }
  stopAll(): void { for (const id of [...this.tunnels.keys()]) this.stop(id); }
}
