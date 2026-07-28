import { afterEach, describe, expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { startGlobalDaemon } from "./global-daemon.ts";
import { GitHubAuthService } from "./server/githubAuth.ts";
import { startFakeAcpTcpAgent } from "./server/testFixtures/fakeAcpTcpAgent.ts";
import type { SecretStore } from "./server/secretStore.ts";

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

function memorySecretStore(): SecretStore {
  const values = new Map<string, string>();
  const key = (service: string, account: string) => `${service}\0${account}`;
  return { get: async (service, account) => values.get(key(service, account)), set: async (service, account, value) => { values.set(key(service, account), value); }, remove: async (service, account) => { values.delete(key(service, account)); } };
}

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

describe("global daemon authenticated queue lifecycle", () => {
  test("exposes only dedicated GitHub App capability setup, never a token or gh fallback", async () => {
    const root = temporaryRoot();
    const previousDataDir = process.env.CMUX_LOCALREVIEW_DATA_DIR;
    process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "daemon-data");
    const auth = new GitHubAuthService(memorySecretStore(), fetch, async () => undefined, join(root, "github-apps.json"));
    const daemon = await startGlobalDaemon({ token: "github-app-e2e", githubAuthService: auth });
    const authed = (path: string, init: RequestInit = {}) => fetch(`http://127.0.0.1:${daemon.discovery.port}${path}`, { ...init, headers: { authorization: "Bearer github-app-e2e", ...(init.headers ?? {}) } });
    try {
      const initial = await (await authed("/api/github/auth/status")).json() as { provider: string; capabilities: { read: { configured: boolean } } };
      expect(initial.provider).toBe("github-app-device-flow");
      expect(initial.capabilities.read.configured).toBe(false);
      const grant = await authed("/api/browser/grant", { method: "POST" });
      expect(grant.status).toBe(201);
      const { bootstrapCode } = await grant.json() as { bootstrapCode: string };
      const session = await fetch(`http://127.0.0.1:${daemon.discovery.port}/api/browser/session`, { method: "POST", headers: { authorization: `Bearer ${bootstrapCode}` } });
      expect(session.status).toBe(204);
      const browserCookie = session.headers.get("set-cookie");
      expect(browserCookie).toContain("HttpOnly");
      expect(browserCookie).toContain("SameSite=Strict");
      expect(browserCookie).not.toContain("github-app-e2e");
      expect((await fetch(`http://127.0.0.1:${daemon.discovery.port}/api/browser/session`, { method: "POST", headers: { authorization: `Bearer ${bootstrapCode}` } })).status).toBe(401);
      const cookieHeader = browserCookie!.split(";", 1)[0]!;
      // A browser session authorizes Queue Home without re-sending the daemon
      // master capability, exactly as a real HttpOnly cookie would.
      expect((await fetch(`http://127.0.0.1:${daemon.discovery.port}/api/github/auth/status`, { headers: { cookie: cookieHeader } })).status).toBe(200);
      expect((await fetch(`http://127.0.0.1:${daemon.discovery.port}/api/queue/open-next`, { method: "POST", headers: { cookie: cookieHeader, origin: "http://attacker.invalid", "sec-fetch-site": "cross-site" } })).status).toBe(403);
      const configured = await authed("/api/github/auth/configure", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ capability: "read", clientId: "Iv1.fixtureRead" }) });
      expect(configured.status).toBe(204);
      const statusText = await (await authed("/api/github/auth/status")).text();
      expect(statusText).toContain("github-app-device-flow");
      expect(statusText).not.toContain("access_token");
      expect(statusText).not.toContain("gh auth");
    } finally {
      await daemon.close();
      if (previousDataDir === undefined) delete process.env.CMUX_LOCALREVIEW_DATA_DIR;
      else process.env.CMUX_LOCALREVIEW_DATA_DIR = previousDataDir;
    }
  });

  test("captures a snapshot, delivers feedback to ACP, removes a reviewed item, and resubmits its stable topic", async () => {
    const root = temporaryRoot();
    const workspace = join(root, "workspace");
    createRepository(workspace);
    const priorDataDir = process.env.CMUX_LOCALREVIEW_DATA_DIR;
    process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "daemon-data");
    const token = "global-daemon-e2e-token";
    const daemon = await startGlobalDaemon({ token });
    const acp = await startFakeAcpTcpAgent();
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
          acpHost: "127.0.0.1",
          acpPort: acp.port,
          acpSessionId: "fixture-live-session",
          topic: "parser-boundaries",
          provenance: { fixture: true, cmux: { surfaceId: "surface-fixture" } },
        }),
      });
      expect(createdResponse.status).toBe(201);
      const created = await createdResponse.json() as { item: { id: string; status: string; snapshotManifestPath: string | null; provenance: unknown; identityKey: string; reviewTopic: string } };
      expect(created.item.status).toBe("queued");
      expect(created.item.snapshotManifestPath).toContain("manifest.json");
      expect(created.item.provenance).toEqual({ fixture: true, cmux: { surfaceId: "surface-fixture" } });
      expect(created.item.reviewTopic).toBe("parser-boundaries");
      expect(created.item.identityKey).toBe(`local:${workspace}:parser-boundaries`);

      writeFileSync(join(workspace, "reviewed.ts"), "export const answer = 43;\n");
      const secondResponse = await authed("/api/queue", {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ workspacePath: workspace, title: "Second stream", topic: "separate-topic" }),
      });
      const second = await secondResponse.json() as { item: { id: string } };
      // The source can change after submit; opening must use the retained
      // snapshot rather than silently reviewing those later edits.
      writeFileSync(join(workspace, "reviewed.ts"), "export const answer = 999;\n");
      const openedSpecific = await authed(`/api/queue/${second.item.id}/open`, { method: "POST" });
      expect(openedSpecific.status).toBe(200);
      const openedSpecificJson = await openedSpecific.json() as { item: { id: string; status: string }; workspacePath: string; sourceWorkspacePath: string; reviewUrl: string };
      expect(openedSpecificJson).toMatchObject({ item: { id: second.item.id, status: "in_review" }, sourceWorkspacePath: workspace, reviewUrl: `/review?queueItem=${second.item.id}` });
      expect(openedSpecificJson.workspacePath).not.toBe(workspace);
      expect(readFileSync(join(openedSpecificJson.workspacePath, "reviewed.ts"), "utf8")).toBe("export const answer = 43;\n");

      // A formal inline diff thread is queue feedback, using a path relative
      // to the review workspace. `/ask` threads are deliberately excluded.
      const repos = await (await fetch(`${baseUrl}/api/repos`)).json() as { repos: { id: string }[] };
      const immutableDiff = await (await fetch(`${baseUrl}/api/repos/${repos.repos[0]!.id}/api/diff`)).json() as { files: { path: string }[] };
      expect(immutableDiff.files.map((file) => file.path)).toContain("reviewed.ts");
      const commentsUrl = `${baseUrl}/api/repos/${repos.repos[0]!.id}/api/comments`;
      const savedFormal = await fetch(commentsUrl, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ comments: [{ id: "formal-thread", file: "reviewed.ts", line: 1, body: "Use a named constant." }] }),
      });
      expect(savedFormal.status).toBe(200);
      const savedAsk = await fetch(commentsUrl, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ comments: [
          { id: "formal-thread", file: "reviewed.ts", line: 1, body: "Use a named constant." },
          { id: "ask-thread", file: "reviewed.ts", line: 1, body: "/ask why is this here?" },
        ] }),
      });
      expect(savedAsk.status).toBe(200);
      const formalDetail = await (await authed(`/api/queue/${second.item.id}`)).json() as { feedback: { body: string; path: string | null; sourceKey: string | null }[] };
      expect(formalDetail.feedback).toHaveLength(1);
      expect(formalDetail.feedback[0]).toMatchObject({ body: "Use a named constant.", path: "reviewed.ts", sourceKey: expect.stringContaining("formal:") });
      expect((await fetch(`${commentsUrl}/formal-thread`, { method: "DELETE" })).status).toBe(200);
      const resolvedDetail = await (await authed(`/api/queue/${second.item.id}`)).json() as { feedback: unknown[] };
      expect(resolvedDetail.feedback).toEqual([]);
      const afterSpecificOpen = await (await authed("/api/queue?history=true")).json() as { items: { id: string; status: string }[] };
      expect(afterSpecificOpen.items.find((item) => item.id === created.item.id)?.status).toBe("queued");
      expect(afterSpecificOpen.items.find((item) => item.id === second.item.id)?.status).toBe("in_review");

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

      const delivered = await authed(`/api/queue/${created.item.id}/deliver-feedback`, {
        method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ policy: "queue" }),
      });
      expect(delivered.status).toBe(200);
      expect(acp.prompts).toHaveLength(1);
      expect(acp.prompts[0]).toContain("reviewed.ts:1: Handle the boundary case.");

      const opened = await authed("/api/queue/open-next", { method: "POST" });
      expect(opened.status).toBe(200);
      expect((await opened.json() as { item: { status: string } }).item.status).toBe("in_review");
      // Opening/reopening the reviewer is never an ACP delivery path. The one
      // prompt above came only from the explicit deliver-feedback request.
      expect(acp.prompts).toHaveLength(1);
      const reopened = await authed("/api/workspaces/open", {
        method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ workspacePath: workspace }),
      });
      expect(reopened.status).toBe(200);
      expect(acp.prompts).toHaveLength(1);

      const requestedChanges = await authed(`/api/queue/${created.item.id}/decision`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ decision: "changes_requested", body: "Please cover the edge case." }),
      });
      expect(requestedChanges.status).toBe(200);
      expect((await requestedChanges.json() as { item: { status: string } }).item.status).toBe("changes_requested");

      // Terminal reviews leave the active queue, while history retains the
      // review round and delivered-feedback audit trail.
      const activeAfterDecision = await (await authed("/api/queue")).json() as { items: { id: string }[] };
      expect(activeAfterDecision.items.some((item) => item.id === created.item.id)).toBe(false);
      const historyAfterDecision = await (await authed("/api/queue?history=true")).json() as { items: { id: string }[] };
      expect(historyAfterDecision.items.some((item) => item.id === created.item.id)).toBe(true);

      const requeued = await authed(`/api/queue/${created.item.id}/requeue`, { method: "POST" });
      expect(requeued.status).toBe(200);
      const detail = await (await authed(`/api/queue/${created.item.id}`)).json() as {
        item: { status: string };
        feedback: { body: string; deliveredAt: number | null }[];
        decisions: { status: string }[];
      };
      expect(detail.item.status).toBe("queued");
      expect(detail.feedback).toHaveLength(1);
      expect(detail.feedback[0]).toMatchObject({ body: "Handle the boundary case." });
      expect(detail.feedback[0].deliveredAt).toBeNumber();
      expect(detail.decisions.map((decision) => decision.status)).toEqual(["changes_requested", "requeued"]);

      const reproduce = await (await authed(`/api/queue/${created.item.id}/reproduce`)).json() as { snapshot: { id: string } | null; commands: { reproduceSnapshot: string } | null };
      expect(reproduce.snapshot?.id).toBeTruthy();
      expect(reproduce.commands?.reproduceSnapshot).toContain("localreview-reproduce");

      const removed = await authed(`/api/queue/${created.item.id}`, { method: "DELETE" });
      expect(removed.status).toBe(200);
      const activeAfterRemove = await (await authed("/api/queue")).json() as { items: { id: string }[] };
      expect(activeAfterRemove.items.some((item) => item.id === created.item.id)).toBe(false);

      writeFileSync(join(workspace, "reviewed.ts"), "export const answer = 43;\n");
      const resubmitted = await authed("/api/queue", {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ workspacePath: workspace, title: "Renamed review", topic: "parser-boundaries" }),
      });
      expect(resubmitted.status).toBe(201);
      const replacement = await resubmitted.json() as { item: { id: string; supersedesId: string | null; identityKey: string; status: string } };
      expect(replacement.item.id).not.toBe(created.item.id);
      expect(replacement.item.supersedesId).toBe(created.item.id);
      expect(replacement.item.identityKey).toBe(`local:${workspace}:parser-boundaries`);
      expect(replacement.item.status).toBe("queued");
    } finally {
      await acp.close();
      await daemon.close();
      if (priorDataDir === undefined) delete process.env.CMUX_LOCALREVIEW_DATA_DIR;
      else process.env.CMUX_LOCALREVIEW_DATA_DIR = priorDataDir;
    }
  });
});
