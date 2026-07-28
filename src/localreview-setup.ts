#!/usr/bin/env bun
import { access, mkdir, readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Command } from "commander";

const MANAGED_MARKER = "cmux-localreview-managed";
const BLOCK_START = "<!-- cmux-localreview:start -->";
const BLOCK_END = "<!-- cmux-localreview:end -->";
const skillNames = ["localreview-submit", "localreview-feedback", "localreview-reproduce"] as const;

type SkillName = (typeof skillNames)[number];
export interface SetupOptions {
  workspace: string;
  command?: string;
  reproduceCommand?: string;
  dryRun?: boolean;
  force?: boolean;
  json?: boolean;
  personal?: boolean;
  project?: boolean;
}

export interface SetupChange {
  path: string;
  action: "created" | "updated" | "unchanged" | "skipped";
  reason?: string;
}

function sourceRoot(): string {
  return resolve(dirname(fileURLToPath(import.meta.url)), "..");
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\\"'\\\"'")}'`;
}

function configuredCommand(explicit?: string): string {
  if (explicit?.trim()) return explicit.trim();
  if (process.env.LOCALREVIEW_SUBMIT_COMMAND?.trim()) return process.env.LOCALREVIEW_SUBMIT_COMMAND.trim();
  return `bun ${shellQuote(join(sourceRoot(), "src", "queue-submit.ts"))}`;
}

function configuredReproduceCommand(explicit?: string): string {
  if (explicit?.trim()) return explicit.trim();
  if (process.env.LOCALREVIEW_REPRODUCE_COMMAND?.trim()) return process.env.LOCALREVIEW_REPRODUCE_COMMAND.trim();
  return `bun ${shellQuote(join(sourceRoot(), "src", "localreview-reproduce-copilot.ts"))}`;
}

async function exists(path: string): Promise<boolean> {
  try {
    await access(path);
    return true;
  } catch {
    return false;
  }
}

function isManaged(content: string): boolean {
  return content.includes(MANAGED_MARKER);
}

function tagSkill(content: string): string {
  return content.replace(/^---\n[\s\S]*?\n---\n/, (header) => `${header}\n<!-- ${MANAGED_MARKER}: skill -->\n`);
}

async function projectInstructions(command: string): Promise<string> {
  const asset = await readFile(join(sourceRoot(), "copilot", "copilot-instructions.md"), "utf8");
  return `${BLOCK_START}\n<!-- ${MANAGED_MARKER}: project instructions -->\n${asset.trim()}\n\n## Installed command\n\nTo submit a review from this checkout, run:\n\n\`\`\`sh\n${command} . --title "<review title>"\n\`\`\`\n${BLOCK_END}\n`;
}

async function writeManagedFile(path: string, content: string, options: SetupOptions, changes: SetupChange[]): Promise<void> {
  const pathExists = await exists(path);
  if (!pathExists) {
    changes.push({ path, action: "created" });
    if (!options.dryRun) {
      await mkdir(dirname(path), { recursive: true });
      await writeFile(path, content, "utf8");
    }
    return;
  }

  const current = await readFile(path, "utf8");
  if (current === content) {
    changes.push({ path, action: "unchanged" });
    return;
  }
  if (!isManaged(current)) {
    changes.push({ path, action: "skipped", reason: "existing file is not managed by cmux-localreview; it was preserved" });
    return;
  }
  changes.push({ path, action: "updated" });
  if (!options.dryRun) await writeFile(path, content, "utf8");
}

async function mergeInstructionFile(path: string, block: string, options: SetupOptions, changes: SetupChange[]): Promise<void> {
  const pathExists = await exists(path);
  if (!pathExists) {
    changes.push({ path, action: "created" });
    if (!options.dryRun) {
      await mkdir(dirname(path), { recursive: true });
      await writeFile(path, block, "utf8");
    }
    return;
  }
  const current = await readFile(path, "utf8");
  const start = current.indexOf(BLOCK_START);
  const end = current.indexOf(BLOCK_END);
  const next = start >= 0 && end >= start
    ? `${current.slice(0, start)}${block}${current.slice(end + BLOCK_END.length).replace(/^\n?/, "")}`
    : `${current.replace(/\s*$/, "")}\n\n${block}`;
  if (next === current) {
    changes.push({ path, action: "unchanged" });
    return;
  }
  changes.push({ path, action: start >= 0 ? "updated" : "updated", reason: start >= 0 ? undefined : "appended managed section without changing existing instructions" });
  if (!options.dryRun) await writeFile(path, next, "utf8");
}

async function loadSkill(name: SkillName, command: string, reproduceCommand: string): Promise<string> {
  const path = join(sourceRoot(), "copilot", "skills", name, "SKILL.md");
  const content = await readFile(path, "utf8");
  return tagSkill(content
    .replaceAll("localreview-submit .", `${command} .`)
    .replaceAll("localreview-reproduce-copilot ", `${reproduceCommand} `));
}

async function installSkills(destination: string, command: string, reproduceCommand: string, options: SetupOptions, changes: SetupChange[]): Promise<void> {
  for (const name of skillNames) {
    await writeManagedFile(join(destination, name, "SKILL.md"), await loadSkill(name, command, reproduceCommand), options, changes);
  }
}

/** Install project-local Copilot instructions and skills without replacing unmanaged files. */
export async function setupCopilot(options: SetupOptions): Promise<SetupChange[]> {
  if (!options.project && !options.personal) throw new Error("choose project setup or --personal");
  const workspace = resolve(options.workspace);
  const command = configuredCommand(options.command);
  const reproduceCommand = configuredReproduceCommand(options.reproduceCommand);
  const changes: SetupChange[] = [];

  if (options.project) {
    await installSkills(join(workspace, ".github", "skills"), command, reproduceCommand, options, changes);
    await mergeInstructionFile(join(workspace, ".github", "copilot-instructions.md"), await projectInstructions(command), options, changes);
  }
  if (options.personal) await installSkills(join(homedir(), ".copilot", "skills"), command, reproduceCommand, options, changes);
  return changes;
}

function displayPath(path: string): string {
  const cwdRelative = relative(process.cwd(), path);
  return cwdRelative && !cwdRelative.startsWith("..") ? cwdRelative : path;
}

export async function main(argv = process.argv): Promise<void> {
  const program = new Command();
  program
    .name("localreview-setup")
    .description("Install cmux-localreview Copilot skills and project instructions without replacing user-owned files")
    .argument("[workspace]", "repository to configure", ".")
    .option("--command <command>", "submit command used by installed skills (defaults to this checkout's Bun entrypoint)")
    .option("--reproduce-command <command>", "reproduction command used by installed skills")
    .option("--personal", "also install the skills in ~/.copilot/skills for Copilot CLI")
    .option("--no-project", "do not install .github skills/instructions in the target repository")
    .option("--dry-run", "show planned writes without changing files")
    .option("--force", "accepted for automation compatibility; managed skills update automatically and unmanaged user files are always preserved")
    .option("--json", "write machine-readable setup results")
    .action(async (workspace: string, options: Omit<SetupOptions, "workspace">) => {
      const changes = await setupCopilot({ ...options, workspace, project: options.project ?? true });
      if (options.json) {
        console.log(JSON.stringify({ workspace: resolve(workspace), command: configuredCommand(options.command), reproduceCommand: configuredReproduceCommand(options.reproduceCommand), dryRun: Boolean(options.dryRun), changes }, null, 2));
        return;
      }
      for (const change of changes) console.log(`${change.action.padEnd(9)} ${displayPath(change.path)}${change.reason ? ` — ${change.reason}` : ""}`);
      if (!options.dryRun) {
        console.log("\nCopilot CLI: start a new session or run /skills reload, then use /localreview-submit.");
        console.log("Run `localreview-setup --dry-run` any time to inspect an idempotent update.");
      }
    });
  await program.parseAsync(argv);
}

if (import.meta.main) {
  main().catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}
