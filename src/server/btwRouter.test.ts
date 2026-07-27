import { describe, expect, test, afterEach, afterAll } from "bun:test";
import { Database } from "bun:sqlite";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import type { Server } from "node:http";

import { buildWorkspaceApp } from "./app.ts";
import { runMigrations } from "./db.ts";
import { WsHub } from "./wsHub.ts";
import { resolveBtwDir } from "./terminalBtw.ts";

const FAKE_AGENT = fileURLToPath(new URL("./testFixtures/fakeAcpAgent.ts", import.meta.url));

function makeDb(): Database {
  const db = new Database(":memory:");
  runMigrations(db);
  return db;
}

const dirsToClean: string[] = [];
const serversToClose: Server[] = [];
const hubsToClose: WsHub[] = [];

function makeTmpDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "cmux-localreview-btw-router-test-"));
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

async function listenWithHub(app: Awaited<ReturnType<typeof buildWorkspaceApp>>): Promise<{
  baseUrl: string;
  hub: WsHub;
}> {
  return new Promise((resolve) => {
    const server = app.app.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("expected AddressInfo");
      serversToClose.push(server);
      const hub = new WsHub(server);
      hubsToClose.push(hub);
      resolve({ baseUrl: `http://127.0.0.1:${address.port}`, hub });
    });
  });
}

async function waitUntil(predicate: () => Promise<boolean>, timeoutMs = 5000): Promise<void> {
  const start = Date.now();
  for (;;) {
    if (await predicate()) return;
    if (Date.now() - start > timeoutMs) throw new Error("waitUntil timed out");
    await new Promise((r) => setTimeout(r, 30));
  }
}

afterEach(() => {
  while (dirsToClean.length) rmSync(dirsToClean.pop()!, { recursive: true, force: true });
  rmSync(resolveBtwDir(), { recursive: true, force: true });
});

afterAll(() => {
  while (serversToClose.length) serversToClose.pop()!.close();
  while (hubsToClose.length) hubsToClose.pop()!.close();
});

describe("/api/btw", () => {
  test("POST /api/btw/ask (acp) streams and finalizes an answer, visible via GET /api/btw/threads", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"));
    const db = makeDb();

    const app = await buildWorkspaceApp({ workspaceRoot: workspace, db });
    const { baseUrl, hub } = await listenWithHub(app);
    const { btwManager, terminalBtw } = app.startBtw(hub, `bun ${FAKE_AGENT} normal`);

    const askRes = await fetch(`${baseUrl}/api/btw/ask`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        transport: "acp",
        repoId: app.repos[0]!.repoId,
        filePath: "a.txt",
        startLine: 1,
        question: "what does this file do?",
      }),
    });
    expect(askRes.status).toBe(200);

    await waitUntil(async () => {
      const res = await fetch(`${baseUrl}/api/btw/threads`);
      const json = (await res.json()) as { threads: { questions: { answer: { pending: boolean } | null }[] }[] };
      return json.threads[0]?.questions[0]?.answer?.pending === false;
    });

    const res = await fetch(`${baseUrl}/api/btw/threads`);
    const json = (await res.json()) as {
      threads: { transport: string; filePath: string; questions: { body: string; answer: { body: string } }[] }[];
    };
    expect(json.threads).toHaveLength(1);
    expect(json.threads[0]!.transport).toBe("acp");
    expect(json.threads[0]!.filePath).toBe("a.txt");
    expect(json.threads[0]!.questions[0]!.answer.body).toBe("Looking at the file... Done.");

    btwManager.disposeAll();
    terminalBtw.stop();
  });

  test("POST /api/btw/ask (terminal) with an unknown repoId 404s", async () => {
    const workspace = makeTmpDir();
    gitInit(join(workspace, "repo"));
    const db = makeDb();

    const app = await buildWorkspaceApp({ workspaceRoot: workspace, db });
    const { baseUrl, hub } = await listenWithHub(app);
    const { btwManager, terminalBtw } = app.startBtw(hub, "claude");

    const res = await fetch(`${baseUrl}/api/btw/ask`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ transport: "terminal", repoId: "does-not-exist", question: "hi" }),
    });
    expect(res.status).toBe(404);

    btwManager.disposeAll();
    terminalBtw.stop();
  });
});
