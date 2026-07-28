import type { Database } from "bun:sqlite";
import express, { type Router } from "express";
import { createHash } from "node:crypto";

import {
  listThreads,
  upsertThread,
  deleteThread,
  isThreadTombstoned,
  tombstoneThread,
  refreshOrphanFlags,
  type DiffCommentThread,
  type CommentMessage,
} from "./commentsStore.ts";

// Mirrors difit's server.ts normalizeLineValue/normalizeThreadPayload
// closely enough to accept the exact payload shapes its client already
// sends to /api/comments and /api/comment-imports.
function normalizeLine(line: unknown): DiffCommentThread["position"]["line"] {
  if (Array.isArray(line) && line.length === 2) {
    const [start, end] = line as unknown[];
    if (
      typeof start === "number" &&
      typeof end === "number" &&
      Number.isInteger(start) &&
      Number.isInteger(end) &&
      start > 0 &&
      end > 0 &&
      start <= end
    ) {
      return { start, end };
    }
  }
  if (typeof line === "number" && Number.isInteger(line) && line > 0) return line;
  return 1;
}

function normalizeThreadPayload(thread: unknown): DiffCommentThread | null {
  if (!thread || typeof thread !== "object") return null;
  const t = thread as Record<string, unknown>;
  const now = new Date().toISOString();

  const threadId =
    typeof t.id === "string" && t.id.length > 0
      ? t.id
      : createHash("sha256").update(JSON.stringify(t)).digest("hex").slice(0, 12);

  const rawMessages = Array.isArray(t.messages) ? t.messages : null;
  const messages: CommentMessage[] = rawMessages
    ? rawMessages.map((m, index) => {
        const msg = m as Record<string, unknown>;
        return {
          id: typeof msg.id === "string" && msg.id.length > 0 ? msg.id : `${threadId}:${index}`,
          body: typeof msg.body === "string" ? msg.body : "",
          author: typeof msg.author === "string" ? msg.author : undefined,
          createdAt: typeof msg.createdAt === "string" ? msg.createdAt : now,
          updatedAt: typeof msg.updatedAt === "string" ? msg.updatedAt : now,
        };
      })
    : [
        {
          id: threadId,
          body: typeof t.body === "string" ? t.body : "",
          createdAt: now,
          updatedAt: now,
        },
      ];

  const filePath =
    typeof t.filePath === "string" && t.filePath.length > 0
      ? t.filePath
      : typeof t.file === "string" && t.file.length > 0
        ? t.file
        : "<unknown file>";

  const position = t.position as Record<string, unknown> | undefined;
  const side = (position?.side ?? t.side ?? "new") === "old" ? "old" : "new";
  const line = normalizeLine(position?.line ?? t.line);

  const codeSnapshot = position
    ? (t.codeSnapshot as { content?: unknown } | undefined)
    : undefined;
  const content =
    typeof codeSnapshot?.content === "string"
      ? codeSnapshot.content
      : typeof t.codeContent === "string"
        ? t.codeContent
        : undefined;

  return {
    id: threadId,
    filePath,
    createdAt: typeof t.createdAt === "string" ? t.createdAt : now,
    updatedAt: typeof t.updatedAt === "string" ? t.updatedAt : now,
    position: { side, line },
    codeSnapshot: content !== undefined ? { content } : undefined,
    messages,
  };
}

function parseThreadsPayload(body: unknown): DiffCommentThread[] {
  const payload = typeof body === "string" ? (JSON.parse(body) as unknown) : body;
  const p = payload as { threads?: unknown[]; comments?: unknown[] };
  const source = Array.isArray(p.threads) ? p.threads : Array.isArray(p.comments) ? p.comments : [];
  return source.map(normalizeThreadPayload).filter((t): t is DiffCommentThread => t !== null);
}

export interface CommentsRouterDeps {
  db: Database;
  /** Called fresh per-request so a "New Review" mid-run is picked up immediately. */
  getSessionId: () => number;
  repoDbId: number;
  /** Current content of a (file, side, startLine..endLine) span, or undefined if it no longer exists — used for orphan re-anchoring. */
  currentContentAt: (
    filePath: string,
    side: "old" | "new",
    startLine: number,
    endLine: number,
  ) => Promise<string | undefined> | string | undefined;
  onChange?: () => void;
  /** Mirrors only formal (non-/ask) threads into the active queue item. */
  syncFormalFeedback?: (threads: DiffCommentThread[]) => Promise<void> | void;
}

/**
 * SQLite-backed replacement for difit's in-memory /api/comments* endpoints
 * (SPEC.md §3). Speaks the exact same request/response JSON shapes difit's
 * client already sends and expects, so the client comment UI needs no
 * changes — only mounted *before* the repo's difit sub-app so these routes
 * shadow its in-memory ones.
 */
export function createCommentsRouter(deps: CommentsRouterDeps): Router {
  const router = express.Router();
  router.use(express.json());
  router.use(express.text());

  let version = 0;

  async function refreshedThreads(): Promise<DiffCommentThread[]> {
    const sessionId = deps.getSessionId();
    await refreshOrphanFlags(deps.db, sessionId, deps.repoDbId, deps.currentContentAt);
    return listThreads(deps.db, sessionId, deps.repoDbId);
  }

  async function persistFormalFeedback(threads: DiffCommentThread[]): Promise<void> {
    await deps.syncFormalFeedback?.(threads);
  }

  router.get("/api/comments-json", async (_req, res) => {
    res.json({ version, threads: await refreshedThreads() });
  });

  router.get("/api/comments-output", (_req, res) => {
    res.type("text/plain");
    res.send("");
  });

  router.post("/api/comments", async (req, res) => {
    try {
      const sessionId = deps.getSessionId();
      const requestedBaseVersion = typeof req.body?.baseVersion === "number" ? req.body.baseVersion : undefined;
      // Whole-thread snapshots are convenient for the diff client, but a
      // late save from another tab must never resurrect a thread the reviewer
      // has just resolved. The client will adopt this canonical response.
      if (requestedBaseVersion !== undefined && requestedBaseVersion !== version) {
        res.status(409).json({
          error: "Comments changed in another request; refresh the local state.",
          merged: true,
          version,
          threads: await refreshedThreads(),
        });
        return;
      }
      const requestedThreads = parseThreadsPayload(req.body);
      // A tab may be fresh enough to have the current version but still hold
      // an old local thread array. Tombstones make a reviewer’s explicit
      // resolve/delete authoritative across every open tab.
      const nextThreads = requestedThreads.filter(
        (thread) => !isThreadTombstoned(deps.db, sessionId, deps.repoDbId, thread.id),
      );
      for (const thread of nextThreads) {
        upsertThread(deps.db, sessionId, deps.repoDbId, thread);
      }
      // A browser snapshot is merge-only.  It may be an empty local cache
      // while another tab has already created a comment, and treating its
      // omissions as deletions loses review work.  Deliberate removal always
      // uses DELETE /api/comments/:threadId (the bulk UI invokes it for each
      // thread), where we can durably tombstone the client-generated id.

      version += 1;
      await persistFormalFeedback(await refreshedThreads());
      deps.onChange?.();

      res.json({
        success: true,
        merged: false,
        version,
        threads: await refreshedThreads(),
      });
    } catch (error) {
      console.error("Error saving comments:", error);
      res.status(400).json({ error: "Invalid comment data" });
    }
  });

  router.delete("/api/comments/:threadId", async (req, res) => {
    const sessionId = deps.getSessionId();
    const deleted = deleteThread(deps.db, sessionId, deps.repoDbId, req.params.threadId);
    tombstoneThread(deps.db, sessionId, deps.repoDbId, req.params.threadId);
    version += 1;
    await persistFormalFeedback(await refreshedThreads());
    deps.onChange?.();
    res.json({ success: true, deleted, threadId: req.params.threadId, version });
  });

  router.post("/api/comment-imports", async (req, res) => {
    try {
      const imported = parseThreadsPayload(
        typeof req.body === "string"
          ? JSON.stringify({ threads: JSON.parse(req.body) })
          : { threads: req.body },
      );
      const sessionId = deps.getSessionId();
      for (const thread of imported) {
        if (isThreadTombstoned(deps.db, sessionId, deps.repoDbId, thread.id)) continue;
        upsertThread(deps.db, sessionId, deps.repoDbId, thread);
      }
      version += 1;
      await persistFormalFeedback(await refreshedThreads());
      deps.onChange?.();
      res.json({ success: true, changed: imported.length > 0, count: imported.length, warnings: [] });
    } catch (error) {
      console.error("Error importing comments:", error);
      res.status(400).json({ error: "Invalid comment import data" });
    }
  });

  return router;
}
