import { readdirSync, readFileSync, existsSync, realpathSync, statSync } from "node:fs";
import { join, relative, resolve, dirname, isAbsolute } from "node:path";
import { simpleGit } from "simple-git";

export interface RepoInfo {
  /** Workspace-relative path to the repo root (POSIX separators, no leading './'). */
  workspaceRelativePath: string;
  /** Absolute path to the repo working tree root. */
  absolutePath: string;
  /** Canonical (symlink-resolved) absolute path to the repo's git directory. */
  gitDir: string;
  /** Normalized primary remote URL, or null if the repo has no remote. */
  remoteUrl: string | null;
}

const SKIP_DIR_NAMES = new Set(["node_modules", ".git"]);
const DEFAULT_MAX_DEPTH = 6;

function isHidden(name: string): boolean {
  return name.startsWith(".") && name !== ".";
}

function readGitDir(repoRoot: string): string {
  const dotGit = join(repoRoot, ".git");
  const isDir = statSync(dotGit).isDirectory();

  if (isDir) {
    return realpathSync(dotGit);
  }

  // Worktrees/submodules: `.git` is a file containing `gitdir: <path>`.
  const content = readFileSync(dotGit, "utf-8").trim();
  const match = /^gitdir:\s*(.+)$/.exec(content);
  if (!match) {
    throw new Error(`Unrecognized .git file format at ${dotGit}`);
  }
  const target = match[1]!.trim();
  const resolved = isAbsolute(target) ? target : resolve(repoRoot, target);
  return realpathSync(resolved);
}

function normalizeRemoteUrl(raw: string): string {
  let url = raw.trim();
  // git@host:owner/repo.git -> ssh form retained but normalized to a comparable shape.
  const scpMatch = /^([^@]+)@([^:]+):(.+)$/.exec(url);
  if (scpMatch) {
    url = `ssh://${scpMatch[1]}@${scpMatch[2]}/${scpMatch[3]}`;
  }
  url = url.replace(/\.git$/, "");
  url = url.replace(/\/$/, "");
  return url.toLowerCase();
}

async function readRemoteUrl(repoRoot: string): Promise<string | null> {
  try {
    const git = simpleGit(repoRoot);
    const raw = await git.raw(["config", "--get", "remote.origin.url"]);
    const trimmed = raw.trim();
    return trimmed ? normalizeRemoteUrl(trimmed) : null;
  } catch {
    return null;
  }
}

export interface DiscoverReposOptions {
  maxDepth?: number;
}

/**
 * Recursively scan a workspace directory for git working trees.
 * Stops descending once a repo root is found (repos are discovered at
 * arbitrary sibling subpaths, never nested inside another discovered repo).
 */
export async function discoverRepos(
  workspaceRoot: string,
  options: DiscoverReposOptions = {},
): Promise<RepoInfo[]> {
  const maxDepth = options.maxDepth ?? DEFAULT_MAX_DEPTH;
  const root = resolve(workspaceRoot);
  const repos: RepoInfo[] = [];
  const visitedRealPaths = new Set<string>();

  async function scan(dir: string, depth: number): Promise<void> {
    let real: string;
    try {
      real = realpathSync(dir);
    } catch {
      return;
    }
    if (visitedRealPaths.has(real)) return; // symlink loop guard
    visitedRealPaths.add(real);

    if (existsSync(join(dir, ".git"))) {
      const gitDir = readGitDir(dir);
      const remoteUrl = await readRemoteUrl(dir);
      const relPath = relative(root, dir);
      repos.push({
        workspaceRelativePath: relPath === "" ? "." : relPath.split(/[/\\]/).join("/"),
        absolutePath: resolve(dir),
        gitDir,
        remoteUrl,
      });
      return; // stop descending at a repo root
    }

    if (depth >= maxDepth) return;

    let entries;
    try {
      entries = readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }

    for (const entry of entries) {
      if (!entry.isDirectory()) continue;
      if (SKIP_DIR_NAMES.has(entry.name)) continue;
      if (isHidden(entry.name)) continue;
      await scan(join(dir, entry.name), depth + 1);
    }
  }

  await scan(root, 0);

  repos.sort((a, b) => a.workspaceRelativePath.localeCompare(b.workspaceRelativePath));
  return repos;
}

/** Stable identity key for a repo, independent of directory name. */
export function repoIdentityKey(repo: Pick<RepoInfo, "gitDir">): string {
  return repo.gitDir;
}
