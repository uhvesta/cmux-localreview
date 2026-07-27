import { describe, expect, test, afterEach, afterAll } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, appendFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import type { Server } from "node:http";

import { buildWorkspaceApp, repoIdFor } from "./app.ts";
import { discoverRepos } from "./workspace.ts";

const dirsToClean: string[] = [];
const serversToClose: Server[] = [];

function makeTmpDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "cmux-localreview-app-test-"));
  dirsToClean.push(dir);
  return dir;
}

function gitInit(dir: string): void {
  mkdirSync(dir, { recursive: true });
  execFileSync("git", ["init", "-q"], { cwd: dir });
  execFileSync("git", ["config", "user.email", "test@test.com"], { cwd: dir });
  execFileSync("git", ["config", "user.name", "test"], { cwd: dir });
  writeFileSync(join(dir, "a.txt"), "hello\n");
  execFileSync("git", ["add", "."], { cwd: dir });
  execFileSync("git", ["commit", "-q", "-m", "init"], { cwd: dir });
}

async function listen(app: Awaited<ReturnType<typeof buildWorkspaceApp>>["app"]): Promise<{
  server: Server;
  baseUrl: string;
}> {
  return new Promise((resolve) => {
    const server = app.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("expected AddressInfo");
      serversToClose.push(server);
      resolve({ server, baseUrl: `http://127.0.0.1:${address.port}` });
    });
  });
}

afterEach(() => {
  while (dirsToClean.length) {
    rmSync(dirsToClean.pop()!, { recursive: true, force: true });
  }
});

afterAll(() => {
  while (serversToClose.length) {
    serversToClose.pop()!.close();
  }
});

describe("buildWorkspaceApp", () => {
  test("mounts one namespaced diff app per discovered repo, isolated from each other", async () => {
    const workspace = makeTmpDir();
    const repoAPath = join(workspace, "repo-a");
    const repoBPath = join(workspace, "repo-b");
    gitInit(repoAPath);
    gitInit(repoBPath);
    appendFileSync(join(repoAPath, "a.txt"), "change in repo A\n");
    appendFileSync(join(repoBPath, "a.txt"), "change in repo B\n");

    const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace });
    expect(repos).toHaveLength(2);

    const { baseUrl } = await listen(app);

    const listRes = await fetch(`${baseUrl}/api/repos`);
    const listJson = (await listRes.json()) as { repos: { id: string; workspaceRelativePath: string }[] };
    expect(listJson.repos.map((r) => r.workspaceRelativePath).sort()).toEqual(["repo-a", "repo-b"]);

    const repoA = repos.find((r) => r.repo.workspaceRelativePath === "repo-a")!;
    const repoB = repos.find((r) => r.repo.workspaceRelativePath === "repo-b")!;

    const diffARes = await fetch(`${baseUrl}/api/repos/${repoA.repoId}/api/diff`);
    const diffA = (await diffARes.json()) as { files: { path: string }[] };
    expect(diffARes.status).toBe(200);
    expect(diffA.files).toHaveLength(1);

    const diffBRes = await fetch(`${baseUrl}/api/repos/${repoB.repoId}/api/diff`);
    const diffB = (await diffBRes.json()) as { files: { path: string }[] };
    expect(diffBRes.status).toBe(200);
    expect(diffB.files).toHaveLength(1);

    // Comments posted to repo A must not leak into repo B's session.
    await fetch(`${baseUrl}/api/repos/${repoA.repoId}/api/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ comments: [{ file: "a.txt", line: 1, body: "hi from A" }] }),
    });

    const commentsB = await fetch(`${baseUrl}/api/repos/${repoB.repoId}/api/comments-json`);
    const commentsBJson = (await commentsB.json()) as { threads: unknown[] };
    expect(commentsBJson.threads).toHaveLength(0);

    const commentsA = await fetch(`${baseUrl}/api/repos/${repoA.repoId}/api/comments-json`);
    const commentsAJson = (await commentsA.json()) as { threads: unknown[] };
    expect(commentsAJson.threads).toHaveLength(1);
  });

  test("repoIdFor is stable for the same repo across discovery runs", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"));

    const first = await discoverRepos(workspace);
    const second = await discoverRepos(workspace);

    expect(repoIdFor(first[0]!)).toBe(repoIdFor(second[0]!));
  });

  test("blob route rejects path traversal within a mounted repo", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"));

    const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace });
    const { baseUrl } = await listen(app);
    const repoId = repos[0]!.repoId;

    const res = await fetch(`${baseUrl}/api/repos/${repoId}/api/blob/..%2F..%2Fetc%2Fpasswd`);
    expect(res.status).toBe(400);
  });
});
