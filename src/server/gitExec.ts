import { existsSync, mkdirSync } from "node:fs";
import { basename, join } from "node:path";

export interface CommandResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export async function runCommand(command: string[], cwd?: string, env?: Record<string, string>): Promise<CommandResult> {
  const child = Bun.spawn({ cmd: command, cwd, env: { ...process.env, ...env }, stdout: "pipe", stderr: "pipe" });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { stdout, stderr, exitCode };
}

export async function git(args: string[], cwd: string, env?: Record<string, string>): Promise<string> {
  const result = await runCommand(["git", ...args], cwd, env);
  if (result.exitCode !== 0) throw new Error(`git ${args.join(" ")} failed in ${cwd}: ${result.stderr.trim()}`);
  return result.stdout.trim();
}

export function safeArtifactName(value: string): string {
  return basename(value).replace(/[^a-zA-Z0-9._-]/g, "_") || "repo";
}

export function ensureDir(path: string): void {
  if (!existsSync(path)) mkdirSync(path, { recursive: true, mode: 0o700 });
}

export function tempIndexPath(directory: string, id: string): string {
  return join(directory, `.index-${safeArtifactName(id)}`);
}
