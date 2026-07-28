import { describe, expect, test, afterEach, afterAll } from "bun:test";
import { Database } from "bun:sqlite";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, appendFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import type { Server } from "node:http";

import { buildWorkspaceApp, repoIdFor } from "./app.ts";
import { discoverRepos } from "./workspace.ts";
import { runMigrations } from "./db.ts";

function makeDb(): Database {
  const db = new Database(":memory:");
  runMigrations(db);
  return db;
}

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

    const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
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

    const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
    const { baseUrl } = await listen(app);
    const repoId = repos[0]!.repoId;

    const res = await fetch(`${baseUrl}/api/repos/${repoId}/api/blob/..%2F..%2Fetc%2Fpasswd`);
    expect(res.status).toBe(400);
  });

  test("export prompt anchors workspace-relative paths at the review workspace", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo-a"));
    gitInit(join(workspace, "nested", "repo-b"));

    const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
    const { baseUrl } = await listen(app);
    const repoA = repos.find((r) => r.repo.workspaceRelativePath === "repo-a")!;
    const repoB = repos.find((r) => r.repo.workspaceRelativePath === "nested/repo-b")!;

    await fetch(`${baseUrl}/api/repos/${repoA.repoId}/api/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ comments: [{ file: "a.txt", line: 1, body: "fix this in A" }] }),
    });
    await fetch(`${baseUrl}/api/repos/${repoB.repoId}/api/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ comments: [{ file: "a.txt", line: 1, body: "fix this in B" }] }),
    });

    const promptRes = await fetch(`${baseUrl}/api/export/prompt`);
    const prompt = await promptRes.text();

    expect(prompt).toContain(`Workspace root: ${workspace}`);
    expect(prompt).toContain("File paths below are relative to this workspace root");
    expect(prompt).toContain("repo-a/a.txt:L1");
    expect(prompt).toContain("nested/repo-b/a.txt:L1");
    expect(prompt).toContain("fix this in A");
    expect(prompt).toContain("fix this in B");
  });

  test("POST /api/export with destination=cmux logs a dry-run send and records the export", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"));
    const db = makeDb();

    const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db, dryRun: true });
    const { baseUrl } = await listen(app);

    await fetch(`${baseUrl}/api/repos/${repos[0]!.repoId}/api/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ comments: [{ file: "a.txt", line: 1, body: "hello" }] }),
    });

    const exportRes = await fetch(`${baseUrl}/api/export`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ destination: "cmux" }),
    });
    expect(exportRes.status).toBe(200);
    const exportJson = (await exportRes.json()) as { success: boolean; content: string };
    expect(exportJson.success).toBe(true);
    expect(exportJson.content).toContain("hello");

    const rows = db.query(`SELECT destination, content FROM exports`).all() as {
      destination: string;
      content: string;
    }[];
    expect(rows).toHaveLength(1);
    expect(rows[0]!.destination).toBe("cmux");
    expect(rows[0]!.content).toContain("hello");
  });

  test("comments persist across a fresh buildWorkspaceApp call against the same db (restart)", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"));
    const db = makeDb();

    const first = await buildWorkspaceApp({ workspaceRoot: workspace, db });
    const { baseUrl: url1 } = await listen(first.app);
    await fetch(`${url1}/api/repos/${first.repos[0]!.repoId}/api/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ comments: [{ file: "a.txt", line: 1, body: "survives restart" }] }),
    });

    const second = await buildWorkspaceApp({ workspaceRoot: workspace, db });
    const { baseUrl: url2 } = await listen(second.app);
    const res = await fetch(`${url2}/api/repos/${second.repos[0]!.repoId}/api/comments-json`);
    const json = (await res.json()) as { threads: { messages: { body: string }[] }[] };
    expect(json.threads).toHaveLength(1);
    expect(json.threads[0]!.messages[0]!.body).toBe("survives restart");
  });

  describe("Full File view (/api/fullfile)", () => {
    test("side=current returns the full working-tree file with deletion gates", async () => {
      const workspace = makeTmpDir();
      const repoPath = join(workspace, "repo");
      mkdirSync(repoPath, { recursive: true });
      execFileSync("git", ["init", "-q"], { cwd: repoPath });
      execFileSync("git", ["config", "user.email", "t@t.com"], { cwd: repoPath });
      execFileSync("git", ["config", "user.name", "t"], { cwd: repoPath });
      writeFileSync(join(repoPath, "f.txt"), "one\ntwo\nthree\nfour\nfive\n");
      execFileSync("git", ["add", "."], { cwd: repoPath });
      execFileSync("git", ["commit", "-q", "-m", "init"], { cwd: repoPath });
      writeFileSync(join(repoPath, "f.txt"), "one\nthree\nfive\nsix\n");

      const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
      const { baseUrl } = await listen(app);
      const repoId = repos[0]!.repoId;

      const res = await fetch(`${baseUrl}/api/repos/${repoId}/api/fullfile/f.txt?side=current`);
      expect(res.status).toBe(200);
      const json = (await res.json()) as {
        lines: string[];
        gates: { afterLine: number; hiddenStart: number; hiddenEnd: number; lines: string[] }[];
      };
      expect(json.lines).toEqual(["one", "three", "five", "six"]);
      // "two" (old line 2) was deleted, anchored after new-file line 1 ("one").
      expect(json.gates).toContainEqual({ afterLine: 1, hiddenStart: 2, hiddenEnd: 2, lines: ["two"] });
      // "four" (old line 4) was deleted, anchored after new-file line 2 ("three").
      expect(json.gates).toContainEqual({ afterLine: 2, hiddenStart: 4, hiddenEnd: 4, lines: ["four"] });
    });

    test("side=base returns the full old file with addition gates", async () => {
      const workspace = makeTmpDir();
      const repoPath = join(workspace, "repo");
      mkdirSync(repoPath, { recursive: true });
      execFileSync("git", ["init", "-q"], { cwd: repoPath });
      execFileSync("git", ["config", "user.email", "t@t.com"], { cwd: repoPath });
      execFileSync("git", ["config", "user.name", "t"], { cwd: repoPath });
      writeFileSync(join(repoPath, "f.txt"), "one\ntwo\nthree\n");
      execFileSync("git", ["add", "."], { cwd: repoPath });
      execFileSync("git", ["commit", "-q", "-m", "init"], { cwd: repoPath });
      writeFileSync(join(repoPath, "f.txt"), "one\ntwo\nnew-line\nthree\n");

      const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
      const { baseUrl } = await listen(app);
      const repoId = repos[0]!.repoId;

      const res = await fetch(`${baseUrl}/api/repos/${repoId}/api/fullfile/f.txt?side=base`);
      expect(res.status).toBe(200);
      const json = (await res.json()) as {
        lines: string[];
        gates: { afterLine: number; hiddenStart: number; hiddenEnd: number; lines: string[] }[];
      };
      expect(json.lines).toEqual(["one", "two", "three"]);
      expect(json.gates).toContainEqual({
        afterLine: 2,
        hiddenStart: 3,
        hiddenEnd: 3,
        lines: ["new-line"],
      });
    });

    test("deleted file: side=current reports deleted:true instead of gates", async () => {
      const workspace = makeTmpDir();
      const repoPath = join(workspace, "repo");
      mkdirSync(repoPath, { recursive: true });
      execFileSync("git", ["init", "-q"], { cwd: repoPath });
      execFileSync("git", ["config", "user.email", "t@t.com"], { cwd: repoPath });
      execFileSync("git", ["config", "user.name", "t"], { cwd: repoPath });
      writeFileSync(join(repoPath, "gone.txt"), "bye\n");
      execFileSync("git", ["add", "."], { cwd: repoPath });
      execFileSync("git", ["commit", "-q", "-m", "init"], { cwd: repoPath });
      execFileSync("git", ["rm", "-q", "gone.txt"], { cwd: repoPath });

      const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
      const { baseUrl } = await listen(app);
      const repoId = repos[0]!.repoId;

      const res = await fetch(`${baseUrl}/api/repos/${repoId}/api/fullfile/gone.txt?side=current`);
      const json = (await res.json()) as { deleted?: boolean };
      expect(json.deleted).toBe(true);
    });

    test("rejects path traversal", async () => {
      const workspace = makeTmpDir();
      gitInit(join(workspace, "repo"));
      const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
      const { baseUrl } = await listen(app);
      const res = await fetch(
        `${baseUrl}/api/repos/${repos[0]!.repoId}/api/fullfile/..%2F..%2Fetc%2Fpasswd`,
      );
      expect(res.status).toBe(400);
    });
  });

  describe("sessions (New Review)", () => {
    test("POST /api/sessions/new freezes prior comments and starts a clean session", async () => {
      const workspace = makeTmpDir();
      gitInit(join(workspace, "repo"));
      const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
      const { baseUrl } = await listen(app);
      const repoId = repos[0]!.repoId;

      await fetch(`${baseUrl}/api/repos/${repoId}/api/comments`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ comments: [{ file: "a.txt", line: 1, body: "old session comment" }] }),
      });

      const sessionsBefore = (await (await fetch(`${baseUrl}/api/sessions`)).json()) as {
        activeSessionId: number;
      };

      const newSessionRes = await fetch(`${baseUrl}/api/sessions/new`, { method: "POST" });
      expect(newSessionRes.status).toBe(200);
      const { sessionId: newSessionId } = (await newSessionRes.json()) as { sessionId: number };
      expect(newSessionId).not.toBe(sessionsBefore.activeSessionId);

      // New session starts with no comments visible...
      const commentsAfter = (await (
        await fetch(`${baseUrl}/api/repos/${repoId}/api/comments-json`)
      ).json()) as { threads: unknown[] };
      expect(commentsAfter.threads).toHaveLength(0);

      // ...but the frozen session's comment is still exportable by id.
      const oldPrompt = await (
        await fetch(`${baseUrl}/api/export/prompt?sessionId=${sessionsBefore.activeSessionId}`)
      ).text();
      expect(oldPrompt).toContain("old session comment");

      const sessionsList = (await (await fetch(`${baseUrl}/api/sessions`)).json()) as {
        sessions: { id: number; frozenAt: number | null; commentCount: number }[];
      };
      const frozen = sessionsList.sessions.find((s) => s.id === sessionsBefore.activeSessionId)!;
      expect(frozen.frozenAt).not.toBeNull();
      expect(frozen.commentCount).toBe(1);
    });

    test("a comment posted after New Review attaches to the new session, not the frozen one", async () => {
      const workspace = makeTmpDir();
      gitInit(join(workspace, "repo"));
      const { app, repos } = await buildWorkspaceApp({ workspaceRoot: workspace, db: makeDb() });
      const { baseUrl } = await listen(app);
      const repoId = repos[0]!.repoId;

      await fetch(`${baseUrl}/api/sessions/new`, { method: "POST" });

      await fetch(`${baseUrl}/api/repos/${repoId}/api/comments`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ comments: [{ file: "a.txt", line: 1, body: "new session comment" }] }),
      });

      const commentsRes = (await (
        await fetch(`${baseUrl}/api/repos/${repoId}/api/comments-json`)
      ).json()) as { threads: { messages: { body: string }[] }[] };
      expect(commentsRes.threads).toHaveLength(1);
      expect(commentsRes.threads[0]!.messages[0]!.body).toBe("new session comment");
    });
  });
});
