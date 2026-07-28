import { afterEach, describe, expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { createWorkspaceSnapshot, materializeSnapshot, verifySnapshotManifest } from "./snapshots.ts";

const roots: string[] = [];

function tempRoot(): string {
  const root = mkdtempSync(join(tmpdir(), "cmux-localreview-snapshot-e2e-"));
  roots.push(root);
  return root;
}

function git(cwd: string, args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

function createRepo(path: string, initial: string): void {
  execFileSync("git", ["init", "-q", path]);
  git(path, ["config", "user.email", "fixture@example.invalid"]);
  git(path, ["config", "user.name", "fixture"]);
  writeFileSync(join(path, "tracked.txt"), initial);
  git(path, ["add", "tracked.txt"]);
  git(path, ["commit", "-qm", "initial"]);
}

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

describe("multi-repository snapshot reproduction fixture", () => {
  test("captures dirty and untracked files across repositories without changing their source state", async () => {
    const root = tempRoot();
    const priorDataDir = process.env.CMUX_LOCALREVIEW_DATA_DIR;
    process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "daemon-data");
    const workspace = join(root, "workspace");
    const repoA = join(workspace, "service-a");
    const repoB = join(workspace, "nested", "service-b");
    createRepo(repoA, "base A\n");
    createRepo(repoB, "base B\n");
    writeFileSync(join(repoA, "tracked.txt"), "dirty A\n");
    writeFileSync(join(repoA, "untracked.txt"), "new A\n");
    writeFileSync(join(repoB, "tracked.txt"), "dirty B\n");
    const aHead = git(repoA, ["rev-parse", "HEAD"]);
    const bHead = git(repoB, ["rev-parse", "HEAD"]);
    const aStatus = git(repoA, ["status", "--porcelain"]);
    const bStatus = git(repoB, ["status", "--porcelain"]);

    try {
    const snapshotId = randomUUID();
    const captured = await createWorkspaceSnapshot(workspace, snapshotId, undefined, {
      version: 1,
      capturedAt: new Date().toISOString(),
      workspacePath: workspace,
      caller: { cwd: workspace, cmuxSurfaceId: "surface-fixture", cmuxWorkspaceId: "workspace-fixture" },
      cmux: { available: true, surfaces: [{ id: "surface-fixture", workspaceId: "workspace-fixture" }] },
    });
      expect(captured.manifest.repos.map((repo) => repo.workspaceRelativePath).sort()).toEqual(["nested/service-b", "service-a"]);
    expect(verifySnapshotManifest(captured.manifestPath).id).toBe(snapshotId);
      expect(git(repoA, ["rev-parse", "HEAD"])).toBe(aHead);
      expect(git(repoB, ["rev-parse", "HEAD"])).toBe(bHead);
      expect(git(repoA, ["status", "--porcelain"])).toBe(aStatus);
      expect(git(repoB, ["status", "--porcelain"])).toBe(bStatus);

      const reproduced = join(root, "reproduced");
      await materializeSnapshot(captured.manifestPath, reproduced);
      expect(readFileSync(join(reproduced, "service-a", "tracked.txt"), "utf8")).toBe("dirty A\n");
      expect(readFileSync(join(reproduced, "service-a", "untracked.txt"), "utf8")).toBe("new A\n");
      expect(readFileSync(join(reproduced, "nested", "service-b", "tracked.txt"), "utf8")).toBe("dirty B\n");
      expect(existsSync(join(reproduced, "service-a", ".git"))).toBe(true);
    } finally {
      if (priorDataDir === undefined) delete process.env.CMUX_LOCALREVIEW_DATA_DIR;
      else process.env.CMUX_LOCALREVIEW_DATA_DIR = priorDataDir;
    }
  });

  test("keeps bundles distinct when nested repositories share a basename", async () => {
    const root = tempRoot();
    const priorDataDir = process.env.CMUX_LOCALREVIEW_DATA_DIR;
    process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "daemon-data");
    const workspace = join(root, "workspace");
    const first = join(workspace, "apps", "one", "api");
    const second = join(workspace, "apps", "two", "api");
    createRepo(first, "one\n");
    createRepo(second, "two\n");
    try {
      const captured = await createWorkspaceSnapshot(workspace);
      expect(new Set(captured.manifest.repos.map((repo) => repo.bundle)).size).toBe(2);
      const reproduced = join(root, "reproduced");
      await materializeSnapshot(captured.manifestPath, reproduced);
      expect(readFileSync(join(reproduced, "apps", "one", "api", "tracked.txt"), "utf8")).toBe("one\n");
      expect(readFileSync(join(reproduced, "apps", "two", "api", "tracked.txt"), "utf8")).toBe("two\n");
    } finally {
      if (priorDataDir === undefined) delete process.env.CMUX_LOCALREVIEW_DATA_DIR;
      else process.env.CMUX_LOCALREVIEW_DATA_DIR = priorDataDir;
    }
  });
});
