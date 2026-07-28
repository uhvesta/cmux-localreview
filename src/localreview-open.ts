#!/usr/bin/env bun
import { resolve } from "node:path";
import { Command } from "commander";
import open from "open";

import { connectDaemon, type DaemonClient } from "./daemonClient.ts";

interface OpenWorkspaceResponse {
  workspacePath: string;
  repos: { id: string; path: string }[];
  reviewUrl: string;
}

interface OpenReadOnlyPullRequestResponse extends OpenWorkspaceResponse {
  pullRequest: { url: string; number: number; title: string; headSha: string; baseSha: string };
}

/** Create the short-lived browser handoff URL without exposing the daemon token. */
export async function browserUrl(daemon: DaemonClient, path: string): Promise<string> {
  const grant = await daemon.request<{ bootstrapCode: string }>("/api/browser/grant", { method: "POST" });
  const url = new URL(path, `${daemon.baseUrl}/`);
  // This is a one-time, 60-second bootstrap code—not the daemon discovery
  // capability. The client immediately exchanges and removes it from the URL.
  url.hash = new URLSearchParams({ bootstrapCode: grant.bootstrapCode }).toString();
  return url.toString();
}

async function main(): Promise<void> {
  const program = new Command();
  program
    .name("localreview-open")
    .description("Register a workspace with the review daemon and open its browser UI")
    .argument("[workspace]", "workspace directory", ".")
    .option("--base <ref>", "intended Git base ref for the review")
    .option("--pr <url>", "open a PR for local question-only review; no queue item or browser bearer token")
    .option("--home", "open Queue Home instead of a workspace review")
    .option("--open", "open the browser UI", true)
    .option("--no-open", "do not launch a browser")
    .option("--json", "write the daemon response as JSON")
    .action(async (workspace: string, options: { base?: string; pr?: string; home?: boolean; open: boolean; json?: boolean }) => {
      const workspacePath = resolve(workspace);
      const daemon = await connectDaemon();
      if (options.home) {
        const reviewUrl = await browserUrl(daemon, "/");
        if (options.open) await open(reviewUrl);
        if (options.json) {
          console.log(JSON.stringify({ reviewUrl, queueHome: true }, null, 2));
          return;
        }
        console.log(`Queue Home: ${reviewUrl}`);
        return;
      }
      if (options.pr) {
        const result = await daemon.request<OpenReadOnlyPullRequestResponse>("/api/local-review/pr", {
          method: "POST",
          body: JSON.stringify({ remoteUrl: options.pr }),
        });
        // Unlike normal queue/workspace review, this URL intentionally has no
        // daemon token. It supports diffs and /ask only; formal submission
        // remains behind the authenticated control plane.
        const reviewUrl = new URL(result.reviewUrl, `${daemon.baseUrl}/`).toString();
        if (options.open) await open(reviewUrl);
        if (options.json) {
          console.log(JSON.stringify({ ...result, reviewUrl, localQuestionOnly: true }, null, 2));
          return;
        }
        console.log(`Opened PR #${result.pullRequest.number} for local questions: ${result.pullRequest.title}`);
        console.log(`Review UI (no bearer token): ${reviewUrl}`);
        console.log("Use /ask for side-chat or inline questions. This review was not added to the queue and cannot publish feedback.");
        return;
      }
      await daemon.request<{ workspacePath: string }>("/api/workspaces", {
        method: "POST",
        body: JSON.stringify({ workspacePath }),
      });
      const result = await daemon.request<OpenWorkspaceResponse>("/api/workspaces/open", {
        method: "POST",
        body: JSON.stringify({ workspacePath, base: options.base }),
      });
      // The browser receives only a short-lived, one-time bootstrap code in
      // the fragment. It exchanges that code for an HttpOnly loopback cookie;
      // the daemon bearer capability never enters browser JavaScript or URLs.
      const reviewUrl = await browserUrl(daemon, result.reviewUrl || "/");
      if (options.open) await open(reviewUrl);
      if (options.json) {
        console.log(JSON.stringify({ ...result, reviewUrl }, null, 2));
        return;
      }
      console.log(`Opened workspace: ${result.workspacePath}`);
      console.log(`Review UI: ${reviewUrl}`);
      console.log(`Repositories: ${result.repos.length}`);
    });
  await program.parseAsync(process.argv);
}

if (import.meta.main) {
  main().catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}
