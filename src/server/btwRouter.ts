import type { Database } from "bun:sqlite";
import express, { type Router } from "express";

import { listThreads } from "./btwStore.ts";
import { getActiveSessionId } from "./commentsStore.ts";
import { BtwManager } from "./btwManager.ts";
import { TerminalBtw } from "./terminalBtw.ts";
import type { WsHub } from "./wsHub.ts";
import type { MountedRepo } from "./app.ts";

export interface BtwRouterOptions {
  db: Database;
  hub: WsHub;
  agent: string;
  workspaceRoot: string;
  dryRun: boolean;
  repos: MountedRepo[];
}

interface AskBody {
  transport?: "acp" | "terminal";
  repoId?: string;
  filePath?: string;
  startLine?: number;
  endLine?: number;
  codeContent?: string;
  question?: string;
  surfaceId?: string;
}

/**
 * Workspace-level /btw endpoints (SPEC.md §4): not repo-namespaced, since a
 * question may target the whole workspace or a specific repo.
 */
export function createBtwRouter(
  options: BtwRouterOptions,
): { router: Router; btwManager: BtwManager; terminalBtw: TerminalBtw } {
  const router = express.Router();
  router.use(express.json());

  const btwManager = new BtwManager();
  const terminalBtw = new TerminalBtw(options.db, options.hub, options.dryRun);

  router.get("/api/btw/threads", (req, res) => {
    const requestedSessionId = req.query.sessionId ? Number(req.query.sessionId) : undefined;
    const sessionId =
      requestedSessionId && Number.isInteger(requestedSessionId)
        ? requestedSessionId
        : getActiveSessionId(options.db);
    res.json({ threads: listThreads(options.db, sessionId) });
  });

  router.post("/api/btw/ask", async (req, res) => {
    const body = req.body as AskBody;
    if (!body.question || typeof body.question !== "string") {
      res.status(400).json({ error: "question is required" });
      return;
    }

    const mounted = body.repoId ? options.repos.find((r) => r.repoId === body.repoId) : undefined;
    if (body.repoId && !mounted) {
      res.status(404).json({ error: `Unknown repoId: ${body.repoId}` });
      return;
    }

    try {
      const sessionId = getActiveSessionId(options.db);
      if (body.transport === "terminal") {
        const result = await terminalBtw.ask({
          sessionId,
          repoId: mounted?.repoDbId,
          repoWorkspaceRelativePath: mounted?.repo.workspaceRelativePath,
          filePath: body.filePath,
          startLine: body.startLine,
          endLine: body.endLine,
          codeContent: body.codeContent,
          questionBody: body.question,
          surfaceId: body.surfaceId,
        });
        res.json(result);
        return;
      }

      const result = await btwManager.ask({
        db: options.db,
        sessionId,
        hub: options.hub,
        agent: options.agent,
        workspaceRoot: options.workspaceRoot,
        repoId: mounted?.repoDbId,
        repoAbsolutePath: mounted?.repo.absolutePath,
        repoWorkspaceRelativePath: mounted?.repo.workspaceRelativePath,
        filePath: body.filePath,
        startLine: body.startLine,
        endLine: body.endLine,
        codeContent: body.codeContent,
        questionBody: body.question,
        dryRun: options.dryRun,
      });
      res.json(result);
    } catch (error) {
      console.error("Error handling /btw ask:", error);
      res.status(500).json({ error: "Failed to ask /btw question" });
    }
  });

  return { router, btwManager, terminalBtw };
}
