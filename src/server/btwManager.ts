import type { Database } from "bun:sqlite";
import type { SessionNotification } from "@zed-industries/agent-client-protocol";

import { AcpSession } from "./acp.ts";
import { resolveAgentCommand } from "./agentProvider.ts";
import * as store from "./btwStore.ts";
import type { WsHub } from "./wsHub.ts";

export interface AskBtwOptions {
  db: Database;
  sessionId: number;
  hub: WsHub;
  agent: string;
  workspaceRoot: string;
  repoId?: number;
  repoAbsolutePath?: string;
  repoWorkspaceRelativePath?: string;
  filePath?: string;
  startLine?: number;
  endLine?: number;
  codeContent?: string;
  questionBody: string;
  dryRun?: boolean;
}

function extractAgentText(notification: SessionNotification): string | null {
  const update = notification.update;
  if (update.sessionUpdate !== "agent_message_chunk") return null;
  const content = update.content;
  if (!content || Array.isArray(content) || content.type !== "text") return null;
  return content.text;
}

function formatBtwPrompt(options: AskBtwOptions): string {
  const lines: string[] = [];
  if (options.filePath) {
    const repoPrefix =
      options.repoWorkspaceRelativePath && options.repoWorkspaceRelativePath !== "."
        ? `${options.repoWorkspaceRelativePath}/`
        : "";
    const lineInfo =
      options.startLine != null
        ? options.endLine != null && options.endLine !== options.startLine
          ? `:L${options.startLine}-L${options.endLine}`
          : `:L${options.startLine}`
        : "";
    lines.push(`Context: ${repoPrefix}${options.filePath}${lineInfo}`);
    if (options.codeContent) {
      lines.push("```");
      lines.push(options.codeContent);
      lines.push("```");
    }
    lines.push("");
  }
  lines.push(
    "This is a side question about the code above (via /btw) — please answer it directly; " +
      "this is not a request to make any edits.",
  );
  lines.push("");
  lines.push(options.questionBody);
  return lines.join("\n");
}

/**
 * Owns ACP sessions for /btw threads (SPEC.md §4). Each thread gets its own
 * ACP session (spawned lazily on its first question); the session id is
 * persisted so a restart can attempt `session/load` to resume it — most
 * agents don't support that, which is expected, not an error.
 */
export class BtwManager {
  private liveSessions = new Map<number, AcpSession>();

  async ask(options: AskBtwOptions): Promise<{ threadId: number; questionId: number }> {
    const threadId = store.createThread(options.db, {
      sessionId: options.sessionId,
      transport: "acp",
      acpProvider: options.agent,
      repoId: options.repoId,
      filePath: options.filePath,
      startLine: options.startLine,
      endLine: options.endLine,
    });
    const questionId = store.addQuestion(options.db, threadId, options.questionBody);
    const answerId = store.createPendingAnswer(options.db, questionId);

    this.runTurn(options, threadId, answerId).catch((error: unknown) => {
      const message = error instanceof Error ? error.message : String(error);
      console.error("btw turn failed:", error);
      store.appendAnswerChunk(options.db, answerId, `\n\n[error: ${message}]`);
      store.finalizeAnswer(options.db, answerId);
      options.hub.broadcast({ type: "btw-update", threadId });
    });

    return { threadId, questionId };
  }

  private async runTurn(options: AskBtwOptions, threadId: number, answerId: number): Promise<void> {
    const { db, hub } = options;
    const prompt = formatBtwPrompt(options);

    if (options.dryRun) {
      console.log(`[acp dry-run] would ask "${options.agent}":\n${prompt}`);
      store.appendAnswerChunk(db, answerId, `[dry-run] would ask the "${options.agent}" agent this question.`);
      store.finalizeAnswer(db, answerId);
      hub.broadcast({ type: "btw-update", threadId });
      return;
    }

    const cwd = options.repoAbsolutePath ?? options.workspaceRoot;
    const { command, args } = resolveAgentCommand(options.agent);

    const session = await AcpSession.spawn({
      command,
      args,
      cwd,
      resumeSessionId: store.getThreadAcpSessionId(db, threadId) ?? undefined,
      onUpdate: (notification) => {
        const text = extractAgentText(notification);
        if (text) {
          store.appendAnswerChunk(db, answerId, text);
          hub.broadcast({ type: "btw-update", threadId });
        }
      },
    });

    this.liveSessions.set(threadId, session);
    store.setThreadAcpSessionId(db, threadId, session.sessionId);

    try {
      await session.prompt(prompt);
    } finally {
      store.finalizeAnswer(db, answerId);
      hub.broadcast({ type: "btw-update", threadId });
    }
  }

  disposeAll(): void {
    for (const session of this.liveSessions.values()) session.dispose();
    this.liveSessions.clear();
  }
}
