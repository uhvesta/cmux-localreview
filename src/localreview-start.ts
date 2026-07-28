#!/usr/bin/env bun
import { resolve } from "node:path";
import { Command } from "commander";
import open from "open";

import { connectDaemon } from "./daemonClient.ts";
import { browserUrl } from "./localreview-open.ts";
import { setupCopilot } from "./localreview-setup.ts";
import { submitQueue } from "./queue-submit.ts";
import { GitHubAuthService } from "./server/githubAuth.ts";

interface OpenWorkspaceResponse { reviewUrl: string; workspacePath: string; }
interface OpenQueueResponse { reviewUrl: string; item: { id: string; title: string; status: string }; }

export interface StartOptions {
  title?: string;
  base?: string;
  topic?: string;
  submit?: boolean;
  setupCopilot?: boolean;
  personalSkills?: boolean;
  home?: boolean;
  open?: boolean;
  json?: boolean;
}

/**
 * The one-command local workflow.  It deliberately writes neither a GitHub
 * token nor a daemon capability to the workspace: optional setup writes only
 * managed Copilot instruction/skill files, and optional submission captures a
 * snapshot in the daemon's private data directory.
 */
export async function startLocalReview(workspace: string, options: StartOptions = {}): Promise<Record<string, unknown>> {
  const workspacePath = resolve(workspace);
  const setup = options.setupCopilot
    ? await setupCopilot({ workspace: workspacePath, project: true, personal: options.personalSkills })
    : [];
  const daemon = await connectDaemon();
  const github = await new GitHubAuthService().status();

  let result: Record<string, unknown>;
  let path: string;
  if (options.home) {
    path = "/";
    result = { queueHome: true };
  } else if (options.submit) {
    const submission = await submitQueue(workspacePath, { title: options.title, topic: options.topic, base: options.base });
    const opened = await daemon.request<OpenQueueResponse>(`/api/queue/${encodeURIComponent(submission.item.id)}/open`, { method: "POST" });
    path = opened.reviewUrl;
    result = { queue: submission, opened };
  } else {
    await daemon.request("/api/workspaces", { method: "POST", body: JSON.stringify({ workspacePath }) });
    const opened = await daemon.request<OpenWorkspaceResponse>("/api/workspaces/open", { method: "POST", body: JSON.stringify({ workspacePath, base: options.base }) });
    path = opened.reviewUrl;
    result = { opened };
  }
  const reviewUrl = await browserUrl(daemon, path);
  if (options.open !== false) await open(reviewUrl);
  return { workspacePath, reviewUrl, github, setup, ...result };
}

export async function main(argv = process.argv): Promise<void> {
  const program = new Command();
  program
    .name("localreview-start")
    .description("Start Queue Home or a workspace reviewer, optionally install Copilot skills and submit an immutable snapshot")
    .argument("[workspace]", "workspace directory", ".")
    .option("--home", "open Queue Home instead of a workspace")
    .option("--submit", "capture a snapshot, queue it, and open that exact review item")
    .option("--title <title>", "title for --submit")
    .option("--topic <topic>", "stable review stream topic for --submit")
    .option("--base <ref>", "base ref for workspace review or --submit")
    .option("--setup-copilot", "install managed project Copilot skills and instructions before opening")
    .option("--personal-skills", "with --setup-copilot, also install managed personal Copilot skills")
    .option("--no-open", "do not launch a browser")
    .option("--json", "write machine-readable results")
    .action(async (workspace: string, options: StartOptions) => {
      if (options.personalSkills && !options.setupCopilot) throw new Error("--personal-skills requires --setup-copilot");
      const result = await startLocalReview(workspace, options);
      if (options.json) {
        console.log(JSON.stringify(result, null, 2));
        return;
      }
      console.log(`Review UI: ${result.reviewUrl}`);
      const github = result.github as { provider: string };
      console.log(`GitHub credential source: ${github.provider}`);
      if (options.submit) console.log("Captured an immutable snapshot and opened the queued item.");
      else if (options.home) console.log("Opened Queue Home.");
      else console.log("Opened a workspace reviewer (no snapshot was submitted). Use --submit when work is ready for review.");
    });
  await program.parseAsync(argv);
}

if (import.meta.main) main().catch((error: unknown) => { console.error(error instanceof Error ? error.message : String(error)); process.exit(1); });
