import { describe, expect, test, afterEach } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";

import { discoverRepos, repoIdentityKey } from "./workspace.ts";

const dirsToClean: string[] = [];

function makeTmpDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "cmux-localreview-workspace-test-"));
  dirsToClean.push(dir);
  return dir;
}

function gitInit(dir: string, opts: { remote?: string } = {}): void {
  mkdirSync(dir, { recursive: true });
  execFileSync("git", ["init", "-q"], { cwd: dir });
  execFileSync("git", ["config", "user.email", "test@test.com"], { cwd: dir });
  execFileSync("git", ["config", "user.name", "test"], { cwd: dir });
  writeFileSync(join(dir, "README.md"), "hello\n");
  execFileSync("git", ["add", "."], { cwd: dir });
  execFileSync("git", ["commit", "-q", "-m", "init"], { cwd: dir });
  if (opts.remote) {
    execFileSync("git", ["remote", "add", "origin", opts.remote], { cwd: dir });
  }
}

afterEach(() => {
  while (dirsToClean.length) {
    const dir = dirsToClean.pop()!;
    rmSync(dir, { recursive: true, force: true });
  }
});

describe("discoverRepos", () => {
  test("finds multiple repos at sibling subpaths", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo-a"));
    gitInit(join(workspace, "nested", "repo-b"));
    mkdirSync(join(workspace, "not-a-repo", "just-files"), { recursive: true });
    writeFileSync(join(workspace, "not-a-repo", "just-files", "x.txt"), "noise");

    const repos = await discoverRepos(workspace);
    const paths = repos.map((r) => r.workspaceRelativePath).sort();

    expect(paths).toEqual(["nested/repo-b", "repo-a"]);
  });

  test("skips node_modules and hidden directories", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "node_modules", "some-pkg"));
    gitInit(join(workspace, ".hidden", "repo"));
    gitInit(join(workspace, "real-repo"));

    const repos = await discoverRepos(workspace);
    const paths = repos.map((r) => r.workspaceRelativePath);

    expect(paths).toEqual(["real-repo"]);
  });

  test("does not descend past a discovered repo root", async () => {
    const workspace = makeTmpDir();
    const outer = join(workspace, "outer");
    gitInit(outer);
    // A directory that looks like it could contain another repo, but since
    // it's inside an already-discovered repo root, scanning stops there.
    mkdirSync(join(outer, "vendor", "some-lib", ".git"), { recursive: true });

    const repos = await discoverRepos(workspace);
    expect(repos.map((r) => r.workspaceRelativePath)).toEqual(["outer"]);
  });

  test("normalizes remote URL and exposes a stable identity key", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"), { remote: "git@github.com:owner/repo.git" });

    const repos = await discoverRepos(workspace);
    expect(repos).toHaveLength(1);
    expect(repos[0]!.remoteUrl).toBe("ssh://git@github.com/owner/repo");
    expect(repoIdentityKey(repos[0]!)).toBe(repos[0]!.gitDir);
  });

  test("repo with no remote has remoteUrl null", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"));

    const repos = await discoverRepos(workspace);
    expect(repos[0]!.remoteUrl).toBeNull();
  });

  test("workspace root itself being a repo yields '.'", async () => {
    const workspace = makeTmpDir();
    gitInit(workspace);

    const repos = await discoverRepos(workspace);
    expect(repos.map((r) => r.workspaceRelativePath)).toEqual(["."]);
  });
});
