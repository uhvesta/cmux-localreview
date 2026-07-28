#!/usr/bin/env bun
import { Command } from "commander";

import { GitHubAuthService, type GitHubCapability } from "./server/githubAuth.ts";

const capabilities: GitHubCapability[] = ["read", "write", "copilot"];

const appGuide: Record<GitHubCapability, { title: string; permissions: string; purpose: string }> = {
  read: {
    title: "cmux-localreview PR Read",
    permissions: "Repository permissions: Metadata: Read-only; Contents: Read-only; Pull requests: Read-only.",
    purpose: "Resolves a PR and creates a local immutable mirror/worktree. Install it only on repositories you review.",
  },
  write: {
    title: "cmux-localreview PR Publish",
    permissions: "Repository permissions: Metadata: Read-only; Pull requests: Read and write.",
    purpose: "Publishes only an explicit review action from Queue Home. Keep this unconnected unless you need publication.",
  },
  copilot: {
    title: "cmux-localreview Copilot Ask",
    permissions: "Enable the Copilot user-token permission required by your GitHub Copilot SDK plan. Do not grant repository permissions unless your organization requires it.",
    purpose: "Authenticates fresh, read-only Copilot SDK /ask conversations separately from GitHub PR publication.",
  },
};

function capability(value: string): GitHubCapability {
  if (capabilities.includes(value as GitHubCapability)) return value as GitHubCapability;
  throw new Error("capability must be read, write, or copilot");
}

function guide(): void {
  console.log("Create three dedicated GitHub Apps at https://github.com/settings/apps/new (or your organization’s GitHub App settings).\n");
  for (const key of capabilities) {
    const item = appGuide[key];
    console.log(`${key}: ${item.title}\n  ${item.permissions}\n  ${item.purpose}\n  Enable Device Flow, create the App, copy its Client ID, then run:\n  localreview-github-app configure --capability ${key} --client-id Iv1.…\n`);
  }
  console.log("The device flow does not need a client secret. The client ID is public configuration; issued access/refresh tokens stay in macOS Keychain or Linux libsecret under cmux-localreview.github-app.");
}

async function waitForAuthorization(service: GitHubAuthService, selected: GitHubCapability): Promise<void> {
  const start = await service.start(selected);
  console.log(`Open ${start.verificationUri} and enter ${start.userCode}. Waiting for GitHub authorization…`);
  for (;;) {
    await new Promise((resolve) => setTimeout(resolve, 2_000));
    const state = await service.poll(selected);
    if (state.authenticated) { console.log(`Connected ${selected} as @${state.login ?? "GitHub user"}.`); return; }
    if (state.loginState === "failed") throw new Error(state.message ?? state.error ?? "GitHub App authorization failed.");
  }
}

export async function main(argv = process.argv): Promise<void> {
  const program = new Command();
  program.name("localreview-github-app").description("Configure dedicated GitHub App device-flow capabilities for cmux-localreview");
  program.command("guide").description("Print the three least-privilege GitHub App registrations to create").action(guide);
  program.command("configure").requiredOption("--capability <read|write|copilot>").requiredOption("--client-id <id>").description("Save a public GitHub App client ID").action((options: { capability: string; clientId: string }) => {
    const selected = capability(options.capability);
    new GitHubAuthService().configure(selected, options.clientId);
    console.log(`Saved ${selected} GitHub App client ID. Run localreview-github-app connect --capability ${selected}.`);
  });
  program.command("connect").requiredOption("--capability <read|write|copilot>").description("Run the GitHub App device flow and keep its token in the system secret store").action(async (options: { capability: string }) => {
    await waitForAuthorization(new GitHubAuthService(), capability(options.capability));
  });
  program.command("status").description("Show capability status without revealing credentials").action(async () => {
    console.log(JSON.stringify(await new GitHubAuthService().status(), null, 2));
  });
  program.command("disconnect").requiredOption("--capability <read|write|copilot>").description("Remove one capability token from the local secret store").action(async (options: { capability: string }) => {
    const selected = capability(options.capability);
    await new GitHubAuthService().disconnect(selected);
    console.log(`Disconnected ${selected} locally.`);
  });
  await program.parseAsync(argv);
}

if (import.meta.main) main().catch((error: unknown) => { console.error(error instanceof Error ? error.message : String(error)); process.exit(1); });
