import { createHash } from "node:crypto";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import express, { type Express } from "express";

import { createDiffApp, type DiffApp } from "../../vendor/difit/src/server/server.ts";

import { discoverRepos, repoIdentityKey, type RepoInfo } from "./workspace.ts";
import { resolveDiffBase } from "./diffBase.ts";
import type { WsHub } from "./wsHub.ts";

export interface MountedRepo {
  repo: RepoInfo;
  repoId: string;
  diffApp: DiffApp;
}

export interface WorkspaceServerOptions {
  workspaceRoot: string;
  /** Workspace-wide default diff base ('.', 'staged', 'working', or a ref). */
  base?: string;
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

  for (const repo of repos) {
    const repoId = repoIdFor(repo);
    const { selection, diffMode } = resolveDiffBase(options.base ?? ".");

    const diffApp = await createDiffApp({
      repoPath: repo.absolutePath,
      selection,
      diffMode,
      openBrowser: false,
    });

    app.use(`/api/repos/${repoId}`, diffApp.app);
    mounted.push({ repo, repoId, diffApp });
  }

  app.get("/api/repos", (_req, res) => {
    res.json({
      workspaceRoot: options.workspaceRoot,
      repos: mounted.map((m) => ({
        id: m.repoId,
        workspaceRelativePath: m.repo.workspaceRelativePath,
        remoteUrl: m.repo.remoteUrl,
        changeCount: m.diffApp.initialDiffData.files.length,
      })),
    });
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

  return { app, repos: mounted, startWatchers, stopWatchers };
}
