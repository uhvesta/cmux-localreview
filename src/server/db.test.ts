import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";

import { runMigrations } from "./db.ts";

describe("runMigrations", () => {
  test("creates all schema tables and sets user_version", () => {
    const db = new Database(":memory:");
    runMigrations(db);

    const version = db.query("PRAGMA user_version").get() as { user_version: number };
    expect(version.user_version).toBeGreaterThan(0);

    const tables = db
      .query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
      .all() as { name: string }[];
    const names = tables.map((t) => t.name);

    for (const expected of [
      "workspace",
      "repos",
      "sessions",
      "comments",
      "btw_threads",
      "btw_questions",
      "btw_answers",
      "exports",
      "ui_state",
    ]) {
      expect(names).toContain(expected);
    }
  });

  test("is idempotent — running twice does not error or duplicate tables", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    runMigrations(db);

    const tables = db
      .query("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'comments'")
      .all();
    expect(tables.length).toBe(1);
  });

  test("workspace table enforces a single row via CHECK(id = 1)", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    db.query("INSERT INTO workspace (id, root_path, created_at) VALUES (1, '/tmp/x', 0)").run();
    expect(() =>
      db.query("INSERT INTO workspace (id, root_path, created_at) VALUES (2, '/tmp/y', 0)").run(),
    ).toThrow();
  });
});
