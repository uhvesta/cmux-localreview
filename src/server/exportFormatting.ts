import { formatCommentThreadPrompt } from "../../vendor/difit/src/utils/commentFormatting.ts";
import type { CommentThread, LineNumber } from "../../vendor/difit/src/types/diff.ts";

import type { DiffCommentThread } from "./commentsStore.ts";

export interface RepoThreads {
  repoWorkspaceRelativePath: string;
  threads: DiffCommentThread[];
}

function toLineNumber(line: DiffCommentThread["position"]["line"]): LineNumber {
  return typeof line === "number" ? line : [line.start, line.end];
}

function toCommentThread(repoPath: string, thread: DiffCommentThread): CommentThread {
  const prefix = repoPath === "." || repoPath === "" ? "" : `${repoPath}/`;
  return {
    id: thread.id,
    file: `${prefix}${thread.filePath}`,
    line: toLineNumber(thread.position.line),
    side: thread.position.side,
    createdAt: thread.createdAt,
    updatedAt: thread.updatedAt,
    codeContent: thread.codeSnapshot?.content,
    messages: thread.messages,
  };
}

function firstLine(line: LineNumber): number {
  return typeof line === "number" ? line : line[0];
}

/**
 * SPEC.md §3: one prompt aggregating every comment across every repo in the
 * workspace, grouped by repo then file, each entry prefixed with
 * `<workspace-relative-repo>/<file>:<line-range>`.
 */
export function buildExportPrompt(reposWithThreads: RepoThreads[]): string {
  const allThreads: CommentThread[] = [];
  for (const { repoWorkspaceRelativePath, threads } of reposWithThreads) {
    for (const thread of threads) {
      allThreads.push(toCommentThread(repoWorkspaceRelativePath, thread));
    }
  }

  if (allThreads.length === 0) return "";

  allThreads.sort((a, b) => {
    if (a.file !== b.file) return a.file < b.file ? -1 : 1;
    return firstLine(a.line) - firstLine(b.line);
  });

  return allThreads.map((thread) => formatCommentThreadPrompt(thread)).join("\n=====\n");
}
