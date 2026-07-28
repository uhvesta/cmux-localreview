import { afterEach, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { setupCopilot } from "./localreview-setup.ts";

const temporaryDirectories: string[] = [];

afterEach(async () => {
  await Promise.all(temporaryDirectories.splice(0).map((path) => rm(path, { recursive: true, force: true })));
});

async function temporaryRepository(): Promise<string> {
  const path = await mkdtemp(join(tmpdir(), "cmux-localreview-setup-"));
  temporaryDirectories.push(path);
  return path;
}

describe("localreview-setup", () => {
  test("installs repository skills and appends a bounded instruction block", async () => {
    const workspace = await temporaryRepository();
    const changes = await setupCopilot({ workspace, command: "localreview-submit", project: true });
    expect(changes.filter((change) => change.action === "created")).toHaveLength(4);
    const skill = await readFile(join(workspace, ".github", "skills", "localreview-submit", "SKILL.md"), "utf8");
    expect(skill).toContain("name: localreview-submit");
    expect(skill).toContain("localreview-submit . --title");
    const instructions = await readFile(join(workspace, ".github", "copilot-instructions.md"), "utf8");
    expect(instructions).toContain("cmux-localreview:start");
  });

  test("is idempotent and preserves an unmanaged skill path", async () => {
    const workspace = await temporaryRepository();
    const unmanaged = join(workspace, ".github", "skills", "localreview-feedback", "SKILL.md");
    await mkdir(join(workspace, ".github", "skills", "localreview-feedback"), { recursive: true });
    await writeFile(unmanaged, "user-owned skill\n");
    await setupCopilot({ workspace, project: true });
    expect(await readFile(unmanaged, "utf8")).toBe("user-owned skill\n");
    const rerun = await setupCopilot({ workspace, project: true });
    expect(rerun.find((change) => change.path === unmanaged)?.action).toBe("skipped");
  });

  test("dry runs without creating files", async () => {
    const workspace = await temporaryRepository();
    const changes = await setupCopilot({ workspace, project: true, dryRun: true });
    expect(changes.every((change) => change.action === "created")).toBe(true);
    await expect(readFile(join(workspace, ".github", "copilot-instructions.md"), "utf8")).rejects.toThrow();
  });

  test("automatically refreshes managed skills without touching unmanaged files", async () => {
    const workspace = await temporaryRepository();
    await setupCopilot({ workspace, project: true, command: "localreview-submit" });
    const skillPath = join(workspace, ".github", "skills", "localreview-submit", "SKILL.md");
    await writeFile(skillPath, `${await readFile(skillPath, "utf8")}\n<!-- local adjustment -->\n`);
    const refreshed = await setupCopilot({ workspace, project: true, command: "localreview-submit" });
    expect(refreshed.find((change) => change.path === skillPath)?.action).toBe("updated");
    expect(await readFile(skillPath, "utf8")).not.toContain("local adjustment");
  });
});
