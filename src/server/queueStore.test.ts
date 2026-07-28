import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";

import { runMigrations } from "./db.ts";
import { decisionHistoryForItem, decideQueueItem, enqueue, refreshRemoteQueue, requeueQueueItem } from "./queueStore.ts";

describe("queue provenance", () => {
  test("persists source fingerprint and supersession link", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const first = enqueue(db, { title: "first", workspacePath: "/tmp/repo", sourceFingerprint: "one" }).item;
    const second = enqueue(db, {
      title: "second",
      workspacePath: "/tmp/repo",
      sourceFingerprint: "two",
      supersedesId: first.id,
      provenance: { version: 1, caller: { cmuxSurfaceId: "surface-1" } },
    }).item;
    expect(second.sourceFingerprint).toBe("two");
    expect(second.supersedesId).toBe(first.id);
    expect(second.provenance).toEqual({ version: 1, caller: { cmuxSurfaceId: "surface-1" } });
  });

  test("keeps lifecycle history when a completed review is requeued", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const item = enqueue(db, { title: "review", workspacePath: "/tmp/repo" }).item;
    decideQueueItem(db, item.id, "changes_requested", "Please handle the edge case.");
    const requeued = requeueQueueItem(db, item.id)!;
    expect(requeued.status).toBe("queued");
    expect(decisionHistoryForItem(db, item.id).map((entry) => [entry.status, entry.body])).toEqual([
      ["changes_requested", "Please handle the edge case."],
      ["requeued", "Requeued by reviewer."],
    ]);
  });

  test("uses a sticky PR identity but creates a fresh immutable round after a terminal review", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const input = {
      title: "PR 9", workspacePath: "/tmp/pr-9", remoteUrl: "https://github.com/example/repo/pull/9",
      snapshotManifest: { remotePullRequest: { headSha: "head-one" } }, sourceFingerprint: "head-one",
    };
    const first = refreshRemoteQueue(db, input).item;
    decideQueueItem(db, first.id, "approved", "Looks good.");
    const second = refreshRemoteQueue(db, input);
    expect(second.created).toBe(true);
    expect(second.headChanged).toBe(false);
    expect(second.item.id).not.toBe(first.id);
    expect(second.item.supersedesId).toBe(first.id);
    expect(second.item.identityKey).toBe(first.identityKey);
    expect(second.item.status).toBe("queued");
  });
});
