#!/usr/bin/env bun
import { resolve } from "node:path";
import { Command } from "commander";

import { connectDaemon } from "./daemonClient.ts";

interface QueueResponse {
  created: boolean;
  item: { id: string; title: string; status: string; workspacePath: string; snapshotManifestPath?: string | null };
}

function isRemotePullRequest(value: string): boolean {
  try {
    const url = new URL(value);
    return (url.protocol === "https:" || url.protocol === "http:") && /\/pull\/\d+\/?$/.test(url.pathname);
  } catch {
    return false;
  }
}

async function main(): Promise<void> {
  const program = new Command();
  program
    .name("queue-submit")
    .description("Snapshot a workspace and submit it to the local review queue")
    .argument("[workspace]", "workspace directory or GitHub pull-request URL", ".")
    .option("--title <title>", "review title")
    .option("--body <body>", "review description")
    .option("--base <ref>", "intended Git base ref for the review")
    .option("--idempotent-key <key>", "return an existing queue item with this key")
    .option("--json", "write the daemon response as JSON")
    .action(async (workspace: string, options: { title?: string; body?: string; base?: string; idempotentKey?: string; json?: boolean }) => {
      const remoteUrl = isRemotePullRequest(workspace) ? workspace : undefined;
      const workspacePath = remoteUrl ? undefined : resolve(workspace);
      const title = options.title ?? (remoteUrl ? `Review ${remoteUrl}` : `Review ${workspacePath}`);
      const daemon = await connectDaemon();
      const result = await daemon.request<QueueResponse>("/api/queue", {
        method: "POST",
        body: JSON.stringify({ title, body: options.body, base: options.base, idempotentKey: options.idempotentKey, workspacePath, remoteUrl }),
      });
      if (options.json) {
        console.log(JSON.stringify(result, null, 2));
        return;
      }
      console.log(`${result.created ? "Queued" : "Already queued"}: ${result.item.title}`);
      console.log(`id: ${result.item.id}`);
      console.log(`workspace: ${result.item.workspacePath}`);
      if (result.item.snapshotManifestPath) console.log(`snapshot: ${result.item.snapshotManifestPath}`);
    });
  await program.parseAsync(process.argv);
}

main().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
});
