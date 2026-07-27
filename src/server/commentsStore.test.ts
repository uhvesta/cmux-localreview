import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";

import { runMigrations } from "./db.ts";
import {
  upsertRepoRow,
  getActiveSessionId,
  upsertThread,
  listThreads,
  deleteThread,
  refreshOrphanFlags,
  type DiffCommentThread,
} from "./commentsStore.ts";

function makeDb(): Database {
  const db = new Database(":memory:");
  runMigrations(db);
  return db;
}

const fakeRepo = {
  workspaceRelativePath: "repo-a",
  absolutePath: "/tmp/repo-a",
  gitDir: "/tmp/repo-a/.git",
  remoteUrl: null,
};

function makeThread(overrides: Partial<DiffCommentThread> = {}): DiffCommentThread {
  return {
    id: "thread-1",
    filePath: "a.txt",
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    position: { side: "new", line: 3 },
    codeSnapshot: { content: "hello world" },
    messages: [
      {
        id: "msg-1",
        body: "please fix this",
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
      },
    ],
    ...overrides,
  };
}

describe("upsertRepoRow", () => {
  test("is idempotent by gitDir and returns a stable id", () => {
    const db = makeDb();
    const id1 = upsertRepoRow(db, fakeRepo);
    const id2 = upsertRepoRow(db, fakeRepo);
    expect(id1).toBe(id2);
  });
});

describe("getActiveSessionId", () => {
  test("creates one session and reuses it", () => {
    const db = makeDb();
    const s1 = getActiveSessionId(db);
    const s2 = getActiveSessionId(db);
    expect(s1).toBe(s2);
  });
});

describe("upsertThread / listThreads / deleteThread", () => {
  test("round-trips a thread including messages and code snapshot", () => {
    const db = makeDb();
    const repoId = upsertRepoRow(db, fakeRepo);
    const sessionId = getActiveSessionId(db);

    upsertThread(db, sessionId, repoId, makeThread());

    const threads = listThreads(db, sessionId, repoId);
    expect(threads).toHaveLength(1);
    expect(threads[0]!.id).toBe("thread-1");
    expect(threads[0]!.messages[0]!.body).toBe("please fix this");
    expect(threads[0]!.codeSnapshot?.content).toBe("hello world");
    expect(threads[0]!.position.line).toBe(3);
  });

  test("upsert with same thread id updates in place instead of duplicating", () => {
    const db = makeDb();
    const repoId = upsertRepoRow(db, fakeRepo);
    const sessionId = getActiveSessionId(db);

    upsertThread(db, sessionId, repoId, makeThread());
    upsertThread(
      db,
      sessionId,
      repoId,
      makeThread({
        messages: [
          {
            id: "msg-1",
            body: "edited body",
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          },
        ],
      }),
    );

    const threads = listThreads(db, sessionId, repoId);
    expect(threads).toHaveLength(1);
    expect(threads[0]!.messages[0]!.body).toBe("edited body");
  });

  test("comments in different repos don't collide despite the same thread id", () => {
    const db = makeDb();
    const repoA = upsertRepoRow(db, fakeRepo);
    const repoB = upsertRepoRow(db, { ...fakeRepo, workspaceRelativePath: "repo-b", gitDir: "/tmp/repo-b/.git" });
    const sessionId = getActiveSessionId(db);

    upsertThread(db, sessionId, repoA, makeThread());
    upsertThread(db, sessionId, repoB, makeThread());

    expect(listThreads(db, sessionId, repoA)).toHaveLength(1);
    expect(listThreads(db, sessionId, repoB)).toHaveLength(1);
  });

  test("deleteThread removes it and returns false for unknown ids", () => {
    const db = makeDb();
    const repoId = upsertRepoRow(db, fakeRepo);
    const sessionId = getActiveSessionId(db);

    upsertThread(db, sessionId, repoId, makeThread());
    expect(deleteThread(db, sessionId, repoId, "thread-1")).toBe(true);
    expect(listThreads(db, sessionId, repoId)).toHaveLength(0);
    expect(deleteThread(db, sessionId, repoId, "thread-1")).toBe(false);
  });
});

describe("refreshOrphanFlags", () => {
  test("flags a thread orphaned when its anchored content no longer matches", async () => {
    const db = makeDb();
    const repoId = upsertRepoRow(db, fakeRepo);
    const sessionId = getActiveSessionId(db);
    upsertThread(db, sessionId, repoId, makeThread());

    await refreshOrphanFlags(db, sessionId, repoId, () => "completely different content now");

    const threads = listThreads(db, sessionId, repoId);
    expect(threads[0]!.orphaned).toBe(true);
  });

  test("keeps a thread un-orphaned when content still matches", async () => {
    const db = makeDb();
    const repoId = upsertRepoRow(db, fakeRepo);
    const sessionId = getActiveSessionId(db);
    upsertThread(db, sessionId, repoId, makeThread());

    await refreshOrphanFlags(db, sessionId, repoId, () => "hello world");

    const threads = listThreads(db, sessionId, repoId);
    expect(threads[0]!.orphaned).toBe(false);
  });

  test("a thread with no code snapshot is never orphaned", async () => {
    const db = makeDb();
    const repoId = upsertRepoRow(db, fakeRepo);
    const sessionId = getActiveSessionId(db);
    upsertThread(db, sessionId, repoId, makeThread({ codeSnapshot: undefined }));

    await refreshOrphanFlags(db, sessionId, repoId, () => undefined);

    const threads = listThreads(db, sessionId, repoId);
    expect(threads[0]!.orphaned).toBe(false);
  });

  test("flags orphaned when the file/line no longer exists", async () => {
    const db = makeDb();
    const repoId = upsertRepoRow(db, fakeRepo);
    const sessionId = getActiveSessionId(db);
    upsertThread(db, sessionId, repoId, makeThread());

    await refreshOrphanFlags(db, sessionId, repoId, () => undefined);

    const threads = listThreads(db, sessionId, repoId);
    expect(threads[0]!.orphaned).toBe(true);
  });
});
