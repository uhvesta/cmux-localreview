import { createHash, randomUUID } from "node:crypto";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";

import type { QueueItem } from "./queueStore.ts";
import { runCommand } from "./gitExec.ts";

export interface RemotePullRequest {
  /** Canonical, browser-safe GitHub PR URL returned by gh. */
  url: string;
  number: number;
  title: string;
  body: string;
  state: string;
  isDraft: boolean;
  repository: string;
  repositoryUrl: string;
  headRefName: string;
  headSha: string;
  baseRefName: string;
  baseSha: string;
}

export interface RemoteWorkspace {
  workspacePath: string;
  mirrorPath: string;
  worktreePath: string;
  pullRequest: RemotePullRequest;
}

export interface RemoteReviewResult {
  method: "github-review-api" | "gh-pr-review-fallback";
  inlineComments: number;
}

type GhPrJson = {
  url: string;
  number: number;
  title: string;
  body: string | null;
  state: string;
  isDraft: boolean;
  headRefName: string;
  baseRefName: string;
};

type GhPullApiJson = {
  head: { sha: string };
  base: {
    sha: string;
    repo: { full_name: string; ssh_url?: string | null; clone_url?: string | null; html_url?: string | null } | null;
  };
};

// An override is useful for isolated runners and tests.  Production continues
// to use the per-user cache and never writes inside a submitted repository.
const CACHE_ROOT = process.env.CMUX_LOCALREVIEW_CACHE_DIR
  ? resolve(process.env.CMUX_LOCALREVIEW_CACHE_DIR)
  : join(homedir(), ".cache", "cmux-localreview");

function commandError(label: string, result: { stderr: string; stdout: string }): Error {
  return new Error(`${label}: ${(result.stderr.trim() || result.stdout.trim() || "unknown error")}`);
}

/** Resolve a PR URL through the user's authenticated `gh` installation. */
export async function resolveRemotePullRequest(remoteUrl: string, cwd?: string): Promise<RemotePullRequest> {
  const result = await runCommand([
    "gh", "pr", "view", remoteUrl,
    "--json", "url,number,title,body,state,isDraft,headRefName,baseRefName",
  ], cwd);
  if (result.exitCode !== 0) throw commandError(`Unable to resolve GitHub pull request ${remoteUrl}`, result);

  let value: GhPrJson;
  try { value = JSON.parse(result.stdout) as GhPrJson; } catch { throw new Error(`gh returned invalid PR metadata for ${remoteUrl}`); }
  let url: URL;
  try { url = new URL(value.url); } catch { throw new Error(`gh returned an invalid PR URL for ${remoteUrl}`); }
  const parts = url.pathname.split("/").filter(Boolean);
  if (parts.length < 4 || parts[2] !== "pull") throw new Error(`gh returned a non-standard PR URL for ${remoteUrl}`);
  const repositoryPath = `${parts[0]}/${parts[1]}`;
  const details = await runCommand(["gh", "api", `repos/${repositoryPath}/pulls/${value.number}`], cwd);
  if (details.exitCode !== 0) throw commandError(`Unable to resolve GitHub commit metadata for ${value.url}`, details);
  let api: GhPullApiJson;
  try { api = JSON.parse(details.stdout) as GhPullApiJson; } catch { throw new Error(`gh returned invalid pull-request API metadata for ${value.url}`); }
  const repository = api.base.repo;
  const repositoryUrl = repository?.ssh_url ?? repository?.clone_url ?? repository?.html_url;
  if (!repository?.full_name || !repositoryUrl || !api.head?.sha || !api.base?.sha) {
    throw new Error(`GitHub pull request ${remoteUrl} has incomplete repository or commit metadata`);
  }
  return {
    url: value.url,
    number: value.number,
    title: value.title,
    body: value.body ?? "",
    state: value.state,
    isDraft: value.isDraft,
    repository: repository.full_name,
    repositoryUrl,
    headRefName: value.headRefName,
    headSha: api.head.sha,
    baseRefName: value.baseRefName,
    baseSha: api.base.sha,
  };
}

function cacheKey(pr: RemotePullRequest): string {
  // `owner/repo` is only unique within a GitHub host.  Including the host
  // prevents a GitHub Enterprise PR from reusing (and retargeting) a mirror
  // belonging to github.com with the same repository name.
  let host = "github.com";
  try { host = new URL(pr.url).host.toLowerCase(); } catch { /* validated at resolution time */ }
  return createHash("sha256").update(`github:${host}:${pr.repository.toLowerCase()}`).digest("hex");
}

export function remoteWorkspacePaths(pr: RemotePullRequest): Pick<RemoteWorkspace, "mirrorPath" | "worktreePath"> {
  const key = cacheKey(pr);
  const mirrorPath = join(CACHE_ROOT, "mirrors", `${key}.git`);
  // A commit-specific worktree never mutates a review that is already open.
  // It also makes returning to an older queued review reproducible until the
  // caller explicitly cleans it up.
  const worktreePath = join(CACHE_ROOT, "worktrees", key, `pr-${pr.number}`, pr.headSha);
  return { mirrorPath, worktreePath };
}

async function ensureMirror(pr: RemotePullRequest, mirrorPath: string): Promise<void> {
  mkdirSync(join(CACHE_ROOT, "mirrors"), { recursive: true, mode: 0o700 });
  if (!existsSync(mirrorPath)) {
    const clone = await runCommand(["git", "clone", "--mirror", pr.repositoryUrl, mirrorPath]);
    if (clone.exitCode !== 0) throw commandError(`Unable to mirror ${pr.repository}`, clone);
  } else {
    // A renamed GitHub repository can retain its old remote in an existing
    // cache. Keep the cache keyed by repository identity but refresh origin.
    const setUrl = await runCommand(["git", "--git-dir", mirrorPath, "remote", "set-url", "origin", pr.repositoryUrl]);
    if (setUrl.exitCode !== 0) throw commandError(`Unable to update mirror origin for ${pr.repository}`, setUrl);
  }
  const fetch = await runCommand(["git", "--git-dir", mirrorPath, "fetch", "--prune", "origin"]);
  if (fetch.exitCode !== 0) throw commandError(`Unable to update mirror for ${pr.repository}`, fetch);
  // Fetch the PR ref explicitly: its head may live in a fork and therefore
  // not be reachable through ordinary origin branch refs.
  const pullRef = `+refs/pull/${pr.number}/head:refs/cmux-localreview/pulls/${pr.number}/head`;
  const pullFetch = await runCommand(["git", "--git-dir", mirrorPath, "fetch", "origin", pullRef]);
  if (pullFetch.exitCode !== 0) throw commandError(`Unable to fetch PR #${pr.number} head`, pullFetch);
  const exists = await runCommand(["git", "--git-dir", mirrorPath, "cat-file", "-e", `${pr.headSha}^{commit}`]);
  if (exists.exitCode !== 0) throw new Error(`Fetched PR #${pr.number}, but commit ${pr.headSha} is unavailable in the mirror`);
}

/** Materialize the exact resolved PR head in an isolated cache worktree. */
export async function prepareRemoteWorkspace(pr: RemotePullRequest): Promise<RemoteWorkspace> {
  const { mirrorPath, worktreePath } = remoteWorkspacePaths(pr);
  mkdirSync(join(CACHE_ROOT, "worktrees", cacheKey(pr), `pr-${pr.number}`), { recursive: true, mode: 0o700 });
  await ensureMirror(pr, mirrorPath);
  if (!existsSync(worktreePath)) {
    const add = await runCommand(["git", "--git-dir", mirrorPath, "worktree", "add", "--detach", worktreePath, pr.headSha]);
    if (add.exitCode !== 0) throw commandError(`Unable to create isolated worktree for PR #${pr.number}`, add);
  } else {
    // A previous interrupted run may have left this checkout at a different
    // revision; re-pin it without touching user worktrees.
    const checkout = await runCommand(["git", "-C", worktreePath, "checkout", "--detach", "--force", pr.headSha]);
    if (checkout.exitCode !== 0) throw commandError(`Unable to update cached worktree for PR #${pr.number}`, checkout);
  }
  // A mirror stores the base branch as refs/heads/<name>, but a reviewer (and
  // users selecting `origin/main`) expects the conventional remote-tracking
  // name. The detached PR worktree otherwise reports origin/main as an
  // unknown revision even though the exact base commit is already present.
  const trackingRef = `refs/remotes/origin/${pr.baseRefName}`;
  const pinBase = await runCommand(["git", "-C", worktreePath, "update-ref", trackingRef, pr.baseSha]);
  if (pinBase.exitCode !== 0) throw commandError(`Unable to pin base ref for PR #${pr.number}`, pinBase);
  return { workspacePath: worktreePath, mirrorPath, worktreePath, pullRequest: pr };
}

/** Remove a managed, commit-specific cache worktree. Mirrors stay reusable. */
export async function cleanupRemoteWorkspace(pr: RemotePullRequest, options: { removeMirror?: boolean } = {}): Promise<{ worktreeRemoved: boolean; mirrorRemoved: boolean }> {
  const { mirrorPath, worktreePath } = remoteWorkspacePaths(pr);
  let worktreeRemoved = false;
  if (existsSync(worktreePath)) {
    const remove = await runCommand(["git", "--git-dir", mirrorPath, "worktree", "remove", "--force", worktreePath]);
    if (remove.exitCode !== 0) throw commandError(`Unable to remove cached worktree for PR #${pr.number}`, remove);
    worktreeRemoved = true;
  }
  const removeMirror = options.removeMirror === true;
  let mirrorRemoved = false;
  if (removeMirror && existsSync(mirrorPath)) {
    // Only delete cache-owned paths derived above, never a caller-provided path.
    rmSync(resolve(mirrorPath), { recursive: true, force: true });
    mirrorRemoved = true;
  }
  return { worktreeRemoved, mirrorRemoved };
}

export function remotePullRequestFromQueueItem(item: QueueItem): RemotePullRequest | undefined {
  const manifest = item.snapshotManifest as { remotePullRequest?: unknown } | null;
  const value = manifest?.remotePullRequest;
  if (!value || typeof value !== "object") return undefined;
  const pr = value as Partial<RemotePullRequest>;
  if (typeof pr.url !== "string" || typeof pr.number !== "number" || typeof pr.repository !== "string" || typeof pr.repositoryUrl !== "string" || typeof pr.headSha !== "string" || typeof pr.baseSha !== "string" || typeof pr.title !== "string" || typeof pr.body !== "string" || typeof pr.state !== "string" || typeof pr.isDraft !== "boolean" || typeof pr.headRefName !== "string" || typeof pr.baseRefName !== "string") return undefined;
  return pr as RemotePullRequest;
}

function reviewBody(item: QueueItem, feedback: { body: string; path: string | null; line: number | null }[]): string {
  const summary = feedback
    .filter((entry) => !entry.path || !entry.line)
    .map((entry) => entry.path ? `- ${entry.path}: ${entry.body}` : `- ${entry.body}`);
  return [item.decisionBody, summary.length ? `General feedback:\n${summary.join("\n")}` : ""].filter(Boolean).join("\n\n") || "Reviewed with cmux-localreview.";
}

/**
 * A review is pinned to the immutable head that was opened in the UI.  GitHub
 * would otherwise let the `gh pr review` fallback review whatever head happens
 * to be current, which can silently publish comments for a newer revision.
 */
async function assertReviewHeadIsCurrent(pr: RemotePullRequest, cwd?: string): Promise<void> {
  const current = await resolveRemotePullRequest(pr.url, cwd);
  if (current.state.toUpperCase() !== "OPEN") {
    throw new Error(`GitHub pull request #${pr.number} is ${current.state.toLowerCase()}; refresh the queue before publishing a review`);
  }
  if (current.headSha !== pr.headSha) {
    throw new Error(`GitHub pull request #${pr.number} changed from ${pr.headSha.slice(0, 12)} to ${current.headSha.slice(0, 12)}; refresh the queue before publishing a review`);
  }
}

/**
 * Publish a GitHub review. The Reviews API supports inline path/line comments
 * tied to the resolved head SHA; if GitHub rejects an invalid/outdated line,
 * fall back to the portable `gh pr review` summary rather than losing a review.
 */
export async function submitRemoteDecision(item: QueueItem, decision: "approved" | "changes_requested", feedback: { body: string; path: string | null; line: number | null }[]): Promise<RemoteReviewResult> {
  if (!item.remoteUrl) throw new Error("Remote review is missing its PR URL");
  const pr = remotePullRequestFromQueueItem(item) ?? await resolveRemotePullRequest(item.remoteUrl, item.workspacePath);
  await assertReviewHeadIsCurrent(pr, item.workspacePath);
  const inline = feedback.filter((entry) => entry.path && entry.line && entry.line > 0).map((entry) => ({
    path: entry.path!, line: entry.line!, side: "RIGHT", body: entry.body,
  }));
  const body = reviewBody(item, feedback);
  const payload = {
    commit_id: pr.headSha,
    event: decision === "approved" ? "APPROVE" : "REQUEST_CHANGES",
    body,
    ...(inline.length ? { comments: inline } : {}),
  };
  const temporaryPayload = join(CACHE_ROOT, `github-review-${process.pid}-${randomUUID()}.json`);
  mkdirSync(CACHE_ROOT, { recursive: true, mode: 0o700 });
  writeFileSync(temporaryPayload, JSON.stringify(payload), { encoding: "utf8", mode: 0o600 });
  const endpoint = `repos/${pr.repository}/pulls/${pr.number}/reviews`;
  try {
    const api = await runCommand(["gh", "api", "--method", "POST", endpoint, "--input", temporaryPayload], item.workspacePath);
    if (api.exitCode === 0) return { method: "github-review-api", inlineComments: inline.length };
    // The summary fallback remains valuable for comments against removed or
    // stale lines, which cannot be represented by the current feedback table.
    const comments = feedback.map((entry) => entry.path ? `- ${entry.path}${entry.line ? `:${entry.line}` : ""}: ${entry.body}` : `- ${entry.body}`).join("\n");
    // `body` already has the general feedback, so build the fallback from the
    // reviewer summary plus one complete, non-duplicated comment list.
    const fallbackBody = [item.decisionBody, comments ? `Feedback:\n${comments}` : ""]
      .filter(Boolean).join("\n\n") || "Reviewed with cmux-localreview.";
    const flag = decision === "approved" ? "--approve" : "--request-changes";
    const fallback = await runCommand(["gh", "pr", "review", pr.url, flag, "--body", fallbackBody], item.workspacePath);
    if (fallback.exitCode !== 0) throw new Error(`GitHub review API failed (${api.stderr.trim() || api.stdout.trim()}); fallback failed: ${fallback.stderr.trim() || fallback.stdout.trim()}`);
    return { method: "gh-pr-review-fallback", inlineComments: 0 };
  } finally {
    rmSync(temporaryPayload, { force: true });
  }
}
