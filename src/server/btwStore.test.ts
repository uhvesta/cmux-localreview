import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";

import { runMigrations } from "./db.ts";
import {
  createThread,
  addQuestion,
  createPendingAnswer,
  appendAnswerChunk,
  finalizeAnswer,
  setThreadAcpSessionId,
  getThreadAcpSessionId,
  listThreads,
} from "./btwStore.ts";

function makeDb(): Database {
  const db = new Database(":memory:");
  runMigrations(db);
  db.query(`INSERT INTO sessions (label, started_at) VALUES (NULL, 0)`).run();
  return db;
}

describe("btwStore", () => {
  test("round-trips a thread with a streamed answer", () => {
    const db = makeDb();
    const threadId = createThread(db, { sessionId: 1, transport: "acp", acpProvider: "claude" });
    const questionId = addQuestion(db, threadId, "why is this slow?");
    const answerId = createPendingAnswer(db, questionId);

    appendAnswerChunk(db, answerId, "Looking at the file");
    appendAnswerChunk(db, answerId, "... it's an N+1 query.");
    finalizeAnswer(db, answerId);

    const threads = listThreads(db, 1);
    expect(threads).toHaveLength(1);
    expect(threads[0]!.transport).toBe("acp");
    expect(threads[0]!.questions).toHaveLength(1);
    expect(threads[0]!.questions[0]!.body).toBe("why is this slow?");
    expect(threads[0]!.questions[0]!.answer?.body).toBe("Looking at the file... it's an N+1 query.");
    expect(threads[0]!.questions[0]!.answer?.pending).toBe(false);
  });

  test("answer stays pending until finalized", () => {
    const db = makeDb();
    const threadId = createThread(db, { sessionId: 1, transport: "acp" });
    const questionId = addQuestion(db, threadId, "q");
    createPendingAnswer(db, questionId);

    const threads = listThreads(db, 1);
    expect(threads[0]!.questions[0]!.answer?.pending).toBe(true);
  });

  test("stores and retrieves the ACP session id for resume", () => {
    const db = makeDb();
    const threadId = createThread(db, { sessionId: 1, transport: "acp" });
    expect(getThreadAcpSessionId(db, threadId)).toBeNull();

    setThreadAcpSessionId(db, threadId, "acp-session-abc");
    expect(getThreadAcpSessionId(db, threadId)).toBe("acp-session-abc");
  });

  test("threads carry repo/file/line context when provided", () => {
    const db = makeDb();
    db.query(
      `INSERT INTO repos (workspace_relative_path, git_dir, created_at) VALUES ('repo-a', '/tmp/a/.git', 0)`,
    ).run();

    const threadId = createThread(db, {
      sessionId: 1,
      transport: "terminal",
      repoId: 1,
      filePath: "src/index.ts",
      startLine: 10,
      endLine: 15,
    });

    const threads = listThreads(db, 1);
    const thread = threads.find((t) => t.id === threadId)!;
    expect(thread.repoId).toBe(1);
    expect(thread.filePath).toBe("src/index.ts");
    expect(thread.startLine).toBe(10);
    expect(thread.endLine).toBe(15);
  });

  test("multiple questions in a thread each keep their own latest answer", () => {
    const db = makeDb();
    const threadId = createThread(db, { sessionId: 1, transport: "acp" });
    const q1 = addQuestion(db, threadId, "first question");
    const a1 = createPendingAnswer(db, q1);
    appendAnswerChunk(db, a1, "first answer");
    finalizeAnswer(db, a1);

    const q2 = addQuestion(db, threadId, "second question");
    const a2 = createPendingAnswer(db, q2);
    appendAnswerChunk(db, a2, "second answer");
    finalizeAnswer(db, a2);

    const threads = listThreads(db, 1);
    expect(threads[0]!.questions).toHaveLength(2);
    expect(threads[0]!.questions[0]!.answer?.body).toBe("first answer");
    expect(threads[0]!.questions[1]!.answer?.body).toBe("second answer");
  });
});
