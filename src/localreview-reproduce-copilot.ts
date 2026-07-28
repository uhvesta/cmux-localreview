#!/usr/bin/env bun
import { existsSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { Command } from "commander";

import { openDb } from "./server/db.ts";
import { daemonDbPath } from "./server/daemonPaths.ts";
import { getQueueItem } from "./server/queueStore.ts";
import { materializeSnapshot } from "./server/snapshots.ts";

function assertEmptyDestination(destination: string): void {
  if (existsSync(destination) && readdirSync(destination).length > 0) {
    throw new Error(`Destination must be new or empty: ${destination}`);
  }
}

async function main(): Promise<void> {
  const program = new Command();
  program
    .name("localreview-reproduce-copilot")
    .description("Reconstruct a queued review snapshot and restore Copilot ACP working conditions")
    .argument("<queue-id>", "queue item id with a retained snapshot")
    .argument("<destination>", "new isolated workspace destination")
    .option("--port <port>", "ACP port for a newly launched Copilot CLI", "4123")
    .option("--launch", "start `copilot --acp --port <port>` in the reconstructed cwd")
    .option("--json", "write details as JSON")
    .action(async (queueId: string, destination: string, options: { port: string; launch?: boolean; json?: boolean }) => {
      const db = openDb(daemonDbPath());
      let item;
      try { item = getQueueItem(db, queueId); } finally { db.close(); }
      if (!item) throw new Error(`Unknown queue item: ${queueId}`);
      if (!item.snapshotManifestPath) throw new Error(`Queue item ${queueId} has no retained snapshot`);
      const cwd = resolve(destination);
      assertEmptyDestination(cwd);
      const port = Number(options.port);
      if (!Number.isInteger(port) || port < 1 || port > 65_535) throw new Error("--port must be an integer from 1 through 65535");
      const manifest = await materializeSnapshot(item.snapshotManifestPath, cwd);
      const result = {
        cwd,
        snapshotId: manifest.id,
        queueId: item.id,
        // If this endpoint is still live, feedback can continue to target the
        // exact pre-existing agent session through the daemon.
        existingAcp: item.acpHost && item.acpPort && item.acpSessionId ? { host: item.acpHost, port: item.acpPort, sessionId: item.acpSessionId, state: item.acpState } : null,
        copilotSessionId: item.copilotSessionId,
        freshCommand: `cd ${JSON.stringify(cwd)} && copilot --acp --port ${port}`,
      };
      if (options.json) console.log(JSON.stringify(result, null, 2));
      else {
        console.log(`Reproduced review ${item.id}`);
        console.log(`cwd: ${cwd}`);
        if (result.existingAcp) console.log(`Existing ACP session: ${result.existingAcp.host}:${result.existingAcp.port} (${result.existingAcp.sessionId}, ${result.existingAcp.state})`);
        if (result.copilotSessionId) console.log(`Copilot resume guidance: /resume ${result.copilotSessionId}`);
        console.log(`Fresh ACP setup: ${result.freshCommand}`);
      }
      if (options.launch) {
        const child = Bun.spawn({ cmd: ["copilot", "--acp", "--port", String(port)], cwd, stdin: "inherit", stdout: "inherit", stderr: "inherit" });
        process.exit(await child.exited);
      }
    });
  await program.parseAsync(process.argv);
}

main().catch((error: unknown) => { console.error(error instanceof Error ? error.message : String(error)); process.exit(1); });
