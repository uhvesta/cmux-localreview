#!/usr/bin/env bun
import { resolve } from "node:path";
import { Command } from "commander";

import { connectDaemon } from "./daemonClient.ts";
import { captureSubmissionProvenance } from "./server/submissionContext.ts";

interface QueueResponse {
  created: boolean;
  item: { id: string; title: string; status: string; workspacePath: string; snapshotManifestPath?: string | null; sourceFingerprint?: string | null };
}

export interface QueueSubmitOptions {
  title?: string; topic?: string; body?: string; base?: string; idempotentKey?: string;
  agentId?: string; agentProvider?: string; agentKind?: string; copilotSessionId?: string;
  acpHost?: string; acpPort?: number; acpSessionId?: string; feedbackTarget?: string;
  watch?: boolean; pollInterval?: number;
}

export function isRemotePullRequest(value: string): boolean {
  try {
    const url = new URL(value);
    return (url.protocol === "https:" || url.protocol === "http:") && /\/pull\/\d+\/?$/.test(url.pathname);
  } catch {
    return false;
  }
}

/** Submit a snapshot from code as well as from the CLI. Secrets never enter this object. */
export async function submitQueue(workspace: string, options: QueueSubmitOptions = {}): Promise<QueueResponse & { watch?: unknown }> {
  const remoteUrl = isRemotePullRequest(workspace) ? workspace : undefined;
  const workspacePath = remoteUrl ? undefined : resolve(workspace);
  const title = options.title ?? (remoteUrl ? `Review ${remoteUrl}` : `Review ${workspacePath}`);
  const daemon = await connectDaemon();
  if ([options.acpHost, options.acpPort, options.acpSessionId].some((value) => value !== undefined) && !(options.acpHost && options.acpPort && options.acpSessionId)) {
    throw new Error("acpHost, acpPort, and acpSessionId must be supplied together");
  }
  const provenance = workspacePath ? await captureSubmissionProvenance(workspacePath, {
    agent: options.agentId ? { id: options.agentId, provider: options.agentProvider } : undefined,
    feedbackTarget: options.feedbackTarget,
  }) : undefined;
  const result = await daemon.request<QueueResponse>("/api/queue", {
    method: "POST",
    body: JSON.stringify({ title, topic: options.topic, body: options.body, base: options.base, idempotentKey: options.idempotentKey, workspacePath, remoteUrl, agentId: options.agentId, agentProvider: options.agentProvider, agentKind: options.agentKind, copilotSessionId: options.copilotSessionId, acpHost: options.acpHost, acpPort: options.acpPort, acpSessionId: options.acpSessionId, feedbackTarget: options.feedbackTarget, provenance }),
  });
  if (!options.watch) return result;
  if (!workspacePath) throw new Error("watch is only supported for a local Git workspace");
  if (options.pollInterval !== undefined && (!Number.isInteger(options.pollInterval) || options.pollInterval < 1_000)) throw new Error("pollInterval must be an integer of at least 1000ms");
  const watch = await daemon.request("/api/queue/watch", {
    method: "POST",
    body: JSON.stringify({ workspacePath, title, body: options.body, base: options.base, agentId: options.agentId, agentProvider: options.agentProvider, agentKind: options.agentKind, copilotSessionId: options.copilotSessionId, acpHost: options.acpHost, acpPort: options.acpPort, acpSessionId: options.acpSessionId, feedbackTarget: options.feedbackTarget, provenance, lastQueueItemId: result.item.id, lastFingerprint: result.item.sourceFingerprint, ...(options.pollInterval ? { pollIntervalMs: options.pollInterval } : {}) }),
  });
  return { ...result, watch };
}

async function main(): Promise<void> {
  const program = new Command();
  program
    .name("queue-submit")
    .description("Snapshot a workspace and submit it to the local review queue")
    .argument("[workspace]", "workspace directory or GitHub pull-request URL", ".")
    .option("--title <title>", "review title")
    .option("--topic <topic>", "stable local review topic (path + topic identify one review stream)")
    .option("--body <body>", "review description")
    .option("--base <ref>", "intended Git base ref for the review")
    .option("--idempotent-key <key>", "return an existing queue item with this key")
    .option("--agent-id <id>", "originating ACP/agent identifier")
    .option("--agent-provider <provider>", "originating agent provider")
    .option("--agent-kind <kind>", "agent implementation (for example copilot-cli)")
    .option("--copilot-session-id <id>", "Copilot CLI session id, for resume guidance in a reproduced review")
    .option("--acp-host <host>", "loopback ACP host (remote agents use an SSH local forward)")
    .option("--acp-port <port>", "ACP TCP port")
    .option("--acp-session-id <id>", "existing ACP session id to receive review feedback")
    .option("--feedback-target <target>", "where review feedback should be delivered")
    .option("--watch", "continue to auto-queue when this Git worktree changes")
    .option("--poll-interval <ms>", "git auto-queue polling interval in milliseconds")
    .option("--json", "write the daemon response as JSON")
    .action(async (workspace: string, options: { title?: string; topic?: string; body?: string; base?: string; idempotentKey?: string; agentId?: string; agentProvider?: string; agentKind?: string; copilotSessionId?: string; acpHost?: string; acpPort?: string; acpSessionId?: string; feedbackTarget?: string; watch?: boolean; pollInterval?: string; json?: boolean }) => {
      const acpPort = options.acpPort === undefined ? undefined : Number(options.acpPort);
      if (options.acpPort !== undefined && (!Number.isInteger(acpPort) || acpPort! < 1 || acpPort! > 65535)) throw new Error("--acp-port must be an integer from 1 through 65535");
      const result = await submitQueue(workspace, { ...options, acpPort, pollInterval: options.pollInterval ? Number(options.pollInterval) : undefined });
      if (options.json) {
        console.log(JSON.stringify(result, null, 2));
        return;
      }
      console.log(`${result.created ? "Queued" : "Already queued"}: ${result.item.title}`);
      console.log(`id: ${result.item.id}`);
      console.log(`workspace: ${result.item.workspacePath}`);
      if (result.item.snapshotManifestPath) console.log(`snapshot: ${result.item.snapshotManifestPath}`);
      if (options.watch) console.log("auto-queue: enabled (new Git source revisions will be queued)");
    });
  await program.parseAsync(process.argv);
}

if (import.meta.main) {
  main().catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}
