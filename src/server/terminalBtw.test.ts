import { describe, expect, test, afterEach } from "bun:test";
import { Database } from "bun:sqlite";
import { writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";

import { runMigrations } from "./db.ts";
import { listThreads } from "./btwStore.ts";
import { TerminalBtw, resolveBtwDir } from "./terminalBtw.ts";
import type { WsHub } from "./wsHub.ts";

function makeDb(): Database {
  const db = new Database(":memory:");
  runMigrations(db);
  db.query(`INSERT INTO sessions (label, started_at) VALUES (NULL, 0)`).run();
  return db;
}

function makeFakeHub(): { hub: WsHub; broadcasts: { type: string; threadId: number }[] } {
  const broadcasts: { type: string; threadId: number }[] = [];
  const hub = { broadcast: (m: { type: string; threadId: number }) => broadcasts.push(m) } as unknown as WsHub;
  return { hub, broadcasts };
}

async function waitUntil(predicate: () => boolean, timeoutMs = 5000): Promise<void> {
  const start = Date.now();
  while (!predicate()) {
    if (Date.now() - start > timeoutMs) throw new Error("waitUntil timed out");
    await new Promise((r) => setTimeout(r, 30));
  }
}

const instances: TerminalBtw[] = [];

afterEach(() => {
  while (instances.length) instances.pop()!.stop();
  rmSync(resolveBtwDir(), { recursive: true, force: true });
});

describe("TerminalBtw", () => {
  test("ask() sends via the dry-run cmux connector and creates a pending thread", async () => {
    const db = makeDb();
    const { hub } = makeFakeHub();
    const terminalBtw = new TerminalBtw(db, hub, true);
    instances.push(terminalBtw);

    const { threadId, questionId } = await terminalBtw.ask({
      sessionId: 1,
      filePath: "a.txt",
      startLine: 3,
      questionBody: "what does this do?",
    });

    const threads = listThreads(db, 1);
    expect(threads[0]!.id).toBe(threadId);
    expect(threads[0]!.transport).toBe("terminal");
    expect(threads[0]!.questions[0]!.id).toBe(questionId);
    expect(threads[0]!.questions[0]!.answer?.pending).toBe(true);
  });

  test("a markdown file written to the response dir finalizes the answer within ~1s", async () => {
    const db = makeDb();
    const { hub, broadcasts } = makeFakeHub();
    const terminalBtw = new TerminalBtw(db, hub, true);
    instances.push(terminalBtw);

    const { questionId } = await terminalBtw.ask({
      sessionId: 1,
      questionBody: "why?",
    });

    writeFileSync(join(resolveBtwDir(), `${questionId}.md`), "Because of X.");

    await waitUntil(() => {
      const threads = listThreads(db, 1);
      return threads[0]?.questions[0]?.answer?.pending === false;
    });

    const threads = listThreads(db, 1);
    expect(threads[0]!.questions[0]!.answer?.body).toBe("Because of X.");
    expect(broadcasts.some((b) => b.type === "btw-update")).toBe(true);
  });
});
