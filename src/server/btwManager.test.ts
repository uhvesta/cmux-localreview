import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { fileURLToPath } from "node:url";

import { runMigrations } from "./db.ts";
import { listThreads } from "./btwStore.ts";
import { BtwManager } from "./btwManager.ts";
import type { WsHub } from "./wsHub.ts";

const FAKE_AGENT = fileURLToPath(new URL("./testFixtures/fakeAcpAgent.ts", import.meta.url));

function makeDb(): Database {
  const db = new Database(":memory:");
  runMigrations(db);
  db.query(`INSERT INTO sessions (label, started_at) VALUES (NULL, 0)`).run();
  return db;
}

function makeFakeHub(): { hub: WsHub; broadcasts: unknown[] } {
  const broadcasts: unknown[] = [];
  const hub = { broadcast: (m: unknown) => broadcasts.push(m) } as unknown as WsHub;
  return { hub, broadcasts };
}

async function waitUntil(predicate: () => boolean, timeoutMs = 3000): Promise<void> {
  const start = Date.now();
  while (!predicate()) {
    if (Date.now() - start > timeoutMs) throw new Error("waitUntil timed out");
    await new Promise((r) => setTimeout(r, 20));
  }
}

describe("BtwManager", () => {
  test("ask() persists a thread/question, streams the answer, and finalizes it", async () => {
    const db = makeDb();
    const { hub, broadcasts } = makeFakeHub();
    const manager = new BtwManager();

    const { threadId, questionId } = await manager.ask({
      db,
      sessionId: 1,
      hub,
      agent: `bun ${FAKE_AGENT} normal`,
      workspaceRoot: process.cwd(),
      filePath: "src/index.ts",
      startLine: 10,
      questionBody: "why is this here?",
    });

    await waitUntil(() => {
      const threads = listThreads(db, 1);
      return threads[0]?.questions[0]?.answer?.pending === false;
    });

    const threads = listThreads(db, 1);
    expect(threads).toHaveLength(1);
    expect(threads[0]!.id).toBe(threadId);
    expect(threads[0]!.questions[0]!.id).toBe(questionId);
    expect(threads[0]!.questions[0]!.answer?.body).toBe("Looking at the file... Done.");
    expect(threads[0]!.acpSessionId).toBe("fake-session-1");
    expect(broadcasts.length).toBeGreaterThan(0);

    manager.disposeAll();
  });

  test("write/exec permission requests from the agent are denied", async () => {
    const db = makeDb();
    const { hub } = makeFakeHub();
    const manager = new BtwManager();

    await manager.ask({
      db,
      sessionId: 1,
      hub,
      agent: `bun ${FAKE_AGENT} write-permission`,
      workspaceRoot: process.cwd(),
      questionBody: "please edit this file",
    });

    await waitUntil(() => {
      const threads = listThreads(db, 1);
      return threads[0]?.questions[0]?.answer?.pending === false;
    });

    const threads = listThreads(db, 1);
    const answerBody = threads[0]!.questions[0]!.answer!.body;
    expect(answerBody).toContain('"optionId":"deny"');
    expect(answerBody).not.toContain('"optionId":"allow"');

    manager.disposeAll();
  });

  test("--dry-run never spawns the agent and fabricates a stub answer", async () => {
    const db = makeDb();
    const { hub } = makeFakeHub();
    const manager = new BtwManager();

    await manager.ask({
      db,
      sessionId: 1,
      hub,
      agent: "claude",
      workspaceRoot: process.cwd(),
      questionBody: "does this matter?",
      dryRun: true,
    });

    await waitUntil(() => {
      const threads = listThreads(db, 1);
      return threads[0]?.questions[0]?.answer?.pending === false;
    });

    const threads = listThreads(db, 1);
    expect(threads[0]!.questions[0]!.answer!.body).toContain("[dry-run]");
    expect(threads[0]!.acpSessionId).toBeNull();

    manager.disposeAll();
  });
});
