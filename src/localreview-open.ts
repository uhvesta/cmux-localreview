#!/usr/bin/env bun
import { resolve } from "node:path";
import { Command } from "commander";
import open from "open";

import { connectDaemon } from "./daemonClient.ts";

interface OpenWorkspaceResponse {
  workspacePath: string;
  repos: { id: string; path: string }[];
  reviewUrl: string;
}

async function main(): Promise<void> {
  const program = new Command();
  program
    .name("localreview-open")
    .description("Register a workspace with the review daemon and open its browser UI")
    .argument("[workspace]", "workspace directory", ".")
    .option("--base <ref>", "intended Git base ref for the review")
    .option("--home", "open Queue Home instead of a workspace review")
    .option("--open", "open the browser UI", true)
    .option("--no-open", "do not launch a browser")
    .option("--json", "write the daemon response as JSON")
    .action(async (workspace: string, options: { base?: string; home?: boolean; open: boolean; json?: boolean }) => {
      const workspacePath = resolve(workspace);
      const daemon = await connectDaemon();
      if (options.home) {
        const reviewUrlObject = new URL("/", `${daemon.baseUrl}/`);
        reviewUrlObject.hash = new URLSearchParams({ daemonToken: daemon.discovery.token }).toString();
        const reviewUrl = reviewUrlObject.toString();
        if (options.open) await open(reviewUrl);
        if (options.json) {
          console.log(JSON.stringify({ reviewUrl, queueHome: true }, null, 2));
          return;
        }
        console.log(`Queue Home: ${reviewUrl}`);
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
      // The bearer token stays client-side in the URL fragment (and is never
      // sent in an HTTP request); the UI reads it to authenticate its daemon
      // control-plane calls.
      const reviewUrlObject = new URL(result.reviewUrl || "/", `${daemon.baseUrl}/`);
      reviewUrlObject.hash = new URLSearchParams({ daemonToken: daemon.discovery.token }).toString();
      const reviewUrl = reviewUrlObject.toString();
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

main().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
});
