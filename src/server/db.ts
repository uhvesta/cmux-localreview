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
  {
    // Global-daemon data lives in a SQLite database too.  Keeping the queue
    // schema in the regular migration stream lets a workspace DB be used for
    // standalone/offline operation, while the daemon normally uses daemon.db.
    version: 3,
    up: (db) => {
      db.run(`CREATE TABLE workspace_registry (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        root_path TEXT NOT NULL UNIQUE,
        label TEXT,
        last_opened_at INTEGER NOT NULL,
        active INTEGER NOT NULL DEFAULT 0
      )`);
      db.run(`CREATE TABLE queue_items (
        id TEXT PRIMARY KEY,
        idempotent_key TEXT UNIQUE,
        title TEXT NOT NULL,
        body TEXT NOT NULL DEFAULT '',
        workspace_path TEXT NOT NULL,
        kind TEXT NOT NULL DEFAULT 'local' CHECK(kind IN ('local','remote')),
        remote_url TEXT,
        status TEXT NOT NULL CHECK(status IN ('queued','in_review','changes_requested','approved','completed')),
        position INTEGER NOT NULL,
        agent_id TEXT,
        agent_provider TEXT,
        copilot_session_id TEXT,
        snapshot_manifest_path TEXT,
        snapshot_manifest_json TEXT,
        feedback_target TEXT,
        decision_body TEXT,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )`);
      db.run(`CREATE INDEX idx_queue_items_status_position ON queue_items(status, position)`);
      db.run(`CREATE TABLE queue_feedback (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        queue_item_id TEXT NOT NULL REFERENCES queue_items(id) ON DELETE CASCADE,
        body TEXT NOT NULL,
        path TEXT,
        line INTEGER,
        created_at INTEGER NOT NULL
      )`);
      db.run(`CREATE TABLE agent_registry (
        id TEXT PRIMARY KEY,
        provider TEXT NOT NULL,
        command TEXT,
        workspace_path TEXT,
        review_session_id TEXT,
        status TEXT NOT NULL DEFAULT 'connected',
        metadata_json TEXT,
        updated_at INTEGER NOT NULL
      )`);
      db.run(`CREATE TABLE ask_conversations (
        id TEXT PRIMARY KEY,
        queue_item_id TEXT,
        model TEXT,
        copilot_session_id TEXT,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )`);
      db.run(`CREATE TABLE ask_messages (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        conversation_id TEXT NOT NULL REFERENCES ask_conversations(id) ON DELETE CASCADE,
        role TEXT NOT NULL CHECK(role IN ('user','assistant','system')),
        body TEXT NOT NULL,
        pending INTEGER NOT NULL DEFAULT 0,
        created_at INTEGER NOT NULL
      )`);
    },
  },
  {
    version: 4,
    up: (db) => {
      db.run(`ALTER TABLE queue_items ADD COLUMN base_ref TEXT`);
    },
  },
  {
    version: 5,
    up: (db) => {
      db.run(`ALTER TABLE ui_state ADD COLUMN revision INTEGER NOT NULL DEFAULT 0`);
    },
  },
  {
    // Submission provenance is intentionally JSON: cmux evolves its surface
    // payload independently, and preserving the original capture is more
    // useful than flattening a lossy subset into columns.
    version: 6,
    up: (db) => {
      db.run(`ALTER TABLE queue_items ADD COLUMN provenance_json TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN source_fingerprint TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN supersedes_id TEXT REFERENCES queue_items(id)`);
      db.run(`CREATE INDEX idx_queue_items_workspace_fingerprint ON queue_items(workspace_path, source_fingerprint)`);
      db.run(`CREATE TABLE queue_watchers (
        workspace_path TEXT PRIMARY KEY,
        enabled INTEGER NOT NULL DEFAULT 1,
        poll_interval_ms INTEGER NOT NULL DEFAULT 5000,
        title TEXT,
        body TEXT,
        base_ref TEXT,
        agent_id TEXT,
        agent_provider TEXT,
        feedback_target TEXT,
        provenance_json TEXT,
        last_fingerprint TEXT,
        last_queue_item_id TEXT REFERENCES queue_items(id),
        updated_at INTEGER NOT NULL
      )`);
    },
  },
  {
    // A remote node's bearer token is stored only in the owner-only daemon
    // database. SSH forwards remote loopback to local loopback on demand.
    version: 7,
    up: (db) => {
      db.run(`CREATE TABLE federation_nodes (
        id TEXT PRIMARY KEY,
        label TEXT NOT NULL,
        ssh_target TEXT NOT NULL,
        remote_port INTEGER NOT NULL,
        token TEXT NOT NULL,
        enabled INTEGER NOT NULL DEFAULT 1,
        last_connected_at INTEGER,
        last_error TEXT,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )`);
    },
  },
  {
    // A target agent's terminal binding and delivery health are durable. The
    // terminal /btw thread also records the exact agent it was routed to.
    version: 8,
    up: (db) => {
      db.run(`ALTER TABLE agent_registry ADD COLUMN surface_id TEXT`);
      db.run(`ALTER TABLE agent_registry ADD COLUMN last_seen_at INTEGER`);
      db.run(`ALTER TABLE agent_registry ADD COLUMN last_error TEXT`);
      db.run(`ALTER TABLE agent_registry ADD COLUMN reconnect_attempts INTEGER NOT NULL DEFAULT 0`);
      db.run(`ALTER TABLE btw_threads ADD COLUMN target_agent_id TEXT`);
      db.run(`CREATE INDEX idx_agent_registry_workspace_status ON agent_registry(workspace_path, status)`);
      db.run(`CREATE INDEX idx_btw_threads_target_agent ON btw_threads(target_agent_id)`);
    },
  },
  {
    // ACP endpoints are always loopback addresses. Remote machines are
    // represented by an SSH local forward, so this daemon never becomes an
    // arbitrary TCP proxy just because a queue item was submitted.
    version: 9,
    up: (db) => {
      db.run(`ALTER TABLE queue_items ADD COLUMN acp_host TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN acp_port INTEGER`);
      db.run(`ALTER TABLE queue_items ADD COLUMN acp_session_id TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN agent_kind TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN acp_state TEXT NOT NULL DEFAULT 'unavailable' CHECK(acp_state IN ('unavailable','connecting','idle','busy','error'))`);
      db.run(`ALTER TABLE queue_items ADD COLUMN acp_last_error TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN acp_updated_at INTEGER`);
      db.run(`ALTER TABLE queue_feedback ADD COLUMN delivered_at INTEGER`);
    },
  },
  {
    // Named question sets let reviewers keep a reusable, ordered review
    // checklist and submit it into a persisted /ask conversation later.
    version: 10,
    up: (db) => {
      db.run(`CREATE TABLE question_sets (
        id TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
      )`);
      db.run(`CREATE TABLE questions (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        question_set_id TEXT NOT NULL REFERENCES question_sets(id) ON DELETE CASCADE,
        body TEXT NOT NULL,
        position INTEGER NOT NULL,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL,
        UNIQUE(question_set_id, position)
      )`);
      db.run(`CREATE INDEX idx_questions_set_position ON questions(question_set_id, position)`);
    },
  },
  {
    // An auto-queue revision represents the same submitting agent. Preserve
    // its ACP endpoint so feedback on the newer snapshot still reaches the
    // original live session rather than silently becoming copy/paste-only.
    version: 11,
    up: (db) => {
      db.run(`ALTER TABLE queue_watchers ADD COLUMN acp_host TEXT`);
      db.run(`ALTER TABLE queue_watchers ADD COLUMN acp_port INTEGER`);
      db.run(`ALTER TABLE queue_watchers ADD COLUMN acp_session_id TEXT`);
      db.run(`ALTER TABLE queue_watchers ADD COLUMN agent_kind TEXT`);
      db.run(`ALTER TABLE queue_watchers ADD COLUMN copilot_session_id TEXT`);
    },
  },
  {
    // /ask is deliberately a separate research channel from review feedback.
    // Keep its code-location context with the conversation/messages so an
    // inline question can be resumed without being exported as a comment.
    version: 12,
    up: (db) => {
      db.run(`ALTER TABLE ask_conversations ADD COLUMN context_json TEXT`);
      db.run(`ALTER TABLE ask_messages ADD COLUMN location_json TEXT`);
      db.run(`CREATE INDEX idx_ask_messages_conversation ON ask_messages(conversation_id, id)`);
    },
  },
  {
    // A queue item's current status is useful for scheduling, but reviewers
    // also need an auditable lifecycle when deciding whether it is safe to
    // re-open, reproduce, or publish a remote review.
    version: 13,
    up: (db) => {
      db.run(`CREATE TABLE queue_decisions (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        queue_item_id TEXT NOT NULL REFERENCES queue_items(id) ON DELETE CASCADE,
        status TEXT NOT NULL CHECK(status IN ('approved','changes_requested','completed','requeued')),
        body TEXT,
        created_at INTEGER NOT NULL
      )`);
      db.run(`CREATE INDEX idx_queue_decisions_item_created ON queue_decisions(queue_item_id, created_at, id)`);
    },
  },
  {
    // Browser tabs can retain an old full-comment snapshot. Resolving a
    // thread must win permanently over that stale state, rather than letting
    // a later POST recreate the same client-generated thread id.
    version: 14,
    up: (db) => {
      db.run(`CREATE TABLE comment_tombstones (
        session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
        repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
        thread_id TEXT NOT NULL,
        deleted_at INTEGER NOT NULL,
        PRIMARY KEY(session_id, repo_id, thread_id)
      )`);
    },
  },
  {
    // Persist /ask model controls with the long-lived conversation. Inline
    // anchors remain message metadata, so every code location shares the
    // same Copilot context without losing its precise display location.
    version: 15,
    up: (db) => {
      db.run(`ALTER TABLE ask_conversations ADD COLUMN reasoning_effort TEXT`);
      db.run(`ALTER TABLE ask_conversations ADD COLUMN context_tier TEXT`);
    },
  },
  {
    // `/ask` follows review rounds too. New Review creates a new active
    // formal session, so its Copilot research must start clean while the
    // previous round remains available as historical context.
    version: 16,
    up: (db) => {
      db.run(`ALTER TABLE ask_conversations ADD COLUMN review_session_id INTEGER REFERENCES sessions(id)`);
      db.run(`ALTER TABLE ask_conversations ADD COLUMN archived_at INTEGER`);
      db.run(`CREATE INDEX idx_ask_conversations_session_state ON ask_conversations(review_session_id, archived_at, updated_at DESC)`);
    },
  },
  {
    // A queue entry is a revision of a review stream, not merely a display
    // title.  The identity key lets a stable local topic or a GitHub PR keep
    // its history while each re-submission remains an immutable snapshot.
    version: 17,
    up: (db) => {
      db.run(`ALTER TABLE queue_items ADD COLUMN identity_key TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN review_topic TEXT`);
      db.run(`ALTER TABLE queue_items ADD COLUMN removed_at INTEGER`);
      db.run(`ALTER TABLE queue_items ADD COLUMN removed_reason TEXT`);
      db.run(`CREATE INDEX idx_queue_items_identity_created ON queue_items(identity_key, created_at DESC)`);
      db.run(`CREATE INDEX idx_queue_items_active_position ON queue_items(removed_at, status, position)`);
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
