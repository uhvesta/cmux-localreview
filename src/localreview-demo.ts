#!/usr/bin/env bun
/**
 * Start a disposable, credential-free review environment. It intentionally
 * creates both daemon state and its sample Git repository below one temporary
 * directory, never in the caller's checkout or home directory.
 */
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Command } from "commander";
import open from "open";

import { startGlobalDaemon } from "./global-daemon.ts";

export interface DemoPaths {
  root: string;
  dataDir: string;
  workspace: string;
}

export function createDemoPaths(dataDir?: string): DemoPaths {
  const root = dataDir
    ? resolve(dataDir)
    : mkdtempSync(join(tmpdir(), "cmux-localreview-demo-"));
  const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const relativeToSource = relative(sourceRoot, root);
  if (root === sourceRoot || (relativeToSource && !relativeToSource.startsWith("..") && !isAbsolute(relativeToSource))) {
    throw new Error("Refusing to use a demo data directory inside this source checkout. Choose a directory outside the source tree.");
  }
  return { root, dataDir: join(root, "daemon-data"), workspace: join(root, "sample-workspace") };
}

function createFixture(workspace: string): void {
  mkdirSync(join(workspace, "src"), { recursive: true, mode: 0o700 });
  execFileSync("git", ["init", "-q", workspace]);
  execFileSync("git", ["config", "user.email", "demo@example.invalid"], { cwd: workspace });
  execFileSync("git", ["config", "user.name", "cmux-localreview demo"], { cwd: workspace });
  writeFileSync(join(workspace, "README.md"), "# cmux-localreview demo\n\nThis repository is disposable.\n", "utf8");
  writeFileSync(join(workspace, "src", "review-target.ts"), "export function greeting(name: string): string {\n  return `Hello, ${name}`;\n}\n", "utf8");
  execFileSync("git", ["add", "."], { cwd: workspace });
  execFileSync("git", ["commit", "-qm", "Initial demo fixture"], { cwd: workspace });
  // Leave one visible working-tree change so opening the reviewer immediately
  // demonstrates a real diff without a GitHub/Copilot/cmux account.
  writeFileSync(join(workspace, "src", "review-target.ts"), "export function greeting(name: string): string {\n  const normalized = name.trim();\n  return normalized ? `Hello, ${normalized}` : \"Hello, stranger\";\n}\n", "utf8");
}

async function request(baseUrl: string, token: string, path: string, body: unknown): Promise<Response> {
  return fetch(`${baseUrl}${path}`, {
    method: "POST",
    headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

export async function startDemo(options: { dataDir?: string; fixture?: boolean } = {}) {
  const paths = createDemoPaths(options.dataDir);
  mkdirSync(paths.root, { recursive: true, mode: 0o700 });
  if (options.fixture !== false) createFixture(paths.workspace);
  const previousDataDir = process.env.CMUX_LOCALREVIEW_DATA_DIR;
  process.env.CMUX_LOCALREVIEW_DATA_DIR = paths.dataDir;
  try {
    const daemon = await startGlobalDaemon({ host: "127.0.0.1" });
    const baseUrl = `http://127.0.0.1:${daemon.discovery.port}`;
    let itemId: string | undefined;
    if (options.fixture !== false) {
      const queued = await request(baseUrl, daemon.discovery.token, "/api/queue", {
        workspacePath: paths.workspace,
        title: "Disposable demo review",
        body: "A locally generated fixture. No external account or repository was used.",
        base: "HEAD",
      });
      if (!queued.ok) throw new Error(`Could not create demo queue item: ${await queued.text()}`);
      itemId = (await queued.json() as { item: { id: string } }).item.id;
      const opened = await request(baseUrl, daemon.discovery.token, "/api/workspaces/open", { workspacePath: paths.workspace, base: "HEAD" });
      if (!opened.ok) throw new Error(`Could not open demo workspace: ${await opened.text()}`);
    }
    const grantResponse = await request(baseUrl, daemon.discovery.token, "/api/browser/grant", {});
    if (!grantResponse.ok) throw new Error(`Could not create demo browser bootstrap code: ${await grantResponse.text()}`);
    const { bootstrapCode } = await grantResponse.json() as { bootstrapCode: string };
    const url = new URL(options.fixture === false ? "/" : "/review", `${baseUrl}/`);
    url.hash = new URLSearchParams({ bootstrapCode }).toString();
    return { daemon, paths, baseUrl, reviewUrl: url.toString(), itemId, restoreEnvironment: () => {
      if (previousDataDir === undefined) delete process.env.CMUX_LOCALREVIEW_DATA_DIR;
      else process.env.CMUX_LOCALREVIEW_DATA_DIR = previousDataDir;
    } };
  } catch (error) {
    if (previousDataDir === undefined) delete process.env.CMUX_LOCALREVIEW_DATA_DIR;
    else process.env.CMUX_LOCALREVIEW_DATA_DIR = previousDataDir;
    throw error;
  }
}

export async function main(argv = process.argv): Promise<void> {
  const program = new Command();
  program
    .name("localreview-demo")
    .description("Start an isolated, credential-free local cmux-localreview demo")
    .option("--data-dir <path>", "throwaway parent directory; defaults to a new system temporary directory")
    .option("--no-fixture", "start Queue Home empty instead of creating a disposable Git fixture")
    .option("--open", "open the tokenized localhost URL in the default browser")
    .option("--json", "print machine-readable connection details")
    .action(async (options: { dataDir?: string; fixture?: boolean; open?: boolean; json?: boolean }) => {
      const demo = await startDemo({ dataDir: options.dataDir, fixture: options.fixture });
      const output = {
        url: demo.reviewUrl,
        // Do not derive another browser URL from the long-lived daemon
        // capability. `url` is the single, one-time bootstrap link produced
        // for this demo invocation.
        queueHomeUrl: demo.baseUrl,
        dataDir: demo.paths.dataDir,
        fixtureWorkspace: options.fixture === false ? null : demo.paths.workspace,
        queueItemId: demo.itemId ?? null,
        cleanup: `rm -rf ${JSON.stringify(demo.paths.root)}`,
      };
      if (options.open) await open(demo.reviewUrl);
      if (options.json) console.log(JSON.stringify(output, null, 2));
      else {
        console.log("cmux-localreview disposable demo is running (loopback only).");
        console.log(`Open in Chrome/Firefox: ${output.url}`);
        console.log(`Queue Home: ${output.queueHomeUrl}`);
        if (output.fixtureWorkspace) console.log(`Fixture workspace: ${output.fixtureWorkspace}`);
        console.log("No GitHub, Copilot, cmux, or SSH credentials were used.");
        console.log(`Press Ctrl-C to stop. Afterward, remove all demo data with: ${output.cleanup}`);
      }
      await new Promise<void>((resolveSignal) => {
        process.once("SIGINT", resolveSignal);
        process.once("SIGTERM", resolveSignal);
      });
      await demo.daemon.close();
      demo.restoreEnvironment();
    });
  await program.parseAsync(argv);
}

if (import.meta.main) {
  main().catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}
