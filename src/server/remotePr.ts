import { createHash } from "node:crypto";
import { existsSync, mkdirSync, rmSync } from "node:fs";
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
  method: "github-review-api";
  inlineComments: number;
}

type GitHubPullApiJson = {
  number: number;
  html_url: string;
  title: string;
  body: string | null;
  state: string;
  draft: boolean;
  head: { sha: string; ref: string };
  base: {
    sha: string; ref: string;
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

function pullRequestTarget(remoteUrl: string): { host: string; repository: string; number: number } {
  let url: URL;
  try { url = new URL(remoteUrl); } catch { throw new Error(`Invalid GitHub pull-request URL: ${remoteUrl}`); }
  const parts = url.pathname.split("/").filter(Boolean);
  if (parts.length < 4 || parts[2] !== "pull" || !/^\d+$/.test(parts[3]!)) throw new Error(`Expected a GitHub pull-request URL: ${remoteUrl}`);
  const host = url.host.toLowerCase();
  // A user token for github.com must never be sent to a URL supplied by a
  // queue form, CLI argument, or local browser navigation. Enterprise support
  // needs a separately configured, host-scoped credential provider; until it
  // exists, failing closed is the only safe behavior.
  if (host !== "github.com") throw new Error("Only github.com pull-request URLs are supported until GitHub Enterprise host-scoped credentials are configured.");
  return { host, repository: `${parts[0]}/${parts[1]}`, number: Number(parts[3]) };
}

function githubApiBase(host: string): string {
  // GitHub.com serves REST at the root of api.github.com. GitHub Enterprise
  // Server keeps the /api/v3 prefix on the appliance host.
  return host === "github.com" ? "https://api.github.com" : `https://${host}/api/v3`;
}

type GitHubFetch = (input: string | URL | Request, init?: RequestInit) => Promise<Response>;
let githubFetch: GitHubFetch = fetch;

/** Test seam only; production always uses the platform fetch implementation. */
export function setGitHubFetchForTests(fetcher?: GitHubFetch): void { githubFetch = fetcher ?? fetch; }

async function githubJson<T>(url: string, token: string, init: RequestInit = {}): Promise<T> {
  const response = await githubFetch(url, { ...init, headers: { Accept: "application/vnd.github+json", Authorization: `Bearer ${token}`, "X-GitHub-Api-Version": "2026-03-10", ...(init.headers ?? {}) } });
  const body = await response.text();
  if (!response.ok) throw new Error(`GitHub API ${response.status}: ${body.slice(0, 500)}`);
  try { return JSON.parse(body) as T; } catch { throw new Error("GitHub API returned invalid JSON"); }
}

/** Resolve a PR using only the daemon-owned read-capability GitHub App token. */
export async function resolveRemotePullRequest(remoteUrl: string, token: string): Promise<RemotePullRequest> {
  const target = pullRequestTarget(remoteUrl);
  const apiBase = githubApiBase(target.host);
  const value = await githubJson<GitHubPullApiJson>(`${apiBase}/repos/${target.repository}/pulls/${target.number}`, token);
  const repository = value.base.repo;
  const repositoryUrl = repository?.clone_url ?? repository?.ssh_url ?? repository?.html_url;
  if (!repository?.full_name || !repositoryUrl || !value.head?.sha || !value.base?.sha || !value.html_url || !value.head.ref || !value.base.ref) {
    throw new Error(`GitHub pull request ${remoteUrl} has incomplete repository or commit metadata`);
  }
  return {
    url: value.html_url,
    number: value.number,
    title: value.title,
    body: value.body ?? "",
    state: value.state,
    isDraft: value.draft,
    repository: repository.full_name,
    repositoryUrl,
    headRefName: value.head.ref,
    headSha: value.head.sha,
    baseRefName: value.base.ref,
    baseSha: value.base.sha,
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

function gitHubGitEnvironment(token?: string): Record<string, string> | undefined {
  return token ? { GIT_CONFIG_COUNT: "1", GIT_CONFIG_KEY_0: "http.extraHeader", GIT_CONFIG_VALUE_0: `Authorization: Bearer ${token}`, GIT_TERMINAL_PROMPT: "0" } : undefined;
}

async function ensureMirror(pr: RemotePullRequest, mirrorPath: string, token?: string): Promise<void> {
  mkdirSync(join(CACHE_ROOT, "mirrors"), { recursive: true, mode: 0o700 });
  if (!existsSync(mirrorPath)) {
    const clone = await runCommand(["git", "clone", "--mirror", pr.repositoryUrl, mirrorPath], undefined, gitHubGitEnvironment(token));
    if (clone.exitCode !== 0) throw commandError(`Unable to mirror ${pr.repository}`, clone);
  } else {
    // A renamed GitHub repository can retain its old remote in an existing
    // cache. Keep the cache keyed by repository identity but refresh origin.
    const setUrl = await runCommand(["git", "--git-dir", mirrorPath, "remote", "set-url", "origin", pr.repositoryUrl], undefined, gitHubGitEnvironment(token));
    if (setUrl.exitCode !== 0) throw commandError(`Unable to update mirror origin for ${pr.repository}`, setUrl);
  }
  const fetch = await runCommand(["git", "--git-dir", mirrorPath, "fetch", "--prune", "origin"], undefined, gitHubGitEnvironment(token));
  if (fetch.exitCode !== 0) throw commandError(`Unable to update mirror for ${pr.repository}`, fetch);
  // Fetch the PR ref explicitly: its head may live in a fork and therefore
  // not be reachable through ordinary origin branch refs.
  const pullRef = `+refs/pull/${pr.number}/head:refs/cmux-localreview/pulls/${pr.number}/head`;
  const pullFetch = await runCommand(["git", "--git-dir", mirrorPath, "fetch", "origin", pullRef], undefined, gitHubGitEnvironment(token));
  if (pullFetch.exitCode !== 0) throw commandError(`Unable to fetch PR #${pr.number} head`, pullFetch);
  const exists = await runCommand(["git", "--git-dir", mirrorPath, "cat-file", "-e", `${pr.headSha}^{commit}`]);
  if (exists.exitCode !== 0) throw new Error(`Fetched PR #${pr.number}, but commit ${pr.headSha} is unavailable in the mirror`);
}

/** Materialize the exact resolved PR head in an isolated cache worktree. */
export async function prepareRemoteWorkspace(pr: RemotePullRequest, token?: string): Promise<RemoteWorkspace> {
  const { mirrorPath, worktreePath } = remoteWorkspacePaths(pr);
  mkdirSync(join(CACHE_ROOT, "worktrees", cacheKey(pr), `pr-${pr.number}`), { recursive: true, mode: 0o700 });
  await ensureMirror(pr, mirrorPath, token);
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
 * would otherwise let a review target whatever head happens to be current,
 * which can silently publish comments for a newer revision.
 */
async function assertReviewHeadIsCurrent(pr: RemotePullRequest, readToken: string): Promise<void> {
  const current = await resolveRemotePullRequest(pr.url, readToken);
  if (current.state.toUpperCase() !== "OPEN") {
    throw new Error(`GitHub pull request #${pr.number} is ${current.state.toLowerCase()}; refresh the queue before publishing a review`);
  }
  if (current.headSha !== pr.headSha) {
    throw new Error(`GitHub pull request #${pr.number} changed from ${pr.headSha.slice(0, 12)} to ${current.headSha.slice(0, 12)}; refresh the queue before publishing a review`);
  }
}

/**
 * Publish a GitHub review. The Reviews API supports inline path/line comments
 * tied to the resolved head SHA. Invalid or outdated inline positions are
 * rejected by GitHub rather than being silently redirected to another ref.
 */
/**
 * Publish either a formal decision or a non-blocking COMMENT review.  COMMENT
 * is intentionally separate from the queue lifecycle: it is useful for a
 * safe transport/authentication check and for publishing formal feedback
 * without falsely approving or requesting changes on a pull request.
 */
export async function submitRemoteDecision(item: QueueItem, decision: "approved" | "changes_requested" | "comment", feedback: { body: string; path: string | null; line: number | null }[], tokens: { read: string; write: string }): Promise<RemoteReviewResult> {
  if (!item.remoteUrl) throw new Error("Remote review is missing its PR URL");
  const pr = remotePullRequestFromQueueItem(item) ?? await resolveRemotePullRequest(item.remoteUrl, tokens.read);
  await assertReviewHeadIsCurrent(pr, tokens.read);
  const inline = feedback.filter((entry) => entry.path && entry.line && entry.line > 0).map((entry) => ({
    path: entry.path!, line: entry.line!, side: "RIGHT", body: entry.body,
  }));
  const body = reviewBody(item, feedback);
  const payload = {
    commit_id: pr.headSha,
    event: decision === "approved" ? "APPROVE" : decision === "changes_requested" ? "REQUEST_CHANGES" : "COMMENT",
    body,
    ...(inline.length ? { comments: inline } : {}),
  };
  const target = pullRequestTarget(pr.url);
  const apiBase = githubApiBase(target.host);
  await githubJson(`${apiBase}/repos/${pr.repository}/pulls/${pr.number}/reviews`, tokens.write, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
  return { method: "github-review-api", inlineComments: inline.length };
}
