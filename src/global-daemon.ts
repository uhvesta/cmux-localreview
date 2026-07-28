#!/usr/bin/env bun
import { createHash } from "node:crypto";
import { existsSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import type { Socket } from "node:net";
import { fileURLToPath } from "node:url";
import express, { type Express, type Request, type Response } from "express";

import { openDb } from "./server/db.ts";
import { daemonDbPath, ensureDaemonDirectories, localreviewDataDir, newDaemonToken, writeDiscovery } from "./server/daemonPaths.ts";
import { createWorkspaceSnapshot } from "./server/snapshots.ts";
import { addFeedback, decideQueueItem, decisionHistoryForItem, enqueue, feedbackForItem, getQueueItem, listQueue, markFeedbackDelivered, openNext, refreshRemoteQueue, requeueQueueItem, reorderQueue, updateAcpState, type QueueFeedback, type QueueStatus } from "./server/queueStore.ts";
import { buildWorkspaceApp, type WorkspaceApp } from "./server/app.ts";
import { WsHub } from "./server/wsHub.ts";
import { exportReviewPackage, materializeReviewPackage } from "./server/reviewPackage.ts";
import { cleanupRemoteWorkspace, prepareRemoteWorkspace, remotePullRequestFromQueueItem, remoteWorkspacePaths, resolveRemotePullRequest, submitRemoteDecision } from "./server/remotePr.ts";
import {
  listRegisteredAgents,
  markAgentDelivered,
  markAgentDeliveryFailed,
  reconnectAgent,
  registerAgent,
  resolveTerminalTarget,
  AgentRoutingError,
} from "./server/agentRegistry.ts";
import { captureSubmissionProvenance, redactSubmissionMetadata, type SubmissionProvenance } from "./server/submissionContext.ts";
import { workspaceSourceFingerprint } from "./server/workspaceFingerprint.ts";
import { FederationTunnelManager, getFederationNode, listFederationNodes, removeFederationNode, setFederationNodeEnabled, upsertFederationNode } from "./server/federation.ts";
import { AcpRemoteSession, parseLoopbackAcpEndpoint, type AcpEndpoint } from "./server/acpRemote.ts";
import { GitHubAuthService } from "./server/githubAuth.ts";

const VERSION = "0.2.0";

interface ActiveReview {
  rootPath: string;
  defaultTerminalAgentId?: string;
  workspace: WorkspaceApp;
  stop: () => Promise<void>;
}

export interface GlobalDaemonOptions { port?: number; host?: string; token?: string; open?: boolean; }

function safeWorkspace(input: unknown): string {
  if (typeof input !== "string" || !input.trim()) throw new Error("workspacePath is required");
  return resolve(input);
}

function bodyString(value: unknown): string | undefined { return typeof value === "string" ? value : undefined; }

/**
 * A token-free PR review is intentionally available only to a browser on the
 * same machine. It still creates a cached Git worktree, so never expose this
 * convenience endpoint on a network listener.
 */
function isLoopbackRequest(req: Request): boolean {
  const address = req.socket.remoteAddress ?? "";
  return address === "127.0.0.1" || address === "::1" || address === "::ffff:127.0.0.1";
}

function requireAuth(token: string) {
  return (req: Request, res: Response, next: () => void) => {
    if (req.path === "/health") return next();
    const header = req.header("authorization");
    if (header !== `Bearer ${token}`) { res.status(401).json({ error: "Bearer token required" }); return; }
    next();
  };
}

/** Starts the authenticated, persistent loopback control plane. */
export async function startGlobalDaemon(options: GlobalDaemonOptions = {}) {
  ensureDaemonDirectories();
  const db = openDb(daemonDbPath());
  const federation = new FederationTunnelManager(db);
  const githubAuth = new GitHubAuthService();
  const token = options.token ?? newDaemonToken();
  const app = express();
  app.use(express.json({ limit: "2mb" }));
  app.get("/health", (_req, res) => res.json({ ok: true, version: VERSION, pid: process.pid }));
  const api = express.Router();
  // Review endpoints (/api/repos, comments, /ask, UI state, etc.) are served
  // by the active workspace app and deliberately remain local-browser usable.
  // Only the daemon control plane is bearer-protected.
  api.use((req, res, next) => {
    if (/^\/(workspaces|queue|agents|packages|federation|github)(?:\/|$)/.test(req.path)) return requireAuth(token)(req, res, next);
    next();
  });
  app.use("/api", api);
  let active: ActiveReview | undefined;
  let hub: WsHub | undefined;
  const watcherTimers = new Map<string, ReturnType<typeof setInterval>>();
  const watcherPolls = new Set<string>();
  const acpConnections = new Map<string, { endpoint: AcpEndpoint; session: AcpRemoteSession; chain: Promise<void> }>();
  // Serialize the whole feedback transaction, not only `prompt`. Otherwise
  // two simultaneous HTTP requests can both read the same undelivered rows
  // before either marks them delivered and send duplicate instructions.
  const feedbackDeliveries = new Map<string, Promise<unknown>>();

  // Browser-first authentication for both GitHub API operations and the
  // Copilot SDK. `gh` owns the OAuth credential in the OS keychain; neither
  // a GitHub token nor a Copilot token is accepted or retained by this app.
  api.get("/github/auth/status", async (_req, res) => {
    res.json(await githubAuth.status());
  });
  api.post("/github/auth/start", (_req, res) => {
    res.status(202).json({ login: githubAuth.start() });
  });
  api.post("/github/auth/cancel", (_req, res) => {
    res.json({ login: githubAuth.cancel() });
  });

  const feedbackPrompt = (item: { title: string; workspacePath: string }, feedback: QueueFeedback[], decisionBody?: string) => {
    const lines = feedback.map((entry) => `- ${entry.path ? `${entry.path}${entry.line ? `:${entry.line}` : ""}: ` : ""}${entry.body}`);
    return [
      `Local review feedback for ${item.title}.`,
      `The review snapshot came from ${item.workspacePath}. Keep working in your existing session; address the feedback and report what changed.`,
      decisionBody ? `Reviewer summary: ${decisionBody}` : "",
      lines.length ? `Comments:\n${lines.join("\n")}` : "",
    ].filter(Boolean).join("\n\n");
  };

  const queueAcpEndpoint = (body: any): { host?: string; port?: number; sessionId?: string; cwd?: string } => {
    const acp = body?.acp && typeof body.acp === "object" ? body.acp : body;
    const host = bodyString(acp?.host ?? acp?.acpHost);
    const port = typeof (acp?.port ?? acp?.acpPort) === "number" ? (acp.port ?? acp.acpPort) : undefined;
    const sessionId = bodyString(acp?.sessionId ?? acp?.acpSessionId);
    if (host === undefined && port === undefined && sessionId === undefined) return {};
    return parseLoopbackAcpEndpoint({ host, port, sessionId, cwd: bodyString(body?.workspacePath ?? body?.cwd) });
  };

  /** Normalize optional live-agent data without accepting a non-loopback ACP endpoint. */
  const agentMetadata = (body: any, existing: Record<string, unknown> = {}): Record<string, unknown> => {
    const supplied = body?.metadata && typeof body.metadata === "object" && !Array.isArray(body.metadata)
      ? body.metadata as Record<string, unknown>
      : {};
    const metadata = { ...existing, ...supplied };
    const acp = queueAcpEndpoint(body);
    if (acp.host && acp.port && acp.sessionId) metadata.acp = acp;
    const copilotSessionId = bodyString(body?.copilotSessionId);
    if (copilotSessionId) metadata.copilotSessionId = copilotSessionId;
    return metadata;
  };

  const deliverFeedback = (itemId: string, policy: "queue" | "interrupt", includeDelivered: boolean, extraBody?: string) => {
    // An interrupt request is intentionally allowed to reach a live turn
    // immediately.  The durable delivery transaction below is still
    // serialized, so it re-reads undelivered feedback after the cancelled
    // turn settles and cannot send the same comments twice.
    const active = acpConnections.get(itemId);
    if (policy === "interrupt" && active?.session.isBusy) {
      void active.session.cancel().catch((error) => {
        updateAcpState(db, itemId, "error", String(error));
      });
    }
    const previous = feedbackDeliveries.get(itemId) ?? Promise.resolve();
    const run = previous.catch(() => undefined).then(async () => {
      const item = getQueueItem(db, itemId);
      if (!item) throw new Error("Unknown queue item");
      if (!item.acpHost || !item.acpPort || !item.acpSessionId) throw new Error("This queue item has no ACP session endpoint");
      const endpoint = parseLoopbackAcpEndpoint({ host: item.acpHost, port: item.acpPort, sessionId: item.acpSessionId, cwd: item.workspacePath });
      const feedback = feedbackForItem(db, item.id, { undeliveredOnly: !includeDelivered });
      if (!feedback.length && !extraBody) return { item, delivered: 0, state: item.acpState, skipped: true };
      let live = acpConnections.get(item.id);
      if (!live || live.endpoint.host !== endpoint.host || live.endpoint.port !== endpoint.port || live.endpoint.sessionId !== endpoint.sessionId) {
        live?.session.dispose();
        updateAcpState(db, item.id, "connecting");
        const session = await AcpRemoteSession.connect(endpoint, {
          onState: (state, error) => updateAcpState(db, item.id, state, error ? String(error) : null),
        });
        live = { endpoint, session, chain: Promise.resolve() };
        acpConnections.set(item.id, live);
      }
      if (policy === "interrupt" && live.session.isBusy) await live.session.cancel();
      const prompt = feedbackPrompt(item, feedback, extraBody);
      const promptRun = live.chain.then(async () => { await live!.session.prompt(prompt); });
      live.chain = promptRun.catch(() => undefined);
      await promptRun;
      if (!includeDelivered) markFeedbackDelivered(db, item.id, feedback.map((entry) => entry.id));
      return { item: getQueueItem(db, item.id), delivered: feedback.length, state: "idle", policy };
    });
    feedbackDeliveries.set(itemId, run);
    void run.finally(() => {
      if (feedbackDeliveries.get(itemId) === run) feedbackDeliveries.delete(itemId);
    }).catch(() => undefined);
    return run;
  };

  type WatcherRow = {
    workspace_path: string; enabled: number; poll_interval_ms: number; title: string | null; body: string | null; base_ref: string | null;
    agent_id: string | null; agent_provider: string | null; feedback_target: string | null; provenance_json: string | null;
    acp_host: string | null; acp_port: number | null; acp_session_id: string | null; agent_kind: string | null; copilot_session_id: string | null;
    last_fingerprint: string | null; last_queue_item_id: string | null;
  };

  const provenanceFor = async (workspacePath: string, candidate: unknown): Promise<SubmissionProvenance | unknown> => {
    // queue-submit captures cmux's caller environment before contacting this
    // long-lived daemon. Direct API callers still receive a daemon-side
    // best-effort capture. Metadata is always redacted before SQLite/artifacts.
    if (candidate && typeof candidate === "object" && !Array.isArray(candidate)) {
      return redactSubmissionMetadata(candidate);
    }
    return captureSubmissionProvenance(workspacePath);
  };

  const stopWatcher = (workspacePath: string) => {
    const timer = watcherTimers.get(workspacePath);
    if (timer) clearInterval(timer);
    watcherTimers.delete(workspacePath);
  };

  const pollWatcher = async (workspacePath: string): Promise<void> => {
    if (watcherPolls.has(workspacePath)) return;
    watcherPolls.add(workspacePath);
    try {
      const watcher = db.query(`SELECT workspace_path,enabled,poll_interval_ms,title,body,base_ref,agent_id,agent_provider,feedback_target,provenance_json,acp_host,acp_port,acp_session_id,agent_kind,copilot_session_id,last_fingerprint,last_queue_item_id FROM queue_watchers WHERE workspace_path = ?`).get(workspacePath) as WatcherRow | null;
      if (!watcher || !watcher.enabled) return;
      const fingerprint = await workspaceSourceFingerprint(workspacePath);
      if (fingerprint === watcher.last_fingerprint) return;
      const baseProvenance = watcher.provenance_json ? JSON.parse(watcher.provenance_json) : undefined;
      const provenance = await captureSubmissionProvenance(workspacePath, {
        submission: baseProvenance,
        autoQueue: { mechanism: "git-poll", detectedAt: new Date().toISOString(), previousFingerprint: watcher.last_fingerprint },
      });
      const snapshot = await createWorkspaceSnapshot(workspacePath, undefined, watcher.base_ref ?? undefined, provenance);
      const previous = watcher.last_queue_item_id ? getQueueItem(db, watcher.last_queue_item_id) : undefined;
      // A queued snapshot has not been opened yet, so replace it instead of
      // presenting stale work to the reviewer. Once review has started, keep
      // the original immutable item and add a fresh requeue behind it.
      if (previous && (previous.status === "queued" || previous.status === "changes_requested")) {
        decideQueueItem(db, previous.id, "completed", "Superseded by a newer Git worktree snapshot.");
      }
      const result = enqueue(db, {
        title: watcher.title ?? `Review ${workspacePath}`,
        body: watcher.body ?? "",
        workspacePath,
        idempotentKey: `git-poll:${createHash("sha256").update(workspacePath).digest("hex")}:${fingerprint}`,
        agentId: watcher.agent_id ?? undefined,
        agentProvider: watcher.agent_provider ?? undefined,
        copilotSessionId: watcher.copilot_session_id ?? undefined,
        acpHost: watcher.acp_host ?? undefined,
        acpPort: watcher.acp_port ?? undefined,
        acpSessionId: watcher.acp_session_id ?? undefined,
        agentKind: watcher.agent_kind ?? undefined,
        feedbackTarget: watcher.feedback_target ?? undefined,
        baseRef: watcher.base_ref ?? undefined,
        provenance,
        sourceFingerprint: fingerprint,
        supersedesId: previous?.id,
        snapshotManifestPath: snapshot.manifestPath,
        snapshotManifest: snapshot.manifest,
      });
      db.query(`UPDATE queue_watchers SET last_fingerprint=?,last_queue_item_id=?,updated_at=? WHERE workspace_path=?`).run(fingerprint, result.item.id, Date.now(), workspacePath);
    } catch (error) {
      console.warn(`Auto-queue poll failed for ${workspacePath}:`, error);
    } finally {
      watcherPolls.delete(workspacePath);
    }
  };

  const startWatcher = (workspacePath: string, intervalMs: number) => {
    stopWatcher(workspacePath);
    const timer = setInterval(() => void pollWatcher(workspacePath), intervalMs);
    watcherTimers.set(workspacePath, timer);
    void pollWatcher(workspacePath);
  };

  const queueRemotePullRequest = async (input: {
    remoteUrl: string; title?: string; body?: string; base?: string; snapshot?: boolean;
    agentId?: string; agentProvider?: string; copilotSessionId?: string; feedbackTarget?: string; provenance?: unknown;
    acpHost?: string; acpPort?: number; acpSessionId?: string; agentKind?: string;
  }) => {
    const pr = await resolveRemotePullRequest(input.remoteUrl);
    const remoteWorkspace = await prepareRemoteWorkspace(pr);
    const baseRef = input.base ?? pr.baseSha;
    const provenance = await provenanceFor(remoteWorkspace.workspacePath, input.provenance);
    let snapshotManifestPath: string | undefined;
    let snapshotManifest: unknown = { remotePullRequest: pr, remoteWorkspace: { mirrorPath: remoteWorkspace.mirrorPath, worktreePath: remoteWorkspace.worktreePath } };
    if (input.snapshot !== false) {
      const snapshot = await createWorkspaceSnapshot(remoteWorkspace.workspacePath, undefined, baseRef, provenance as SubmissionProvenance);
      snapshotManifest = { ...snapshot.manifest, remotePullRequest: pr, remoteWorkspace: { mirrorPath: remoteWorkspace.mirrorPath, worktreePath: remoteWorkspace.worktreePath } };
      writeFileSync(snapshot.manifestPath, `${JSON.stringify(snapshotManifest, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
      snapshotManifestPath = snapshot.manifestPath;
    }
    return refreshRemoteQueue(db, {
      title: input.title ?? `Review #${pr.number}: ${pr.title}`,
      body: input.body ?? pr.body,
      workspacePath: remoteWorkspace.workspacePath,
      kind: "remote", remoteUrl: pr.url, idempotentKey: `github-pr:${pr.url}`,
      agentId: input.agentId, agentProvider: input.agentProvider, copilotSessionId: input.copilotSessionId,
      acpHost: input.acpHost, acpPort: input.acpPort, acpSessionId: input.acpSessionId, agentKind: input.agentKind,
      feedbackTarget: input.feedbackTarget, baseRef, provenance, sourceFingerprint: pr.headSha,
      snapshotManifestPath, snapshotManifest,
    });
  };

  /**
   * Opens a PR in an isolated, cached worktree without adding a queue item,
   * snapshot, agent route, ACP endpoint, or GitHub review capability. This is
   * deliberately separate from `queueRemotePullRequest`: it is for local
   * investigation and /ask, not formal review submission.
   */
  const openReadOnlyPullRequest = async (remoteUrl: string) => {
    const pr = await resolveRemotePullRequest(remoteUrl);
    const remoteWorkspace = await prepareRemoteWorkspace(pr);
    const review = await activate(remoteWorkspace.workspacePath, pr.baseSha);
    return {
      pullRequest: pr,
      workspacePath: remoteWorkspace.workspacePath,
      repos: review.workspace.repos.map((repo) => ({ id: repo.repoId, path: repo.repo.workspaceRelativePath })),
      // No daemon token is included. `localOnly=1` hides formal review
      // controls; the remaining diff and /ask routes are workspace-local.
      reviewUrl: "/review?localOnly=1",
    };
  };

  const activate = async (workspacePath: string, base?: string, defaultTerminalAgentId?: string) => {
    if (active?.rootPath === workspacePath) return active;
    if (active) await active.stop();
    // Keep review-local state next to the daemon database. In particular this
    // makes an isolated CMUX_LOCALREVIEW_DATA_DIR a complete isolation
    // boundary for demos, tests, and disposable remote workers.
    const workspaceDb = openDb(join(localreviewDataDir(), "workspaces", `${createHash("sha256").update(workspacePath).digest("hex").slice(0, 16)}.db`));
    workspaceDb.query(`INSERT INTO workspace(id,root_path,created_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET root_path=excluded.root_path`).run(workspacePath, Date.now());
    const workspace = await buildWorkspaceApp({
      workspaceRoot: workspacePath,
      db: workspaceDb,
      base,
      defaultTerminalAgentId,
      resolveTerminalAgent: (agentId, rootPath) => resolveTerminalTarget(db, agentId, rootPath),
      onTerminalDeliverySuccess: (agentId) => markAgentDelivered(db, agentId),
      onTerminalDeliveryFailure: (agentId, error) => markAgentDeliveryFailed(db, agentId, error),
    });
    const startedHub = hub;
    let cleanupBtw: (() => void) | undefined;
    if (startedHub) {
      await workspace.startWatchers(startedHub);
      const managed = workspace.startBtw(startedHub, "claude");
      cleanupBtw = () => { managed.btwManager.disposeAll(); managed.terminalBtw.stop(); };
    }
    active = { rootPath: workspacePath, defaultTerminalAgentId, workspace, stop: async () => { cleanupBtw?.(); await workspace.stopAsk(); await workspace.stopWatchers(); workspaceDb.close(); } };
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
      // `/` is intentionally the daemon-wide Queue Home.  A workspace review
      // is an explicit destination so opening the daemon never silently drops
      // a reviewer into whichever workspace happened to be active.
      res.json({ workspacePath: rootPath, repos: review.workspace.repos.map((repo) => ({ id: repo.repoId, path: repo.repo.workspaceRelativePath })), reviewUrl: "/review" });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });

  // Token-free by design, but only usable from loopback. This is the API form
  // of `localreview-open --pr`; it must never create a queue record or grant
  // a browser the formal feedback/publish controls.
  api.post("/local-review/pr", async (req, res) => {
    if (!isLoopbackRequest(req)) { res.status(403).json({ error: "Local question-only PR review is available only from loopback." }); return; }
    try {
      const remoteUrl = bodyString(req.body?.remoteUrl);
      if (!remoteUrl) throw new Error("remoteUrl is required");
      res.json(await openReadOnlyPullRequest(remoteUrl));
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });

  api.get("/queue", (req, res) => res.json({ items: listQueue(db, bodyString(req.query.status) as QueueStatus | undefined) }));
  api.post("/queue", async (req, res) => {
    try {
      const acp = queueAcpEndpoint(req.body);
      const remoteUrl = bodyString(req.body?.remoteUrl);
      if (remoteUrl) {
        const result = await queueRemotePullRequest({
          remoteUrl, title: bodyString(req.body?.title), body: bodyString(req.body?.body), base: bodyString(req.body?.base), snapshot: req.body?.snapshot !== false,
          agentId: bodyString(req.body?.agentId), agentProvider: bodyString(req.body?.agentProvider), copilotSessionId: bodyString(req.body?.copilotSessionId),
          acpHost: acp.host, acpPort: acp.port, acpSessionId: acp.sessionId, agentKind: bodyString(req.body?.agentKind),
          feedbackTarget: bodyString(req.body?.feedbackTarget), provenance: req.body?.provenance,
        });
        res.status(result.created ? 201 : 200).json(result);
        return;
      }
      let workspacePath = bodyString(req.body?.workspacePath) ?? bodyString(req.body?.cwd);
      const kind = "local";
      const resolvedWorkspace = safeWorkspace(workspacePath);
      const title = bodyString(req.body?.title) ?? `Review ${resolvedWorkspace}`;
      const sourceFingerprint = await workspaceSourceFingerprint(resolvedWorkspace);
      const provenance = await provenanceFor(resolvedWorkspace, req.body?.provenance);
      let snapshotManifestPath: string | undefined;
      let snapshotManifest: unknown;
      if (kind === "local" || req.body?.snapshot !== false) {
        const snapshot = await createWorkspaceSnapshot(resolvedWorkspace, undefined, bodyString(req.body?.base), provenance as SubmissionProvenance);
        snapshotManifestPath = snapshot.manifestPath;
        snapshotManifest = snapshot.manifest;
      }
      const result = enqueue(db, { title, body: bodyString(req.body?.body), workspacePath: resolvedWorkspace, kind, remoteUrl, idempotentKey: bodyString(req.body?.idempotentKey), agentId: bodyString(req.body?.agentId), agentProvider: bodyString(req.body?.agentProvider), copilotSessionId: bodyString(req.body?.copilotSessionId), acpHost: acp.host, acpPort: acp.port, acpSessionId: acp.sessionId, agentKind: bodyString(req.body?.agentKind), snapshotManifestPath, snapshotManifest, feedbackTarget: bodyString(req.body?.feedbackTarget), baseRef: bodyString(req.body?.base), provenance, sourceFingerprint });
      res.status(result.created ? 201 : 200).json(result);
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  // Explicit hook/poll configuration: enabled workspaces are snapshotted at
  // configuration time and then only when their Git source fingerprint moves.
  api.post("/queue/watch", async (req, res) => {
    try {
      const workspacePath = safeWorkspace(req.body?.workspacePath ?? req.body?.cwd);
      const enabled = req.body?.enabled !== false;
      const pollIntervalMs = typeof req.body?.pollIntervalMs === "number" && Number.isInteger(req.body.pollIntervalMs)
        ? Math.max(1_000, Math.min(req.body.pollIntervalMs, 10 * 60_000)) : 5_000;
      if (!enabled) {
        db.query(`UPDATE queue_watchers SET enabled=0,updated_at=? WHERE workspace_path=?`).run(Date.now(), workspacePath);
        stopWatcher(workspacePath);
        res.json({ workspacePath, enabled: false });
        return;
      }
      const provenance = await provenanceFor(workspacePath, req.body?.provenance);
      const acp = queueAcpEndpoint(req.body);
      const requestedSeedId = bodyString(req.body?.lastQueueItemId);
      const seedItem = requestedSeedId ? getQueueItem(db, requestedSeedId) : undefined;
      if (seedItem && seedItem.workspacePath !== workspacePath) throw new Error("lastQueueItemId belongs to another workspace");
      const seedFingerprint = seedItem?.sourceFingerprint ?? bodyString(req.body?.lastFingerprint);
      db.query(`INSERT INTO queue_watchers(workspace_path,enabled,poll_interval_ms,title,body,base_ref,agent_id,agent_provider,feedback_target,provenance_json,acp_host,acp_port,acp_session_id,agent_kind,copilot_session_id,last_fingerprint,last_queue_item_id,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_path) DO UPDATE SET enabled=1,poll_interval_ms=excluded.poll_interval_ms,title=COALESCE(excluded.title,queue_watchers.title),body=COALESCE(excluded.body,queue_watchers.body),base_ref=COALESCE(excluded.base_ref,queue_watchers.base_ref),agent_id=COALESCE(excluded.agent_id,queue_watchers.agent_id),agent_provider=COALESCE(excluded.agent_provider,queue_watchers.agent_provider),feedback_target=COALESCE(excluded.feedback_target,queue_watchers.feedback_target),provenance_json=excluded.provenance_json,acp_host=COALESCE(excluded.acp_host,queue_watchers.acp_host),acp_port=COALESCE(excluded.acp_port,queue_watchers.acp_port),acp_session_id=COALESCE(excluded.acp_session_id,queue_watchers.acp_session_id),agent_kind=COALESCE(excluded.agent_kind,queue_watchers.agent_kind),copilot_session_id=COALESCE(excluded.copilot_session_id,queue_watchers.copilot_session_id),last_fingerprint=COALESCE(excluded.last_fingerprint,queue_watchers.last_fingerprint),last_queue_item_id=COALESCE(excluded.last_queue_item_id,queue_watchers.last_queue_item_id),updated_at=excluded.updated_at`).run(workspacePath, 1, pollIntervalMs, bodyString(req.body?.title) ?? null, bodyString(req.body?.body) ?? null, bodyString(req.body?.base) ?? null, bodyString(req.body?.agentId) ?? null, bodyString(req.body?.agentProvider) ?? null, bodyString(req.body?.feedbackTarget) ?? null, JSON.stringify(provenance), acp.host ?? seedItem?.acpHost ?? null, acp.port ?? seedItem?.acpPort ?? null, acp.sessionId ?? seedItem?.acpSessionId ?? null, bodyString(req.body?.agentKind) ?? seedItem?.agentKind ?? null, bodyString(req.body?.copilotSessionId) ?? seedItem?.copilotSessionId ?? null, seedFingerprint ?? null, seedItem?.id ?? null, Date.now());
      startWatcher(workspacePath, pollIntervalMs);
      // The first poll is async; return its current persisted state rather
      // than blocking an interactive hook for a potentially large snapshot.
      res.status(201).json({ workspacePath, enabled: true, pollIntervalMs });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  // Event-driven integrations (cmux/ACP wrappers, Git hooks) can call this
  // instead of polling. The source fingerprint makes repeated hook delivery
  // idempotent without trusting a caller-supplied idempotency key.
  api.post("/queue/hook", async (req, res) => {
    try {
      const workspacePath = safeWorkspace(req.body?.workspacePath ?? req.body?.cwd);
      const fingerprint = await workspaceSourceFingerprint(workspacePath);
      const prior = db.query(`SELECT * FROM queue_items WHERE workspace_path=? AND source_fingerprint=? ORDER BY created_at DESC LIMIT 1`).get(workspacePath, fingerprint) as { id: string } | null;
      if (prior) { res.json({ created: false, item: getQueueItem(db, prior.id) }); return; }
      const provenance = await provenanceFor(workspacePath, req.body?.provenance);
      const snapshot = await createWorkspaceSnapshot(workspacePath, undefined, bodyString(req.body?.base), provenance as SubmissionProvenance);
      const previous = db.query(`SELECT id,status FROM queue_items WHERE workspace_path=? ORDER BY created_at DESC LIMIT 1`).get(workspacePath) as { id: string; status: QueueStatus } | null;
      if (previous && (previous.status === "queued" || previous.status === "changes_requested")) decideQueueItem(db, previous.id, "completed", "Superseded by a newer Git hook snapshot.");
      const acp = queueAcpEndpoint(req.body);
      const result = enqueue(db, { title: bodyString(req.body?.title) ?? `Review ${workspacePath}`, body: bodyString(req.body?.body), workspacePath, idempotentKey: `hook:${createHash("sha256").update(workspacePath).digest("hex")}:${fingerprint}`, agentId: bodyString(req.body?.agentId), agentProvider: bodyString(req.body?.agentProvider), copilotSessionId: bodyString(req.body?.copilotSessionId), acpHost: acp.host, acpPort: acp.port, acpSessionId: acp.sessionId, agentKind: bodyString(req.body?.agentKind), feedbackTarget: bodyString(req.body?.feedbackTarget), baseRef: bodyString(req.body?.base), provenance, sourceFingerprint: fingerprint, supersedesId: previous?.id, snapshotManifestPath: snapshot.manifestPath, snapshotManifest: snapshot.manifest });
      res.status(201).json(result);
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.post("/queue/open-next", async (_req, res) => {
    const item = openNext(db);
    if (!item) { res.status(404).json({ error: "No queued review items" }); return; }
    try { await activate(item.workspacePath, item.baseRef ?? undefined, item.agentId ?? undefined); res.json({ item, reviewUrl: "/review" }); } catch (error) { res.status(500).json({ error: String(error), item }); }
  });
  api.post("/queue/:id/reorder", (req, res) => { const item = reorderQueue(db, req.params.id, Number(req.body?.position)); item ? res.json({ item }) : res.status(404).json({ error: "Unknown queue item" }); });
  api.post("/queue/:id/requeue", (req, res) => {
    const item = requeueQueueItem(db, req.params.id);
    item ? res.json({ item, decisions: decisionHistoryForItem(db, item.id) }) : res.status(404).json({ error: "Unknown queue item" });
  });
  api.post("/queue/:id/refresh", async (req, res) => {
    try {
      const item = getQueueItem(db, req.params.id);
      if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
      if (item.kind !== "remote" || !item.remoteUrl) { res.status(400).json({ error: "Only remote PR queue items can be refreshed" }); return; }
      const result = await queueRemotePullRequest({
        remoteUrl: item.remoteUrl, title: bodyString(req.body?.title) ?? item.title, body: bodyString(req.body?.body) ?? item.body,
        base: bodyString(req.body?.base) ?? item.baseRef ?? undefined, snapshot: req.body?.snapshot !== false,
        agentId: item.agentId ?? undefined, agentProvider: item.agentProvider ?? undefined, copilotSessionId: item.copilotSessionId ?? undefined,
        acpHost: item.acpHost ?? undefined, acpPort: item.acpPort ?? undefined, acpSessionId: item.acpSessionId ?? undefined, agentKind: item.agentKind ?? undefined,
        feedbackTarget: item.feedbackTarget ?? undefined, provenance: item.provenance ?? undefined,
      });
      res.json(result);
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.post("/queue/:id/cleanup", async (req, res) => {
    try {
      const item = getQueueItem(db, req.params.id);
      if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
      if (item.kind !== "remote") { res.status(400).json({ error: "Only remote PR queue items have managed worktrees" }); return; }
      const pr = remotePullRequestFromQueueItem(item);
      if (!pr) { res.status(409).json({ error: "This remote queue item predates resolved PR metadata; refresh it before cleanup." }); return; }
      const cleanup = await cleanupRemoteWorkspace(pr, { removeMirror: req.body?.removeMirror === true });
      res.json({ item, cleanup });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.get("/queue/:id/remote-status", (req, res) => {
    const item = getQueueItem(db, req.params.id);
    if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
    if (item.kind !== "remote") { res.status(400).json({ error: "Only remote PR queue items have a managed mirror/worktree" }); return; }
    const pr = remotePullRequestFromQueueItem(item);
    if (!pr) { res.status(409).json({ error: "This remote item has no resolved PR metadata. Refresh it to create a managed cache." }); return; }
    const paths = remoteWorkspacePaths(pr);
    res.json({
      pullRequest: pr,
      cache: {
        mirrorPath: paths.mirrorPath,
        mirrorPresent: existsSync(paths.mirrorPath),
        worktreePath: paths.worktreePath,
        worktreePresent: existsSync(paths.worktreePath),
      },
      recovery: {
        refresh: `POST /api/queue/${item.id}/refresh`,
        cleanup: `POST /api/queue/${item.id}/cleanup`,
        reAdd: `POST /api/queue with { remoteUrl: ${JSON.stringify(item.remoteUrl)} }`,
      },
    });
  });
  api.post("/queue/:id/decision", async (req, res) => {
    const decision = bodyString(req.body?.decision);
    if (decision !== "approved" && decision !== "changes_requested" && decision !== "completed") { res.status(400).json({ error: "decision must be approved, changes_requested, or completed" }); return; }
    const before = getQueueItem(db, req.params.id);
    if (!before) { res.status(404).json({ error: "Unknown queue item" }); return; }
    try {
      // Don't mark a PR approved/changes-requested locally unless GitHub
      // accepted the outgoing review. The temporary body is used for API
      // publication before committing the local lifecycle transition.
      const outgoing = { ...before, decisionBody: bodyString(req.body?.body) ?? null };
      const remoteReview = before.kind === "remote" && (decision === "approved" || decision === "changes_requested")
        ? await submitRemoteDecision(outgoing, decision, feedbackForItem(db, before.id)) : undefined;
      const item = decideQueueItem(db, before.id, decision, bodyString(req.body?.body))!;
      const injectFeedback = decision === "changes_requested" && req.body?.injectFeedback === true;
      const delivery = injectFeedback
        ? await deliverFeedback(before.id, req.body?.deliveryPolicy === "interrupt" ? "interrupt" : "queue", false, bodyString(req.body?.body))
        : undefined;
      res.json({ item: getQueueItem(db, before.id), remoteReview, delivery });
    } catch (error) { res.status(502).json({ error: String(error), item: before }); }
  });
  /**
   * A COMMENT review has the same stale-head and inline-comment safeguards as
   * approve/request-changes, but deliberately leaves local queue status
   * alone. This makes feedback publication safe to test on a real PR.
   */
  api.post("/queue/:id/publish-comment", async (req, res) => {
    const item = getQueueItem(db, req.params.id);
    if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
    if (item.kind !== "remote") { res.status(400).json({ error: "Only remote PR queue items can publish GitHub comments" }); return; }
    try {
      const outgoing = { ...item, decisionBody: bodyString(req.body?.body) ?? null };
      const remoteReview = await submitRemoteDecision(outgoing, "comment", feedbackForItem(db, item.id));
      res.json({ item, remoteReview });
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
      res.status(201).json({ cwd: destination, copilotResume: result.package.queueItem.copilotSessionId ? `/resume ${result.package.queueItem.copilotSessionId}` : null, reviewUrl: "/review" });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  api.post("/queue/:id/feedback", async (req, res) => {
    try {
      const body = bodyString(req.body?.body);
      if (!body) throw new Error("body is required");
      addFeedback(db, req.params.id, body, bodyString(req.body?.path), typeof req.body?.line === "number" ? req.body.line : undefined);
      const delivery = req.body?.deliver === true
        ? await deliverFeedback(req.params.id, req.body?.deliveryPolicy === "interrupt" ? "interrupt" : "queue", false)
        : undefined;
      res.status(201).json({ feedback: feedbackForItem(db, req.params.id), delivery });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  /** Portable counterpart to ACP delivery: copy/paste this anywhere safely. */
  api.get("/queue/:id/feedback/prompt", (req, res) => {
    const item = getQueueItem(db, req.params.id);
    if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
    const includeDelivered = req.query.includeDelivered === "true";
    const text = feedbackPrompt(item, feedbackForItem(db, item.id, { undeliveredOnly: !includeDelivered }), item.decisionBody ?? undefined);
    res.type("text/plain").send(text);
  });
  /** Batch all undelivered review comments into one prompt to the live ACP session. */
  api.post("/queue/:id/deliver-feedback", async (req, res) => {
    try {
      const result = await deliverFeedback(
        req.params.id,
        req.body?.policy === "interrupt" ? "interrupt" : "queue",
        req.body?.includeDelivered === true,
        bodyString(req.body?.body),
      );
      res.json(result);
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  /**
   * A UI-safe reproduction plan. Materialization still requires an explicit
   * destination supplied by a local user/CLI, so a page cannot overwrite a
   * workspace merely by rendering this endpoint.
   */
  api.get("/queue/:id/reproduce", (req, res) => {
    const item = getQueueItem(db, req.params.id);
    if (!item) { res.status(404).json({ error: "Unknown queue item" }); return; }
    const snapshot = item.snapshotManifest as { id?: unknown; repos?: unknown[] } | null;
    const hasSnapshot = !!item.snapshotManifestPath && !!snapshot?.id;
    const existingAcp = item.acpHost && item.acpPort && item.acpSessionId
      ? { host: item.acpHost, port: item.acpPort, sessionId: item.acpSessionId, state: item.acpState, error: item.acpLastError, canAttemptResume: item.acpState !== "error" }
      : null;
    res.json({
      itemId: item.id,
      workspacePath: item.workspacePath,
      snapshot: hasSnapshot ? { id: snapshot!.id, manifestPath: item.snapshotManifestPath, repositories: snapshot!.repos?.length ?? 0 } : null,
      existingAcp,
      copilotSessionId: item.copilotSessionId,
      commands: hasSnapshot ? {
        reproduceSnapshot: `localreview-reproduce ${JSON.stringify(item.snapshotManifestPath!)} <empty-destination>`,
        reproduceCopilot: `localreview-reproduce-copilot ${item.id} <empty-destination>`,
        freshAcp: "cd <empty-destination> && copilot --acp --port 4123",
      } : null,
      notes: [
        hasSnapshot ? "The snapshot can be reproduced exactly into a new or empty destination." : "No retained snapshot is available for this queue item.",
        existingAcp ? "The saved ACP endpoint is a live-session hint only; the daemon can resume delivery only while that local endpoint and session still exist." : "No live ACP session was submitted; use the fresh ACP command after reproduction.",
      ],
    });
  });
  api.get("/queue/:id", (req, res) => {
    const item = getQueueItem(db, req.params.id);
    item ? res.json({ item, feedback: feedbackForItem(db, item.id), decisions: decisionHistoryForItem(db, item.id) }) : res.status(404).json({ error: "Unknown queue item" });
  });
  api.get("/agents", (_req, res) => res.json({ agents: listRegisteredAgents(db) }));
  api.post("/agents", (req, res) => {
    const id = bodyString(req.body?.id);
    const provider = bodyString(req.body?.provider);
    if (!id || !provider) { res.status(400).json({ error: "id and provider are required" }); return; }
    try {
      const agent = registerAgent(db, {
        id, provider, command: bodyString(req.body?.command), workspacePath: bodyString(req.body?.workspacePath),
        reviewSessionId: bodyString(req.body?.reviewSessionId), status: bodyString(req.body?.status) as "connected" | "reconnecting" | "disconnected" | undefined,
        metadata: agentMetadata(req.body),
        surfaceId: bodyString(req.body?.surfaceId),
      });
      res.status(201).json({ agent });
    } catch (error) { res.status(400).json({ error: String(error) }); }
  });
  // Agents call this periodically (or whenever their cmux surface changes).
  // It is idempotent, so a reconnect can safely repeat registration.
  api.post("/agents/:id/heartbeat", (req, res) => {
    const existing = listRegisteredAgents(db).find((agent) => agent.id === req.params.id);
    if (!existing) { res.status(404).json({ error: "Unknown agent" }); return; }
    const surfaceId = bodyString(req.body?.surfaceId);
    if (!surfaceId && !existing.metadata.surfaceId) { res.status(400).json({ error: "surfaceId is required for an unbound agent" }); return; }
    const agent = registerAgent(db, {
      id: existing.id,
      provider: existing.provider,
      command: bodyString(req.body?.command) ?? existing.command ?? undefined,
      workspacePath: bodyString(req.body?.workspacePath) ?? existing.workspacePath ?? undefined,
      reviewSessionId: bodyString(req.body?.reviewSessionId) ?? existing.reviewSessionId ?? undefined,
      metadata: agentMetadata(req.body, existing.metadata),
      surfaceId: surfaceId ?? existing.metadata.surfaceId,
      status: "connected",
    });
    res.json({ agent });
  });
  api.post("/agents/:id/reconnect", async (req, res) => {
    try { res.json({ agent: await reconnectAgent(db, req.params.id, req.body?.dryRun === true) }); }
    catch (error) { if (error instanceof AgentRoutingError) res.status(error.statusCode).json({ error: error.message }); else res.status(502).json({ error: String(error) }); }
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
  api.get("/federation/nodes/:id/status", (req, res) => {
    try { res.json({ node: publicNode(getFederationNode(db, req.params.id)), runtime: federation.status(req.params.id) }); }
    catch (error) { res.status(404).json({ error: String(error) }); }
  });
  api.post("/federation/nodes/:id/connect", async (req, res) => {
    try {
      const node = setFederationNodeEnabled(db, req.params.id, true);
      if (!node) { res.status(404).json({ error: "Unknown federation node" }); return; }
      const tunnel = await federation.connect(req.params.id);
      res.json({ node: publicNode(node), localPort: tunnel.port, runtime: federation.status(req.params.id) });
    }
    catch (error) { res.status(502).json({ error: String(error) }); }
  });
  api.post("/federation/nodes/:id/disconnect", (req, res) => {
    if (!getFederationNode(db, req.params.id)) { res.status(404).json({ error: "Unknown federation node" }); return; }
    federation.stop(req.params.id);
    const node = setFederationNodeEnabled(db, req.params.id, false)!;
    res.json({ node: publicNode(node), runtime: federation.status(req.params.id) });
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

  // The queue home is useful before any workspace is selected. Serve the
  // shared SPA shell from the daemon itself, then delegate only API traffic
  // to the currently active workspace application.
  const clientDist = join(dirname(fileURLToPath(import.meta.url)), "..", "vendor", "difit", "dist", "client");
  // A shareable local URL is useful when all the reviewer wants is to inspect
  // a PR and ask Copilot questions. The PR URL is consumed server-side and is
  // removed before serving the client, so it is neither retained in browser
  // history nor confused with a formal queue submission.
  app.get("/review", async (req, res, next) => {
    const remoteUrl = bodyString(req.query.pr);
    if (!remoteUrl) { next(); return; }
    if (!isLoopbackRequest(req)) { res.status(403).type("text/plain").send("Local question-only PR review is available only from loopback."); return; }
    try {
      const opened = await openReadOnlyPullRequest(remoteUrl);
      res.redirect(303, opened.reviewUrl);
    } catch (error) {
      res.status(400).type("text/plain").send(`Unable to open PR for local questions: ${String(error)}`);
    }
  });
  app.use(express.static(clientDist));
  app.get(/^(?!\/api\/).*/, (_req, res) => res.sendFile(join(clientDist, "index.html")));
  // Only review API traffic reaches this dispatcher. Control endpoints above
  // remain token-protected and cannot be shadowed by a workspace app.
  app.use((req, res, next) => active ? active.workspace.app(req, res, next) : res.status(404).json({ error: "No workspace is open. Use POST /api/workspaces/open." }));
  // Watches are durable configuration, not just an in-memory CLI process.
  // Restarting the daemon resumes each enabled source monitor lazily.
  for (const watcher of db.query(`SELECT workspace_path,poll_interval_ms FROM queue_watchers WHERE enabled=1`).all() as { workspace_path: string; poll_interval_ms: number }[]) {
    startWatcher(watcher.workspace_path, watcher.poll_interval_ms);
  }
  const server = await new Promise<ReturnType<Express["listen"]>>((resolvePromise) => {
    const listener = app.listen(options.port ?? 0, options.host ?? "127.0.0.1", () => resolvePromise(listener));
  });
  // `server.close()` waits for every keep-alive/SSE browser connection. A
  // review page deliberately keeps an EventSource open, so retain sockets in
  // order to make Ctrl-C and scripted restarts deterministic instead of
  // waiting forever for a browser tab to disappear.
  const sockets = new Set<Socket>();
  server.on("connection", (socket: Socket) => {
    sockets.add(socket);
    socket.once("close", () => sockets.delete(socket));
  });
  hub = new WsHub(server, "/ws");
  const address = server.address();
  const port = typeof address === "object" && address ? address.port : options.port ?? 0;
  const discovery = { port, token, pid: process.pid, version: VERSION, createdAt: new Date().toISOString() };
  writeDiscovery(discovery);
  return {
    app,
    server,
    db,
    discovery,
    close: async () => {
      for (const workspacePath of watcherTimers.keys()) stopWatcher(workspacePath);
      // A Copilot runtime can be in the middle of a transport shutdown. Give
      // it a chance to clean up, but never let it prevent the daemon from
      // restarting; the process exit below reaps remaining children.
      if (active) {
        await Promise.race([
          active.stop().catch((error) => console.warn("Workspace shutdown failed:", error)),
          new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 2_000)),
        ]);
      }
      federation.stopAll();
      hub?.close();
      const closed = new Promise<void>((resolvePromise) => server.close(() => resolvePromise()));
      for (const socket of sockets) socket.destroy();
      // A platform-level half-open stream can still keep Node/Bun's close
      // callback from firing even after all tracked sockets are destroyed.
      // This daemon is loopback-only and the signal handler exits the process
      // immediately afterwards, so bound the wait rather than making restart
      // depend on a browser TCP teardown.
      await Promise.race([
        closed,
        new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 1_000)),
      ]);
      db.close();
    },
  };
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
