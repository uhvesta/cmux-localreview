import { createHash } from "node:crypto";

import { runCommand } from "./gitExec.ts";
import { discoverRepos } from "./workspace.ts";

/**
 * Digest the exact index + worktree status visible to a review snapshot.
 * Porcelain output includes staged, unstaged and untracked paths; the repo
 * location is included so sibling repositories cannot collide.
 */
export async function workspaceSourceFingerprint(workspacePath: string): Promise<string> {
  const repos = await discoverRepos(workspacePath);
  if (repos.length === 0) throw new Error(`No Git repositories found below ${workspacePath}`);
  const state: string[] = [];
  for (const repo of repos) {
    const [head, status] = await Promise.all([
      runCommand(["git", "rev-parse", "HEAD"], repo.absolutePath),
      runCommand(["git", "status", "--porcelain=v1", "--untracked-files=all"], repo.absolutePath),
    ]);
    if (head.exitCode !== 0 || status.exitCode !== 0) {
      throw new Error(`Unable to read Git state for ${repo.absolutePath}: ${(head.stderr || status.stderr).trim()}`);
    }
    state.push(`${repo.workspaceRelativePath}\0${head.stdout.trim()}\0${status.stdout}`);
  }
  return createHash("sha256").update(state.join("\n---\n")).digest("hex");
}
