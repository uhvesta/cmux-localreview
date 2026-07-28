import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";

import { runMigrations } from "./db.ts";
import { decisionHistoryForItem, decideQueueItem, enqueue, requeueQueueItem } from "./queueStore.ts";

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
});
