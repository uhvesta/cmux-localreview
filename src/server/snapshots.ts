import { createHash, randomUUID } from "node:crypto";
import { existsSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";

import { discoverRepos, type RepoInfo } from "./workspace.ts";
import { artifactsDir, ensureDaemonDirectories } from "./daemonPaths.ts";
import { ensureDir, git, safeArtifactName, tempIndexPath } from "./gitExec.ts";
import type { SubmissionProvenance } from "./submissionContext.ts";

export interface SnapshotRepo {
  workspaceRelativePath: string;
  sourcePath: string;
  baseSha: string | null;
  snapshotSha: string;
  bundle: string;
  bundleSha256: string;
}

export interface SnapshotManifest {
  version: 1;
  id: string;
  workspacePath: string;
  createdAt: string;
  /** Capture of the queue origin, never terminal transcript or credentials. */
  provenance?: SubmissionProvenance;
  repos: SnapshotRepo[];
}

function sha256File(path: string): string {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

async function snapshotRepo(repo: RepoInfo, id: string, destination: string, baseRef?: string): Promise<SnapshotRepo> {
  const indexPath = tempIndexPath(
    destination,
    `${id}-${safeArtifactName(repo.workspaceRelativePath)}-${createHash("sha256").update(repo.workspaceRelativePath).digest("hex").slice(0, 10)}`,
  );
  const env = { GIT_INDEX_FILE: indexPath };
  try {
    let baseSha: string | null = null;
    try { baseSha = await git(["rev-parse", baseRef || "HEAD"], repo.absolutePath); } catch { /* unborn repository or unknown optional base */ }
    if (baseSha) await git(["read-tree", baseSha], repo.absolutePath, env);
    else await git(["read-tree", "--empty"], repo.absolutePath, env);
    await git(["add", "-A"], repo.absolutePath, env);
    const tree = await git(["write-tree"], repo.absolutePath, env);
    const message = `cmux-localreview snapshot ${id}`;
    const commitArgs = ["commit-tree", tree, "-m", message];
    if (baseSha) commitArgs.push("-p", baseSha);
    const snapshotSha = await git(commitArgs, repo.absolutePath, env);
    // Two nested repos can share a basename (for example apps/a/api and
    // apps/b/api). Keep a readable name but include the full relative path's
    // digest so their retained bundles can never overwrite each other.
    const repoIdentity = repo.workspaceRelativePath === "." ? `root:${repo.absolutePath}` : repo.workspaceRelativePath;
    const slug = `${safeArtifactName(repo.workspaceRelativePath === "." ? basename(repo.absolutePath) : repo.workspaceRelativePath)}-${createHash("sha256").update(repoIdentity).digest("hex").slice(0, 10)}`;
    const ref = `refs/cmux-localreview/snapshots/${id}/${slug}`;
    await git(["update-ref", ref, snapshotSha], repo.absolutePath);
    const bundle = join(destination, `${slug}.bundle`);
    // `git bundle create <file> <raw-sha>` ignores an otherwise unreachable
    // synthetic commit on some Git versions.  The retained ref makes the
    // bundle self-contained (including the base history needed by clone).
    await git(["bundle", "create", bundle, ref], repo.absolutePath);
    return {
      workspaceRelativePath: repo.workspaceRelativePath,
      sourcePath: repo.absolutePath,
      baseSha,
      snapshotSha,
      bundle: basename(bundle),
      bundleSha256: sha256File(bundle),
    };
  } finally {
    if (existsSync(indexPath)) rmSync(indexPath, { force: true });
  }
}

/**
 * Captures all tracked and untracked files through a temporary Git index.
 * Neither HEAD nor the caller's actual index/working tree is changed.
 */
export async function createWorkspaceSnapshot(
  workspacePath: string,
  snapshotId = randomUUID(),
  baseRef?: string,
  provenance?: SubmissionProvenance,
): Promise<{ manifest: SnapshotManifest; manifestPath: string }> {
  ensureDaemonDirectories();
  const destination = join(artifactsDir(), "snapshots", snapshotId);
  ensureDir(destination);
  const repos = await discoverRepos(workspacePath);
  if (repos.length === 0) throw new Error(`No Git repositories found below ${workspacePath}`);
  const snapshotRepos = await Promise.all(repos.map((repo) => snapshotRepo(repo, snapshotId, destination, baseRef)));
  const manifest: SnapshotManifest = { version: 1, id: snapshotId, workspacePath, createdAt: new Date().toISOString(), ...(provenance ? { provenance } : {}), repos: snapshotRepos };
  const manifestPath = join(destination, "manifest.json");
  writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
  return { manifest, manifestPath };
}

export function readSnapshotManifest(path: string): SnapshotManifest {
  const manifest = JSON.parse(readFileSync(path, "utf8")) as SnapshotManifest;
  if (manifest.version !== 1 || !Array.isArray(manifest.repos)) throw new Error("Unsupported or invalid snapshot manifest");
  return manifest;
}

/** Verifies retained bundles before materializing a reproducible workspace. */
export function verifySnapshotManifest(manifestPath: string, manifest = readSnapshotManifest(manifestPath)): SnapshotManifest {
  const parent = dirname(manifestPath);
  for (const repo of manifest.repos) {
    const bundle = join(parent, repo.bundle);
    if (!existsSync(bundle) || !statSync(bundle).isFile()) throw new Error(`Missing snapshot bundle: ${repo.bundle}`);
    if (sha256File(bundle) !== repo.bundleSha256) throw new Error(`Snapshot bundle hash mismatch: ${repo.bundle}`);
  }
  return manifest;
}

export async function materializeSnapshot(manifestPath: string, destination: string): Promise<SnapshotManifest> {
  const manifest = verifySnapshotManifest(manifestPath);
  const sourceDir = dirname(manifestPath);
  ensureDir(destination);
  for (const repo of manifest.repos) {
    const target = join(destination, repo.workspaceRelativePath === "." ? "" : repo.workspaceRelativePath);
    ensureDir(target);
    const bundle = join(sourceDir, repo.bundle);
    const existing = await git(["rev-parse", "--is-inside-work-tree"], target).catch(() => "");
    if (!existing) await git(["init"], target);
    await git(["fetch", bundle, repo.snapshotSha], target);
    const branch = `localreview/review-${manifest.id.slice(0, 8)}`;
    await git(["checkout", "-B", branch, repo.snapshotSha], target);
  }
  return manifest;
}
