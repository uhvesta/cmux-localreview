import { afterEach, describe, expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { startGlobalDaemon } from "./global-daemon.ts";

const roots: string[] = [];

function temporaryRoot(): string {
  const root = mkdtempSync(join(tmpdir(), "cmux-localreview-global-daemon-e2e-"));
  roots.push(root);
  return root;
}

function createRepository(path: string): void {
  execFileSync("git", ["init", "-q", path]);
  execFileSync("git", ["config", "user.email", "fixture@example.invalid"], { cwd: path });
  execFileSync("git", ["config", "user.name", "fixture"], { cwd: path });
  writeFileSync(join(path, "reviewed.ts"), "export const answer = 42;\n");
  execFileSync("git", ["add", "."], { cwd: path });
  execFileSync("git", ["commit", "-qm", "fixture"], { cwd: path });
}

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

describe("global daemon authenticated queue lifecycle", () => {
  test("captures a snapshot and drives a queue item from submission through feedback and requeue", async () => {
    const root = temporaryRoot();
    const workspace = join(root, "workspace");
    createRepository(workspace);
    const priorDataDir = process.env.CMUX_LOCALREVIEW_DATA_DIR;
    process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "daemon-data");
    const token = "global-daemon-e2e-token";
    const daemon = await startGlobalDaemon({ token });
    const baseUrl = `http://127.0.0.1:${daemon.discovery.port}`;
    const authed = (path: string, init: RequestInit = {}) => fetch(`${baseUrl}${path}`, {
      ...init,
      headers: { authorization: `Bearer ${token}`, ...(init.headers ?? {}) },
    });

    try {
      expect((await fetch(`${baseUrl}/health`)).status).toBe(200);
      expect((await fetch(`${baseUrl}/api/queue`)).status).toBe(401);

      const createdResponse = await authed("/api/queue", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          workspacePath: workspace,
          title: "Daemon lifecycle fixture",
          body: "Submit an immutable local review.",
          agentId: "fixture-agent",
          agentProvider: "copilot",
          provenance: { fixture: true, cmux: { surfaceId: "surface-fixture" } },
        }),
      });
      expect(createdResponse.status).toBe(201);
      const created = await createdResponse.json() as { item: { id: string; status: string; snapshotManifestPath: string | null; provenance: unknown } };
      expect(created.item.status).toBe("queued");
      expect(created.item.snapshotManifestPath).toContain("manifest.json");
      expect(created.item.provenance).toEqual({ fixture: true, cmux: { surfaceId: "surface-fixture" } });

      const queued = await (await authed("/api/queue")).json() as { items: { id: string; status: string }[] };
      expect(queued.items.some((item) => item.id === created.item.id && item.status === "queued")).toBe(true);

      const feedbackResponse = await authed(`/api/queue/${created.item.id}/feedback`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ body: "Handle the boundary case.", path: "reviewed.ts", line: 1 }),
      });
      expect(feedbackResponse.status).toBe(201);
      const prompt = await (await authed(`/api/queue/${created.item.id}/feedback/prompt`)).text();
      expect(prompt).toContain("reviewed.ts:1: Handle the boundary case.");

      const opened = await authed("/api/queue/open-next", { method: "POST" });
      expect(opened.status).toBe(200);
      expect((await opened.json() as { item: { status: string } }).item.status).toBe("in_review");

      const requestedChanges = await authed(`/api/queue/${created.item.id}/decision`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ decision: "changes_requested", body: "Please cover the edge case." }),
      });
      expect(requestedChanges.status).toBe(200);
      expect((await requestedChanges.json() as { item: { status: string } }).item.status).toBe("changes_requested");

      const requeued = await authed(`/api/queue/${created.item.id}/requeue`, { method: "POST" });
      expect(requeued.status).toBe(200);
      const detail = await (await authed(`/api/queue/${created.item.id}`)).json() as {
        item: { status: string };
        feedback: { body: string; deliveredAt: number | null }[];
        decisions: { status: string }[];
      };
      expect(detail.item.status).toBe("queued");
      expect(detail.feedback).toHaveLength(1);
      expect(detail.feedback[0]).toMatchObject({ body: "Handle the boundary case.", deliveredAt: null });
      expect(detail.decisions.map((decision) => decision.status)).toEqual(["changes_requested", "requeued"]);

      const reproduce = await (await authed(`/api/queue/${created.item.id}/reproduce`)).json() as { snapshot: { id: string } | null; commands: { reproduceSnapshot: string } | null };
      expect(reproduce.snapshot?.id).toBeTruthy();
      expect(reproduce.commands?.reproduceSnapshot).toContain("localreview-reproduce");
    } finally {
      await daemon.close();
      if (priorDataDir === undefined) delete process.env.CMUX_LOCALREVIEW_DATA_DIR;
      else process.env.CMUX_LOCALREVIEW_DATA_DIR = priorDataDir;
    }
  });
});
