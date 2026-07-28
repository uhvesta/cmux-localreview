#!/usr/bin/env bun
import { createHash } from "node:crypto";
import { existsSync, mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import express, { type Express, type Request, type Response } from "express";

import { openDb } from "./server/db.ts";
import { daemonDbPath, ensureDaemonDirectories, newDaemonToken, writeDiscovery } from "./server/daemonPaths.ts";
import { runCommand } from "./server/gitExec.ts";
import { createWorkspaceSnapshot } from "./server/snapshots.ts";
import { addFeedback, decideQueueItem, enqueue, feedbackForItem, getQueueItem, listQueue, openNext, reorderQueue, type QueueStatus } from "./server/queueStore.ts";
import { buildWorkspaceApp, type WorkspaceApp } from "./server/app.ts";
import { WsHub } from "./server/wsHub.ts";
import { exportReviewPackage, materializeReviewPackage } from "./server/reviewPackage.ts";
import { submitRemoteDecision } from "./server/remotePr.ts";
import { FederationTunnelManager, getFederationNode, listFederationNodes, removeFederationNode, upsertFederationNode } from "./server/federation.ts";

const VERSION = "0.2.0";

interface ActiveReview {
  rootPath: string;
  workspace: WorkspaceApp;
  stop: () => Promise<void>;
}

export interface GlobalDaemonOptions { port?: number; host?: string; token?: string; open?: boolean; }

function safeWorkspace(input: unknown): string {
  if (typeof input !== "string" || !input.trim()) throw new Error("workspacePath is required");
  return resolve(input);
}

function bodyString(value: unknown): string | undefined { return typeof value === "string" ? value : undefined; }

function requireAuth(token: string) {
  return (req: Request, res: Response, next: () => void) => {
    if (req.path === "/health") return next();
    const header = req.header("authorization");
    if (header !== `Bearer ${token}`) { res.status(401).json({ error: "Bearer token required" }); return; }
    next();
  };
}

async function prepareRemoteWorkspace(remoteUrl: string): Promise<string> {
  const key = createHash("sha256").update(remoteUrl).digest("hex");
  const mirror = join(homedir(), ".cache", "cmux-localreview", "mirrors", `${key}.git`);
  const worktree = join(homedir(), ".cache", "cmux-localreview", "worktrees", key, `${Date.now()}`);
  mkdirSync(join(homedir(), ".cache", "cmux-localreview", "mirrors"), { recursive: true, mode: 0o700 });
  mkdirSync(join(homedir(), ".cache", "cmux-localreview", "worktrees", key), { recursive: true, mode: 0o700 });
  if (!existsSync(mirror)) {
    const clone = await runCommand(["git", "clone", "--mirror", remoteUrl, mirror]);
    if (clone.exitCode !== 0) throw new Error(`Unable to mirror remote PR repository: ${clone.stderr.trim()}`);
  } else {
    const fetch = await runCommand(["git", "--git-dir", mirror, "fetch", "--prune", "origin"]);
    if (fetch.exitCode !== 0) throw new Error(`Unable to update remote mirror: ${fetch.stderr.trim()}`);
  }
  const add = await runCommand(["git", "--git-dir", mirror, "worktree", "add", "--detach", worktree, "HEAD"]);
  if (add.exitCode !== 0) throw new Error(`Unable to create isolated remote worktree: ${add.stderr.trim()}`);
  return worktree;
}

/** Starts the authenticated, persistent loopback control plane. */
export async function startGlobalDaemon(options: GlobalDaemonOptions = {}) {
  ensureDaemonDirectories();
  const db = openDb(daemonDbPath());
  const federation = new FederationTunnelManager(db);
  const token = options.token ?? newDaemonToken();
  const app = express();
  app.use(express.json({ limit: "2mb" }));
  app.get("/health", (_req, res) => res.json({ ok: true, version: VERSION, pid: process.pid }));
  const api = express.Router();
  // Review endpoints (/api/repos, comments, /ask, UI state, etc.) are served
  // by the active workspace app and deliberately remain local-browser usable.
  // Only the daemon control plane is bearer-protected.
  api.use((req, res, next) => {
    if (/^\/(workspaces|queue|agents|packages|federation)(?:\/|$)/.test(req.path)) return requireAuth(token)(req, res, next);
    next();
  });
  app.use("/api", api);
  let active: ActiveReview | undefined;
  let hub: WsHub | undefined;

  const activate = async (workspacePath: string, base?: string) => {
    if (active?.rootPath === workspacePath) return active;
    if (active) await active.stop();
    const workspaceDb = openDb(join(homedir(), ".local", "share", "cmux-localreview", `${createHash("sha256").update(workspacePath).digest("hex").slice(0, 16)}.db`));
    workspaceDb.query(`INSERT INTO workspace(id,root_path,created_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET root_path=excluded.root_path`).run(workspacePath, Date.now());
    const workspace = await buildWorkspaceApp({ workspaceRoot: workspacePath, db: workspaceDb, base });
    const startedHub = hub;
    let cleanupBtw: (() => void) | undefined;
    if (startedHub) {
      await workspace.startWatchers(startedHub);
      const managed = workspace.startBtw(startedHub, "claude");
      cleanupBtw = () => { managed.btwManager.disposeAll(); managed.terminalBtw.stop(); };
    }
    active = { rootPath: workspacePath, workspace, stop: async () => { cleanupBtw?.(); await workspace.stopAsk(); await workspace.stopWatchers(); workspaceDb.close(); } };
    return active;
  };

  api.get("/workspaces", (_req, res) => {
    const workspaces = db.query(`SELECT id,root_path,label,last_opened_at,active FROM workspace_registry ORDER BY active DESC,last_opened_at DESC`).all().map((row: any) => ({ id: row.id, path: row.root_path, label: row.label, lastOpenedAt: row.last_opened_at, active: !!row.active }));
    res.json({ activeWorkspace: active?.rootPath ?? null, workspaces });
  });
  api.post("/workspaces", async (req, res) => {
    try {
      const rootPath = safeWorkspace(req.body?.workspacePath ?? req.body?.path);
      const label = bodyString(req.body?.label);
      db.query(`INSERT INTO workspace_registry(root_path,label,last_opened_at,active) VALUES(?,?,?,0) ON CONFLICT(root_path) DO UPDATE SET label=COALESCE(excluded.label,workspace_registry.label),last_opened_at=excluded.last_opened_at`).run(rootPath, label ?? null, Date.now());
      res.status(201).json({ workspacePath: rootPath });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.post("/workspaces/open", async (req, res) => {
    try {
      const rootPath = safeWorkspace(req.body?.workspacePath ?? req.body?.path);
      const review = await activate(rootPath, bodyString(req.body?.base));
      db.transaction(() => {
        db.query(`UPDATE workspace_registry SET active=0`).run();
        db.query(`INSERT INTO workspace_registry(root_path,last_opened_at,active) VALUES(?,?,1) ON CONFLICT(root_path) DO UPDATE SET last_opened_at=excluded.last_opened_at,active=1`).run(rootPath, Date.now());
      })();
      res.json({ workspacePath: rootPath, repos: review.workspace.repos.map((repo) => ({ id: repo.repoId, path: repo.repo.workspaceRelativePath })), reviewUrl: "/" });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });

  api.get("/queue", (req, res) => res.json({ items: listQueue(db, bodyString(req.query.status) as QueueStatus | undefined) }));
  api.post("/queue", async (req, res) => {
    try {
      const remoteUrl = bodyString(req.body?.remoteUrl);
      let workspacePath = bodyString(req.body?.workspacePath) ?? bodyString(req.body?.cwd);
      const kind = remoteUrl ? "remote" : "local";
      if (remoteUrl && !workspacePath) workspacePath = await prepareRemoteWorkspace(remoteUrl);
      const resolvedWorkspace = safeWorkspace(workspacePath);
      const title = bodyString(req.body?.title) ?? (remoteUrl ? `Review ${remoteUrl}` : `Review ${resolvedWorkspace}`);
      let snapshotManifestPath: string | undefined;
      let snapshotManifest: unknown;
      if (kind === "local" || req.body?.snapshot !== false) {
        const snapshot = await createWorkspaceSnapshot(resolvedWorkspace, undefined, bodyString(req.body?.base));
        snapshotManifestPath = snapshot.manifestPath;
        snapshotManifest = snapshot.manifest;
      }
      const result = enqueue(db, { title, body: bodyString(req.body?.body), workspacePath: resolvedWorkspace, kind, remoteUrl, idempotentKey: bodyString(req.body?.idempotentKey), agentId: bodyString(req.body?.agentId), agentProvider: bodyString(req.body?.agentProvider), copilotSessionId: bodyString(req.body?.copilotSessionId), snapshotManifestPath, snapshotManifest, feedbackTarget: bodyString(req.body?.feedbackTarget), baseRef: bodyString(req.body?.base) });
      res.status(result.created ? 201 : 200).json(result);
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.post("/queue/open-next", async (_req, res) => {
    const item = openNext(db);
    if (!item) { res.status(404).json({ error: "No queued review items" }); return; }
    try { await activate(item.workspacePath, item.baseRef ?? undefined); res.json({ item, reviewUrl: "/" }); } catch (error) { res.status(500).json({ error: String(error), item }); }
  });
  api.post("/queue/:id/reorder", (req, res) => { const item = reorderQueue(db, req.params.id, Number(req.body?.position)); item ? res.json({ item }) : res.status(404).json({ error: "Unknown queue item" }); });
  api.post("/queue/:id/decision", async (req, res) => {
    const decision = bodyString(req.body?.decision);
    if (decision !== "approved" && decision !== "changes_requested" && decision !== "completed") { res.status(400).json({ error: "decision must be approved, changes_requested, or completed" }); return; }
    const item = decideQueueItem(db, req.params.id, decision, bodyString(req.body?.body));
    if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
    try {
      if (item.kind === "remote" && (decision === "approved" || decision === "changes_requested")) await submitRemoteDecision(item, decision, feedbackForItem(db, item.id));
      res.json({ item });
    } catch (error) { res.status(502).json({ error: String(error), item }); }
  });
  api.post("/queue/:id/export", (req, res) => {
    try {
      const item = getQueueItem(db, req.params.id);
      if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
      const destination = safeWorkspace(req.body?.destination);
      const packagePath = exportReviewPackage(item, feedbackForItem(db, item.id), destination);
      res.status(201).json({ packagePath });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.post("/packages/materialize", async (req, res) => {
    try {
      const packagePath = safeWorkspace(req.body?.packagePath);
      const destination = safeWorkspace(req.body?.destination);
      const result = await materializeReviewPackage(packagePath, destination);
      await activate(destination);
      res.status(201).json({ cwd: destination, copilotResume: result.package.queueItem.copilotSessionId ? `/resume ${result.package.queueItem.copilotSessionId}` : null, reviewUrl: "/" });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.post("/queue/:id/feedback", (req, res) => { try { const body = bodyString(req.body?.body); if (!body) throw new Error("body is required"); addFeedback(db, req.params.id, body, bodyString(req.body?.path), typeof req.body?.line === "number" ? req.body.line : undefined); res.status(201).json({ feedback: feedbackForItem(db, req.params.id) }); } catch (error) { res.status(400).json({ error: String(error) }); } });
  api.get("/queue/:id", (req, res) => { const item = getQueueItem(db, req.params.id); item ? res.json({ item, feedback: feedbackForItem(db, item.id) }) : res.status(404).json({ error: "Unknown queue item" }); });
  api.get("/agents", (_req, res) => res.json({ agents: db.query(`SELECT id,provider,command,workspace_path,review_session_id,status,metadata_json,updated_at FROM agent_registry ORDER BY updated_at DESC`).all() }));
  api.post("/agents", (req, res) => { const id = bodyString(req.body?.id); const provider = bodyString(req.body?.provider); if (!id || !provider) { res.status(400).json({ error: "id and provider are required" }); return; } db.query(`INSERT INTO agent_registry(id,provider,command,workspace_path,review_session_id,status,metadata_json,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET provider=excluded.provider,command=excluded.command,workspace_path=excluded.workspace_path,review_session_id=excluded.review_session_id,status=excluded.status,metadata_json=excluded.metadata_json,updated_at=excluded.updated_at`).run(id,provider,bodyString(req.body?.command) ?? null,bodyString(req.body?.workspacePath) ?? null,bodyString(req.body?.reviewSessionId) ?? null,bodyString(req.body?.status) ?? "connected",JSON.stringify(req.body?.metadata ?? {}),Date.now()); res.status(201).json({ id }); });
  api.post("/agents/:id/reconnect", (req, res) => {
    const result = db.query(`UPDATE agent_registry SET status='connected',updated_at=? WHERE id=?`).run(Date.now(), req.params.id);
    result.changes ? res.json({ id: req.params.id, status: "connected" }) : res.status(404).json({ error: "Unknown agent" });
  });
  const publicNode = (node: ReturnType<typeof getFederationNode>) => {
    if (!node) return undefined;
    const { token: _token, ...safe } = node;
    return safe;
  };
  api.get("/federation/nodes", (_req, res) => res.json({ nodes: listFederationNodes(db).map(publicNode) }));
  api.post("/federation/nodes", (req, res) => {
    try {
      const node = upsertFederationNode(db, { id: bodyString(req.body?.id), label: bodyString(req.body?.label) ?? "", sshTarget: bodyString(req.body?.sshTarget) ?? "", remotePort: Number(req.body?.remotePort), token: bodyString(req.body?.token) ?? "", enabled: req.body?.enabled !== false });
      res.status(201).json({ node: publicNode(node) });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.delete("/federation/nodes/:id", (req, res) => { federation.stop(req.params.id); removeFederationNode(db, req.params.id) ? res.status(204).end() : res.status(404).json({ error: "Unknown federation node" }); });
  api.post("/federation/nodes/:id/connect", async (req, res) => {
    try { const tunnel = await federation.connect(req.params.id); res.json({ node: publicNode(getFederationNode(db, req.params.id)), localPort: tunnel.port }); }
    catch (error) { res.status(502).json({ error: String(error) }); }
  });
  api.get("/federation/nodes/:id/queue", async (req, res) => {
    try { res.json(await federation.request(req.params.id, "/api/queue")); }
    catch (error) { res.status(502).json({ error: String(error) }); }
  });
  api.get("/federation/nodes/:id/workspaces", async (req, res) => {
    try { res.json(await federation.request(req.params.id, "/api/workspaces")); }
    catch (error) { res.status(502).json({ error: String(error) }); }
  });
  // The aggregate is intentionally lazy: it opens tunnels only for enabled
  // nodes queried by this request, and caches their GET responses briefly.
  api.get("/federation/queue", async (_req, res) => {
    const nodes = listFederationNodes(db).filter((node) => node.enabled);
    const results = await Promise.all(nodes.map(async (node) => {
      try { return { node: publicNode(node), ...(await federation.request<{ items: unknown[] }>(node.id, "/api/queue")) }; }
      catch (error) { return { node: publicNode(node), items: [], error: String(error) }; }
    }));
    res.json({ nodes: results });
  });

  // Only review UI traffic reaches this dispatcher. Control endpoints above
  // remain token-protected and cannot be shadowed by a workspace app.
  app.use((req, res, next) => active ? active.workspace.app(req, res, next) : res.status(404).json({ error: "No workspace is open. Use POST /api/workspaces/open." }));
  const server = await new Promise<ReturnType<Express["listen"]>>((resolvePromise) => {
    const listener = app.listen(options.port ?? 0, options.host ?? "127.0.0.1", () => resolvePromise(listener));
  });
  hub = new WsHub(server, "/ws");
  const address = server.address();
  const port = typeof address === "object" && address ? address.port : options.port ?? 0;
  const discovery = { port, token, pid: process.pid, version: VERSION, createdAt: new Date().toISOString() };
  writeDiscovery(discovery);
  return { app, server, db, discovery, close: async () => { if (active) await active.stop(); federation.stopAll(); hub?.close(); await new Promise<void>((resolvePromise) => server.close(() => resolvePromise())); db.close(); } };
}

if (import.meta.main) {
  const argPort = process.argv.indexOf("--port");
  const requestedPort = argPort >= 0 ? Number(process.argv[argPort + 1]) : undefined;
  startGlobalDaemon({ port: requestedPort }).then((daemon) => {
    console.log(`cmux-localreview global daemon listening on 127.0.0.1:${daemon.discovery.port}`);
    const stop = () => void daemon.close().finally(() => process.exit(0));
    process.once("SIGINT", stop);
    process.once("SIGTERM", stop);
  }).catch((error) => { console.error(error); process.exit(1); });
}
