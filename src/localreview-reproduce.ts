#!/usr/bin/env bun
import { existsSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { Command } from "commander";

import { openDb } from "./server/db.ts";
import { daemonDbPath } from "./server/daemonPaths.ts";
import { materializeSnapshot } from "./server/snapshots.ts";

function savedCopilotSession(manifestPath: string): string | undefined {
  if (!existsSync(daemonDbPath())) return undefined;
  const db = openDb(daemonDbPath());
  try {
    const row = db.query(`SELECT copilot_session_id FROM queue_items WHERE snapshot_manifest_path = ? AND copilot_session_id IS NOT NULL ORDER BY updated_at DESC LIMIT 1`).get(manifestPath) as { copilot_session_id: string } | null;
    return row?.copilot_session_id;
  } finally {
    db.close();
  }
}

async function main(): Promise<void> {
  const program = new Command();
  program
    .name("localreview-reproduce")
    .description("Verify and reconstruct a retained local-review snapshot")
    .argument("<manifest>", "path to snapshot manifest.json")
    .argument("<destination>", "new isolated destination directory")
    .option("--session-id <id>", "Copilot session to resume (defaults to the queued item's stored session)")
    .option("--json", "write reconstruction details as JSON")
    .action(async (manifest: string, destination: string, options: { sessionId?: string; json?: boolean }) => {
      const manifestPath = resolve(manifest);
      const cwd = resolve(destination);
      if (existsSync(cwd) && readdirSync(cwd).length > 0) {
        throw new Error(`Destination must be new or empty: ${cwd}`);
      }
      const reconstructed = await materializeSnapshot(manifestPath, cwd);
      const sessionId = options.sessionId ?? savedCopilotSession(manifestPath);
      const result = {
        cwd,
        snapshotId: reconstructed.id,
        repos: reconstructed.repos.map((repo) => ({ path: repo.workspaceRelativePath, snapshotSha: repo.snapshotSha })),
        copilotSessionId: sessionId ?? null,
        resume: sessionId ? `/resume ${sessionId}` : null,
      };
      if (options.json) {
        console.log(JSON.stringify(result, null, 2));
        return;
      }
      console.log(`Reproduced snapshot ${reconstructed.id}`);
      console.log(`cwd: ${cwd}`);
      for (const repo of result.repos) console.log(`repo: ${repo.path} @ ${repo.snapshotSha}`);
      if (result.resume) console.log(`Copilot: ${result.resume}`);
      else console.log("Copilot: no session id was retained; pass --session-id to print resume guidance.");
    });
  await program.parseAsync(process.argv);
}

main().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
});
