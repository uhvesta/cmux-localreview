#!/usr/bin/env bun
/** Captures the frozen daemon's successful managed-remote-PR lifecycle. */
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

type Fixture = { name: string; request: { method: string; path: string; body?: unknown }; response: { status: number; contentType: string | null; body: unknown } };
const root = mkdtempSync(join(tmpdir(), "cmux-localreview-remote-parity-"));
const output = join(import.meta.dir, "..", "testdata", "parity", "ts-final", "remote-pr.json");
const token = "remote-parity-capability";
process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "data");
process.env.CMUX_LOCALREVIEW_CACHE_DIR = join(root, "cache");

function git(cwd: string, args: string[]): string { return execFileSync("git", args, { cwd, encoding: "utf8", stdio: "pipe" }).trim(); }
function clean(value: unknown): unknown {
  if (typeof value === "string") return value
    .replaceAll(root, "<fixture-root>")
    .replace(/\/(?:private\/)?var\/folders\/[^/]+\/[^/]+\/T\/cmux-localreview-remote-parity-[^/]+/gi, "<fixture-root>")
    .replace(/\b[0-9a-f]{40}\b/gi, "<sha>")
    .replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi, "<uuid>")
    .replace(/\b[0-9a-f]{12}\b/gi, "<short-id>")
    .replace(/<sha>-[0-9a-f]+\.bundle/gi, "<bundle>");
  if (Array.isArray(value)) return value.map(clean);
  if (value && typeof value === "object") return Object.fromEntries(Object.entries(value as Record<string, unknown>)
    .filter(([key]) => !["createdAt", "updatedAt", "capturedAt", "openedAt", "expiresAt", "removedAt"].includes(key))
    .map(([key, entry]) => [key, ["sourceFingerprint", "bundleSha256"].includes(key) ? "<hash>" : clean(entry)]));
  return value;
}

const secrets = new Map<string, string>();
const fixtureFetch = (async (input: RequestInfo | URL) => {
  const url = String(input);
  if (url.endsWith("/login/device/code")) return new Response(JSON.stringify({ device_code: "remote-device", user_code: "REMOTE-123", verification_uri: "https://github.com/login/device", expires_in: 900, interval: 1 }));
  if (url.endsWith("/login/oauth/access_token")) return new Response(JSON.stringify({ access_token: "remote-read-token", expires_in: 3600 }));
  if (url.endsWith("/user")) return new Response(JSON.stringify({ login: "remote-fixture" }));
  return new Response(JSON.stringify({ message: "unexpected fixture request" }), { status: 404 });
}) as typeof fetch;

async function main(): Promise<void> {
  const source = join(root, "source"); const bare = join(root, "remote.git");
  mkdirSync(source, { recursive: true }); git(source, ["init", "-q"]);
  git(source, ["config", "user.email", "fixture@example.invalid"]); git(source, ["config", "user.name", "fixture"]);
  writeFileSync(join(source, "README.md"), "base\n"); git(source, ["add", "."]); git(source, ["commit", "-qm", "base"]);
  const baseSha = git(source, ["rev-parse", "HEAD"]);
  execFileSync("git", ["init", "--bare", "-q", bare]); git(source, ["remote", "add", "origin", bare]); git(source, ["push", "-q", "origin", "HEAD:refs/heads/main"]);
  writeFileSync(join(source, "README.md"), "remote PR head\n"); git(source, ["commit", "-am", "remote PR head", "-q"]);
  const headSha = git(source, ["rev-parse", "HEAD"]); git(source, ["push", "-q", "origin", "HEAD:refs/pull/42/head"]);

  // Dynamic imports matter: remotePr reads the cache directory during module
  // evaluation, so this process never touches a developer's normal cache.
  const [{ startGlobalDaemon }, { GitHubAuthService }, { setGitHubFetchForTests }] = await Promise.all([
    import("../src/global-daemon.ts"), import("../src/server/githubAuth.ts"), import("../src/server/remotePr.ts"),
  ]);
  const auth = new GitHubAuthService({
    get: async (service, account) => secrets.get(`${service}\0${account}`),
    set: async (service, account, value) => { secrets.set(`${service}\0${account}`, value); },
    remove: async (service, account) => { secrets.delete(`${service}\0${account}`); },
  }, fixtureFetch, async () => undefined, join(root, "github-apps.json"));
  setGitHubFetchForTests(async () => new Response(JSON.stringify({
    number: 42, html_url: "https://github.com/fixture/repository/pull/42", title: "Fixture remote PR", body: "", state: "OPEN", draft: false,
    head: { sha: headSha, ref: "fixture" }, base: { sha: baseSha, ref: "main", repo: { full_name: "fixture/repository", clone_url: bare } },
  })));
  const daemon = await startGlobalDaemon({ token, open: false, githubAuthService: auth });
  const base = `http://127.0.0.1:${daemon.discovery.port}`; const fixtures: Fixture[] = [];
  const request = async (name: string, path: string, init: RequestInit = {}) => {
    const response = await fetch(`${base}${path}`, { ...init, headers: { authorization: `Bearer ${token}`, ...(init.body ? { "content-type": "application/json" } : {}), ...(init.headers ?? {}) } });
    const text = await response.text(); let body: unknown = text; try { body = JSON.parse(text); } catch { /* text contract */ }
    fixtures.push({ name, request: { method: init.method ?? "GET", path: clean(path) as string, ...(init.body ? { body: clean(JSON.parse(String(init.body))) } : {}) }, response: { status: response.status, contentType: response.headers.get("content-type"), body: clean(body) } });
    return body as any;
  };
  try {
    await request("configure_read", "/api/github/auth/configure", { method: "POST", body: JSON.stringify({ capability: "read", clientId: "Iv1.remoteFixture" }) });
    await request("start_read", "/api/github/auth/read/start", { method: "POST", body: "{}" });
    await request("poll_read", "/api/github/auth/read/poll", { method: "POST", body: "{}" });
    const queued = await request("queue_remote_pr", "/api/queue", { method: "POST", body: JSON.stringify({ remoteUrl: "https://github.com/fixture/repository/pull/42", title: "Fixture remote PR" }) });
    const id = queued.item.id as string;
    await request("remote_status", `/api/queue/${id}/remote-status`);
    await request("open_remote", `/api/queue/${id}/open`, { method: "POST", body: "{}" });
    await request("cleanup_remote", `/api/queue/${id}/cleanup`, { method: "POST", body: JSON.stringify({ removeMirror: true }) });
  } finally {
    await daemon.close(); setGitHubFetchForTests(); rmSync(root, { recursive: true, force: true });
  }
  mkdirSync(join(output, ".."), { recursive: true });
  writeFileSync(output, `${JSON.stringify({ version: 1, source: "ts-final", generatedBy: "scripts/capture-remote-parity-fixtures.ts", fixtures }, null, 2)}\n`);
  console.log(`captured ${fixtures.length} remote PR fixtures at ${output}`);
}
await main();
