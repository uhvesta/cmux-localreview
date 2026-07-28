import type { Database } from "bun:sqlite";
import { existsSync, mkdirSync, readdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { createReviewWatcher } from "../../vendor/cmux-hub/review-watcher.ts";
import {
  createCmuxService,
  createDryRunConnector,
  createSocketConnector,
} from "../../vendor/cmux-hub/cmux.ts";

import * as store from "./btwStore.ts";
import type { WsHub } from "./wsHub.ts";

function sanitizeSegment(value: string): string {
  return value.replace(/[^A-Za-z0-9._-]/g, "_");
}

/**
 * Binding order mirrors cmux-hub's `resolveDefaultReviewDir` (see
 * ../../vendor/cmux-hub/review.ts) — override not applicable here, so:
 * CMUX_WORKSPACE_ID -> CMUX_SURFACE_ID -> pid. Rooted separately from
 * cmux-hub's own review dir since these are unrelated features.
 */
export function resolveBtwDir(): string {
  const root = join(tmpdir(), "cmux-localreview-btw");
  const workspaceId = process.env.CMUX_WORKSPACE_ID;
  if (workspaceId) return join(root, `workspace-${sanitizeSegment(workspaceId)}`);
  const surfaceId = process.env.CMUX_SURFACE_ID;
  if (surfaceId) return join(root, `surface-${sanitizeSegment(surfaceId)}`);
  return join(root, `pid-${process.pid}`);
}

export interface TerminalAskOptions {
  sessionId: number;
  repoId?: number;
  repoWorkspaceRelativePath?: string;
  filePath?: string;
  startLine?: number;
  endLine?: number;
  codeContent?: string;
  questionBody: string;
  /** These are resolved by the durable agent registry before delivery. */
  targetAgentId: string;
  surfaceId: string;
}

/**
 * Secondary /btw transport (SPEC.md §4): injects the question into the
 * existing cmux terminal surface via `sendText`, instructing the agent to
 * write its answer to a per-question markdown file; a vendored
 * review-watcher picks up the file and finalizes the answer.
 */
export class TerminalBtw {
  private dir: string;
  private watcher: ReturnType<typeof createReviewWatcher>;

  constructor(
    private db: Database,
    private hub: WsHub,
    private dryRun: boolean,
  ) {
    this.dir = resolveBtwDir();
    mkdirSync(this.dir, { recursive: true });
    this.watcher = createReviewWatcher([this.dir], () => this.checkForAnswers());
    this.watcher.start();
  }

  async ask(options: TerminalAskOptions): Promise<{ threadId: number; questionId: number }> {
    if (!options.targetAgentId || !options.surfaceId) {
      throw new Error("Terminal /btw requires an explicit registered agent and cmux surface; refusing to fall back to the focused terminal.");
    }
    const threadId = store.createThread(this.db, {
      sessionId: options.sessionId,
      transport: "terminal",
      repoId: options.repoId,
      filePath: options.filePath,
      startLine: options.startLine,
      endLine: options.endLine,
      targetAgentId: options.targetAgentId,
    });
    const questionId = store.addQuestion(this.db, threadId, options.questionBody);
    store.createPendingAnswer(this.db, questionId);

    const answerPath = join(this.dir, `${questionId}.md`);
    const text = this.formatTerminalMessage(options, answerPath);

    const cmux = createCmuxService(this.dryRun ? createDryRunConnector() : createSocketConnector());
    try {
      await cmux.sendText(text, options.surfaceId);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      store.failAnswer(this.db, questionId, message);
      this.hub.broadcast({ type: "btw-update", threadId });
      throw error;
    }

    return { threadId, questionId };
  }

  private formatTerminalMessage(options: TerminalAskOptions, answerPath: string): string {
    const lines: string[] = [];
    if (options.filePath) {
      const prefix =
        options.repoWorkspaceRelativePath && options.repoWorkspaceRelativePath !== "."
          ? `${options.repoWorkspaceRelativePath}/`
          : "";
      const lineInfo =
        options.startLine != null
          ? options.endLine != null && options.endLine !== options.startLine
            ? `:L${options.startLine}-L${options.endLine}`
            : `:L${options.startLine}`
          : "";
      lines.push(`/btw ${prefix}${options.filePath}${lineInfo}`);
      if (options.codeContent) {
        lines.push("```");
        lines.push(options.codeContent);
        lines.push("```");
      }
    } else {
      lines.push("/btw");
    }
    lines.push("");
    lines.push(options.questionBody);
    lines.push("");
    lines.push(`When you have an answer, write it as markdown to: ${answerPath}`);
    return lines.join("\n");
  }

  private checkForAnswers(): void {
    let files: string[];
    try {
      files = readdirSync(this.dir).filter((f) => f.endsWith(".md"));
    } catch {
      return;
    }

    for (const file of files) {
      const questionId = Number.parseInt(file.slice(0, -".md".length), 10);
      if (!Number.isInteger(questionId)) continue;

      const path = join(this.dir, file);
      if (!existsSync(path)) continue;

      let content: string;
      try {
        content = readFileSync(path, "utf-8");
      } catch {
        continue;
      }
      if (!content.trim()) continue;

      const changed = this.db
        .query(
          `UPDATE btw_answers SET body = ?, pending = 0, updated_at = ?
           WHERE question_id = ? AND (body != ? OR pending = 1)`,
        )
        .run(content, Date.now(), questionId, content);

      if (changed.changes > 0) {
        const threadRow = this.db
          .query(`SELECT thread_id FROM btw_questions WHERE id = ?`)
          .get(questionId) as { thread_id: number } | null;
        if (threadRow) {
          this.hub.broadcast({ type: "btw-update", threadId: threadRow.thread_id });
        }
      }
    }
  }

  stop(): void {
    this.watcher.stop();
  }
}
