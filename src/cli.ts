#!/usr/bin/env bun
import { createHash } from "node:crypto";
import { homedir } from "node:os";
import { resolve, join } from "node:path";
import { Command } from "commander";
import open from "open";

import { openDb } from "./server/db.ts";
import { buildWorkspaceApp } from "./server/app.ts";
import { WsHub } from "./server/wsHub.ts";

interface CliOptions {
  port?: string;
  host: string;
  open: boolean;
  base?: string;
  db?: string;
  agent: string;
  dryRun: boolean;
  clean: boolean;
  keepAlive: boolean;
}

import type { Express } from "express";
import type { Server } from "node:http";

function listenWithFallback(app: Express, preferredPort: number, host: string): Promise<Server> {
  return new Promise((resolvePromise, reject) => {
    const server = app.listen(preferredPort, host, (err?: NodeJS.ErrnoException) => {
      if (!err) {
        resolvePromise(server);
        return;
      }
      if (err.code === "EADDRINUSE") {
        // eslint-disable-next-line no-console
        console.log(`cmux-localreview: port ${preferredPort} is busy, trying ${preferredPort + 1}...`);
        listenWithFallback(app, preferredPort + 1, host).then(resolvePromise, reject);
        return;
      }
      reject(err);
    });
  });
}

function defaultDbPath(workspaceRoot: string): string {
  const hash = createHash("sha256").update(workspaceRoot).digest("hex").slice(0, 16);
  return join(homedir(), ".local", "share", "cmux-localreview", `${hash}.db`);
}

async function main() {
  const program = new Command();

  program
    .name("cmux-localreview")
    .description("Local, browser-based multi-repo code review tool")
    .argument("[dir]", "workspace directory to review", ".")
    .option("-p, --port <port>", "port to listen on")
    .option("--host <host>", "host address to bind", "127.0.0.1")
    .option("--open", "open browser automatically", true)
    .option("--no-open", "do not open browser automatically")
    .option("--base <ref>", "workspace-default diff base (branch/commit, or '.', 'staged', 'working')")
    .option("--db <path>", "override the SQLite database path")
    .option("--agent <provider|cmd>", "ACP agent for /btw (claude|opencode|gemini|<cmd>)", "claude")
    .option("--dry-run", "log cmux/ACP sends instead of connecting/spawning", false)
    .option("--clean", "start a fresh review session", false)
    .option("--keep-alive", "keep server running after last browser tab disconnects", false)
    .action(async (dir: string, options: CliOptions) => {
      const workspaceRoot = resolve(dir);
      const dbPath = options.db ? resolve(options.db) : defaultDbPath(workspaceRoot);

      const db = openDb(dbPath);
      const now = Date.now();
      db.query(
        `INSERT INTO workspace (id, root_path, created_at) VALUES (1, ?, ?)
         ON CONFLICT(id) DO UPDATE SET root_path = excluded.root_path`,
      ).run(workspaceRoot, now);

      // eslint-disable-next-line no-console
      console.log(`cmux-localreview: workspace=${workspaceRoot}`);
      // eslint-disable-next-line no-console
      console.log(`cmux-localreview: db=${dbPath}`);
      if (options.dryRun) {
        // eslint-disable-next-line no-console
        console.log("cmux-localreview: --dry-run (cmux/ACP sends will be logged, not sent)");
      }

      const { app, repos, startWatchers } = await buildWorkspaceApp({
        workspaceRoot,
        db,
        base: options.base,
        dryRun: options.dryRun,
      });
      // eslint-disable-next-line no-console
      console.log(
        `cmux-localreview: found ${repos.length} repo(s): ${repos.map((r) => r.repo.workspaceRelativePath).join(", ") || "(none)"}`,
      );

      const preferredPort = options.port ? Number.parseInt(options.port, 10) : 4977;
      const server = await listenWithFallback(app, preferredPort, options.host);
      const address = server.address();
      const port = address && typeof address !== "string" ? address.port : preferredPort;
      const url = `http://${options.host === "0.0.0.0" ? "localhost" : options.host}:${port}`;
      // eslint-disable-next-line no-console
      console.log(`cmux-localreview: server listening at ${url}`);

      const hub = new WsHub(server);
      await startWatchers(hub);

      let shuttingDown = false;
      const shutdown = async () => {
        if (shuttingDown) return;
        shuttingDown = true;
        // eslint-disable-next-line no-console
        console.log("cmux-localreview: shutting down");
        hub.close();
        server.close();
        db.close();
        process.exit(0);
      };
      process.on("SIGINT", () => void shutdown());
      process.on("SIGTERM", () => void shutdown());

      if (options.open) {
        try {
          await open(url);
        } catch {
          // eslint-disable-next-line no-console
          console.warn("cmux-localreview: failed to open browser automatically");
        }
      }
    });

  await program.parseAsync(process.argv);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
