import { Database } from "bun:sqlite";
import { existsSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

type Migration = {
  version: number;
  up: (db: Database) => void;
};

// PRAGMA user_version starts at 0; each migration bumps it by exactly one.
// Migrations must be append-only — never edit a migration once it has
// shipped, add a new one instead.
const MIGRATIONS: Migration[] = [
  {
    version: 1,
    up: (db) => {
      db.run(`
        CREATE TABLE workspace (
          id INTEGER PRIMARY KEY CHECK (id = 1),
          root_path TEXT NOT NULL,
          created_at INTEGER NOT NULL
        )
      `);
      db.run(`
        CREATE TABLE repos (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          workspace_relative_path TEXT NOT NULL,
          git_dir TEXT NOT NULL,
          remote_url TEXT,
          base_override TEXT,
          created_at INTEGER NOT NULL,
          UNIQUE (git_dir)
        )
      `);
      db.run(`
        CREATE TABLE sessions (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          label TEXT,
          started_at INTEGER NOT NULL,
          frozen_at INTEGER
        )
      `);
      db.run(`
        CREATE TABLE comments (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          session_id INTEGER NOT NULL REFERENCES sessions(id),
          repo_id INTEGER NOT NULL REFERENCES repos(id),
          file_path TEXT NOT NULL,
          side TEXT NOT NULL CHECK (side IN ('old', 'new')),
          start_line INTEGER NOT NULL,
          end_line INTEGER NOT NULL,
          body TEXT NOT NULL,
          anchor_content_hash TEXT NOT NULL,
          orphaned INTEGER NOT NULL DEFAULT 0,
          created_at INTEGER NOT NULL,
          updated_at INTEGER NOT NULL
        )
      `);
      db.run(`
        CREATE TABLE btw_threads (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          session_id INTEGER NOT NULL REFERENCES sessions(id),
          transport TEXT NOT NULL CHECK (transport IN ('acp', 'terminal')),
          acp_provider TEXT,
          acp_session_id TEXT,
          repo_id INTEGER REFERENCES repos(id),
          file_path TEXT,
          start_line INTEGER,
          end_line INTEGER,
          created_at INTEGER NOT NULL
        )
      `);
      db.run(`
        CREATE TABLE btw_questions (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          thread_id INTEGER NOT NULL REFERENCES btw_threads(id),
          body TEXT NOT NULL,
          created_at INTEGER NOT NULL
        )
      `);
      db.run(`
        CREATE TABLE btw_answers (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          question_id INTEGER NOT NULL REFERENCES btw_questions(id),
          body TEXT NOT NULL,
          pending INTEGER NOT NULL DEFAULT 0,
          created_at INTEGER NOT NULL,
          updated_at INTEGER NOT NULL
        )
      `);
      db.run(`
        CREATE TABLE exports (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          session_id INTEGER NOT NULL REFERENCES sessions(id),
          content TEXT NOT NULL,
          destination TEXT NOT NULL CHECK (destination IN ('clipboard', 'cmux')),
          created_at INTEGER NOT NULL
        )
      `);
      db.run(`
        CREATE TABLE ui_state (
          key TEXT PRIMARY KEY,
          value TEXT NOT NULL,
          updated_at INTEGER NOT NULL
        )
      `);
    },
  },
  {
    version: 2,
    up: (db) => {
      // difit's client already speaks in "threads" (a position + an ordered
      // list of messages, supporting replies/edits) via /api/comments*; we
      // keep that shape server-side instead of collapsing to one message per
      // row. `thread_id` is the client-generated id (createId()); `body` is
      // kept as a denormalized copy of the first message's body so simple
      // queries/exports don't need to parse messages_json.
      db.run(`ALTER TABLE comments ADD COLUMN thread_id TEXT`);
      db.run(`ALTER TABLE comments ADD COLUMN messages_json TEXT`);
      db.run(`ALTER TABLE comments ADD COLUMN anchor_content TEXT`);
      db.run(
        `CREATE UNIQUE INDEX idx_comments_thread ON comments(session_id, repo_id, thread_id)`,
      );
    },
  },
];

export function runMigrations(db: Database): void {
  db.run("PRAGMA foreign_keys = ON");
  const currentVersion = db.query("PRAGMA user_version").get() as { user_version: number };
  let version = currentVersion.user_version;

  const pending = MIGRATIONS.filter((m) => m.version > version).sort(
    (a, b) => a.version - b.version,
  );

  for (const migration of pending) {
    if (migration.version !== version + 1) {
      throw new Error(
        `Migration gap: expected version ${version + 1}, got ${migration.version}`,
      );
    }
    db.transaction(() => {
      migration.up(db);
      db.run(`PRAGMA user_version = ${migration.version}`);
    })();
    version = migration.version;
  }
}

export function openDb(dbPath: string): Database {
  const dir = dirname(dbPath);
  if (!existsSync(dir)) {
    mkdirSync(dir, { recursive: true });
  }
  const db = new Database(dbPath, { create: true });
  db.run("PRAGMA journal_mode = WAL");
  runMigrations(db);
  return db;
}
