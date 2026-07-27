import type { Database } from "bun:sqlite";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import express, { type Express } from "express";

import { createDiffApp, type DiffApp } from "../../vendor/difit/src/server/server.ts";
import {
  createCmuxService,
  createSocketConnector,
  createDryRunConnector,
} from "../../vendor/cmux-hub/cmux.ts";

import { discoverRepos, repoIdentityKey, type RepoInfo } from "./workspace.ts";
import { resolveDiffBase } from "./diffBase.ts";
import {
  upsertRepoRow,
  getActiveSessionId,
  startNewSession,
  listSessions,
  listThreads,
} from "./commentsStore.ts";
import { createCommentsRouter } from "./commentsRouter.ts";
import { createFullFileRouter } from "./fullFileRouter.ts";
import { buildExportPrompt } from "./exportFormatting.ts";
import { createBtwRouter } from "./btwRouter.ts";
import type { BtwManager } from "./btwManager.ts";
import type { TerminalBtw } from "./terminalBtw.ts";
import type { WsHub } from "./wsHub.ts";

export interface MountedRepo {
  repo: RepoInfo;
  repoId: string;
  repoDbId: number;
  diffApp: DiffApp;
}

export interface WorkspaceServerOptions {
  workspaceRoot: string;
  db: Database;
  /** Workspace-wide default diff base ('.', 'staged', 'working', or a ref). */
  base?: string;
  /** Log cmux sends instead of connecting to the real socket. */
  dryRun?: boolean;
}

/** Stable short id for a repo, derived from its durable identity (gitDir). */
export function repoIdFor(repo: Pick<RepoInfo, "gitDir">): string {
  return createHash("sha256").update(repoIdentityKey(repo)).digest("hex").slice(0, 12);
}

export interface WorkspaceApp {
  app: Express;
  repos: MountedRepo[];
  /** Wires each repo's file watcher to invalidate its cache and broadcast over `hub`. Call once `hub` (needs the listening http.Server) exists. */
  startWatchers: (hub: WsHub) => Promise<void>;
  stopWatchers: () => Promise<void>;
  /** Mounts /api/btw/* (needs `hub`, so called after listen(), like startWatchers). Safe to mount after the SPA fallback: its catch-all regex excludes /api/*. */
  startBtw: (hub: WsHub, agent: string) => { btwManager: BtwManager; terminalBtw: TerminalBtw };
}

/**
 * Discovers every repo in the workspace and mounts a `createDiffApp`
 * instance for each one under `/api/repos/<repoId>`. Each mounted app is
 * fully repo-scoped (its own GitDiffParser bound to that repo's absolute
 * path), so difit's existing path-containment checks
 * (`parseRepositoryRelativePath`) already prevent one repo's routes from
 * reading another repo's — or the workspace's — files.
 */
export async function buildWorkspaceApp(options: WorkspaceServerOptions): Promise<WorkspaceApp> {
  const repos = await discoverRepos(options.workspaceRoot);
  const app = express();
  const mounted: MountedRepo[] = [];
  const { db } = options;
  const getSessionId = () => getActiveSessionId(db);

  for (const repo of repos) {
    const repoId = repoIdFor(repo);
    const repoDbId = upsertRepoRow(db, repo);
    const { selection, diffMode } = resolveDiffBase(options.base ?? ".");

    const diffApp = await createDiffApp({
      repoPath: repo.absolutePath,
      selection,
      diffMode,
      openBrowser: false,
    });

    const commentsRouter = createCommentsRouter({
      db,
      getSessionId,
      repoDbId,
      currentContentAt: async (filePath, side, startLine, endLine) => {
        try {
          const text =
            side === "old"
              ? (await diffApp.parser.getBlobContent(filePath, "HEAD")).toString("utf-8")
              : readFileSync(join(repo.absolutePath, filePath), "utf-8");
          const lines = text.split("\n");
          const slice = lines.slice(startLine - 1, endLine);
          return slice.length > 0 ? slice.join("\n") : undefined;
        } catch {
          return undefined;
        }
      },
    });

    // Mounted before diffApp.app so these SQLite-backed routes shadow
    // difit's own in-memory /api/comments* endpoints (SPEC.md §3).
    const fullFileRouter = createFullFileRouter(diffApp, selection);

    app.use(`/api/repos/${repoId}`, commentsRouter);
    app.use(`/api/repos/${repoId}`, fullFileRouter);
    app.use(`/api/repos/${repoId}`, diffApp.app);
    mounted.push({ repo, repoId, repoDbId, diffApp });
  }

  app.get("/api/repos", (_req, res) => {
    res.json({
      workspaceRoot: options.workspaceRoot,
      repos: mounted.map((m) => ({
        id: m.repoId,
        workspaceRelativePath: m.repo.workspaceRelativePath,
        remoteUrl: m.repo.remoteUrl,
        changeCount: m.diffApp.initialDiffData.files.length,
        files: m.diffApp.initialDiffData.files.map((f) => f.path),
      })),
    });
  });

  function sessionIdFromQuery(req: { query: Record<string, unknown> }): number {
    const raw = req.query.sessionId ? Number(req.query.sessionId) : undefined;
    return raw && Number.isInteger(raw) ? raw : getSessionId();
  }

  app.get("/api/export/prompt", (req, res) => {
    const forSession = sessionIdFromQuery(req);
    const prompt = buildExportPrompt(
      mounted.map((m) => ({
        repoWorkspaceRelativePath: m.repo.workspaceRelativePath,
        threads: listThreads(db, forSession, m.repoDbId),
      })),
    );
    res.type("text/plain").send(prompt);
  });

  app.post("/api/export", express.json(), async (req, res) => {
    const destination = (req.body as { destination?: unknown })?.destination;
    if (destination !== "clipboard" && destination !== "cmux") {
      res.status(400).json({ error: "destination must be 'clipboard' or 'cmux'" });
      return;
    }

    const forSession = sessionIdFromQuery(req);
    const content = buildExportPrompt(
      mounted.map((m) => ({
        repoWorkspaceRelativePath: m.repo.workspaceRelativePath,
        threads: listThreads(db, forSession, m.repoDbId),
      })),
    );

    if (destination === "cmux") {
      const cmux = createCmuxService(
        options.dryRun ? createDryRunConnector() : createSocketConnector(),
      );
      try {
        await cmux.sendText(content);
      } catch (error) {
        res.status(502).json({ error: `Failed to send to cmux: ${String(error)}` });
        return;
      }
    }

    db.query(
      `INSERT INTO exports (session_id, content, destination, created_at) VALUES (?, ?, ?, ?)`,
    ).run(forSession, content, destination, Date.now());

    res.json({ success: true, content });
  });

  app.get("/api/sessions", (_req, res) => {
    res.json({ sessions: listSessions(db), activeSessionId: getSessionId() });
  });

  app.post("/api/sessions/new", express.json(), (req, res) => {
    const label = (req.body as { label?: unknown })?.label;
    const id = startNewSession(db, typeof label === "string" ? label : undefined);
    res.json({ sessionId: id });
  });

  const clientDist = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "vendor", "difit", "dist", "client");
  app.use(express.static(clientDist));
  app.get(/^(?!\/api\/).*/, (_req, res) => {
    res.sendFile(join(clientDist, "index.html"));
  });

  async function startWatchers(hub: WsHub): Promise<void> {
    for (const m of mounted) {
      const { diffMode } = resolveDiffBase(options.base ?? ".");
      try {
        await m.diffApp.fileWatcher.start(diffMode, m.repo.absolutePath, 300, () => {
          m.diffApp.invalidateCache();
          hub.broadcast({ type: "diff-updated", repoId: m.repoId });
        });
      } catch (error) {
        console.warn(`⚠️  File watcher failed to start for ${m.repo.workspaceRelativePath}:`, error);
      }
    }
  }

  async function stopWatchers(): Promise<void> {
    await Promise.all(mounted.map((m) => m.diffApp.fileWatcher.stop()));
  }

  function startBtw(hub: WsHub, agent: string) {
    const { router, btwManager, terminalBtw } = createBtwRouter({
      db,
      hub,
      agent,
      workspaceRoot: options.workspaceRoot,
      dryRun: options.dryRun ?? false,
      repos: mounted,
    });
    app.use(router);
    return { btwManager, terminalBtw };
  }

  return { app, repos: mounted, startWatchers, stopWatchers, startBtw };
}
