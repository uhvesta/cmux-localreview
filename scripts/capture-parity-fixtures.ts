#!/usr/bin/env bun
/**
 * Captures the frozen TypeScript daemon's public HTTP contract against a
 * deterministic, multi-repository workspace.  The resulting corpus is the
 * Phase 0 safety net for the Go cutover: Go tests consume the same request
 * descriptions and compare stable response shape/status before a route is
 * considered ported.
 *
 * Run with: bun scripts/capture-parity-fixtures.ts [--output /tmp/http.json]
 */
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, relative } from "node:path";

import { startGlobalDaemon } from "../src/global-daemon.ts";
import { AskService } from "../src/server/askRouter.ts";
import { GitHubAuthService } from "../src/server/githubAuth.ts";
import type { SecretStore } from "../src/server/secretStore.ts";

type Fixture = {
  name: string;
  request: { method: string; path: string; body?: unknown; authenticated?: boolean };
  response: { status: number; contentType: string | null; body: unknown };
};

function outputPath(defaultPath: string): string {
  const index = process.argv.indexOf("--output");
  if (index < 0) return defaultPath;
  const value = process.argv[index + 1];
  if (!value || process.argv[index + 2] === "--output") {
    throw new Error("--output requires a file path");
  }
  return value;
}

const output = outputPath(join(import.meta.dir, "..", "testdata", "parity", "ts-final", "http.json"));
const root = mkdtempSync(join(tmpdir(), "cmux-localreview-parity-"));
const workspace = join(root, "workspace");
const token = "parity-fixture-capability";

function git(cwd: string, args: string[]): void { execFileSync("git", args, { cwd, stdio: "pipe" }); }
function makeRepo(path: string, filename: string, content: string): void {
  mkdirSync(path, { recursive: true });
  git(path, ["init", "-q"]);
  git(path, ["config", "user.email", "fixture@example.invalid"]);
  git(path, ["config", "user.name", "fixture"]);
  writeFileSync(join(path, filename), content);
  git(path, ["add", "."]);
  git(path, ["commit", "-qm", "fixture"]);
}

function scrub(value: unknown): unknown {
  if (typeof value === "string") {
    return value
      .replaceAll(root, "<fixture-root>")
      .replaceAll(workspace, "<workspace>")
      .replace(/\/(?:private\/)?var\/folders\/[^/]+\/[^/]+\/T\/cmux-localreview-parity-[^/]+/gi, "<fixture-root>")
      // Queue IDs, conversation IDs, snapshot IDs, and repo public IDs are
      // intentionally random. Their shape is contractual; their value is not.
      .replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi, "<uuid>")
      .replace(/\b[0-9a-f]{40}\b/gi, "<sha>")
      .replace(/\b[0-9a-f]{12}\b/gi, "<short-id>")
      .replace(/\b[0-9a-f]{7}\b/gi, "<short-sha>")
      .replace(/workspace-[0-9a-f]+\.bundle/gi, "workspace-<bundle>.bundle");
  }
  if (Array.isArray(value)) return value.map(scrub);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>)
      .filter(([key]) => !["pid", "createdAt", "updatedAt", "startedAt", "openedAt", "lastHeartbeatAt", "lastConnectedAt", "lastSeenAt", "expiresAt", "archivedAt", "capturedAt", "detectedAt", "removedAt"].includes(key))
      .map(([key, item]) => [["repositoryId", "sourceFingerprint", "bundleSha256"].includes(key) ? key : key, ["repositoryId", "sourceFingerprint", "bundleSha256"].includes(key) ? "<hash>" : scrub(item)]));
  }
  return value;
}

function memorySecretStore(): SecretStore {
  const values = new Map<string, string>();
  const key = (service: string, account: string) => `${service}\0${account}`;
  return {
    get: async (service, account) => values.get(key(service, account)),
    set: async (service, account, value) => { values.set(key(service, account), value); },
    remove: async (service, account) => { values.delete(key(service, account)); },
  };
}

/** Hermetic GitHub device-flow fixture; no token leaves this process. */
const githubFixtureFetch = (async (input: RequestInfo | URL) => {
  const url = String(input);
  if (url.endsWith("/login/device/code")) {
    return new Response(JSON.stringify({ device_code: "fixture-device", user_code: "ABCD-1234", verification_uri: "https://github.com/login/device", expires_in: 900, interval: 1 }), { status: 200 });
  }
  if (url.endsWith("/login/oauth/access_token")) {
    return new Response(JSON.stringify({ access_token: "fixture-app-token", expires_in: 3600 }), { status: 200 });
  }
  if (url.endsWith("/user")) return new Response(JSON.stringify({ login: "fixture-reviewer" }), { status: 200 });
  return new Response(JSON.stringify({ message: "unexpected fixture URL" }), { status: 404 });
}) as typeof fetch;

// The capture must include the actual router's SSE framing and persistence,
// but it must never start Copilot CLI or consult a developer's credentials.
// Patch only this disposable Bun process with the narrow SDK-shaped fake.
const fixtureModels = [{
  id: "fixture-model", name: "Fixture Model",
  capabilities: { supports: { reasoningEffort: true } },
  supportedReasoningEfforts: ["low", "medium", "high", "xhigh"],
}];
(AskService.prototype as any).listModels = async () => fixtureModels;
(AskService.prototype as any).sessionFor = async () => {
  const listeners = new Map<string, Set<(event: any) => void>>();
  const emit = (event: string, data: unknown) => listeners.get(event)?.forEach((listener) => listener({ data }));
  const session = {
    on: (event: string, listener: (event: any) => void) => {
      const set = listeners.get(event) ?? new Set(); set.add(listener); listeners.set(event, set);
      return () => set.delete(listener);
    },
    sendAndWait: async () => {
      emit("assistant.message_delta", { deltaContent: "Fixture " });
      emit("assistant.message_delta", { deltaContent: "Copilot answer." });
      emit("assistant.message", { content: "Fixture Copilot answer." });
      emit("session.idle", { aborted: false });
      return { data: { content: "Fixture Copilot answer." } };
    },
    abort: async () => undefined,
    setModel: async () => undefined,
  };
  return { session, model: "fixture-model", sending: false };
};

async function main(): Promise<void> {
  makeRepo(workspace, "root.ts", "export const root = 1;\n");
  makeRepo(join(workspace, "nested"), "nested.ts", "export const nested = 2;\n");
  writeFileSync(join(workspace, "root.ts"), "export const root = 3;\n");
  writeFileSync(join(workspace, "untracked.md"), "fixture\n");
  process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "daemon-data");
  const githubAuth = new GitHubAuthService(
    memorySecretStore(), githubFixtureFetch, async () => undefined, join(root, "github-apps.json"),
  );
  const daemon = await startGlobalDaemon({ token, open: false, githubAuthService: githubAuth });
  const base = `http://127.0.0.1:${daemon.discovery.port}`;
  const fixtures: Fixture[] = [];
  const request = async (name: string, path: string, init: RequestInit = {}, authenticated = true) => {
    const response = await fetch(`${base}${path}`, {
      ...init,
      headers: {
        ...(authenticated ? { authorization: `Bearer ${token}` } : {}),
        ...(init.body ? { "content-type": "application/json" } : {}),
        ...(init.headers ?? {}),
      },
    });
    const text = await response.text();
    let body: unknown = text;
    try { body = JSON.parse(text); } catch { /* plain-text contract */ }
    fixtures.push({
      name,
      request: { method: init.method ?? "GET", path: scrub(path) as string, ...(init.body ? { body: scrub(JSON.parse(String(init.body))) } : {}), authenticated },
      response: { status: response.status, contentType: response.headers.get("content-type"), body: scrub(body) },
    });
    return body as Record<string, unknown>;
  };
  const websocketDiffUpdate = async () => {
    const socket = new WebSocket(`ws://127.0.0.1:${daemon.discovery.port}/ws`);
    const message = await new Promise<unknown>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("timed out waiting for diff-updated websocket frame")), 4_000);
      socket.onopen = () => writeFileSync(join(workspace, "root.ts"), "export const root = 4;\n");
      socket.onmessage = (event) => { clearTimeout(timeout); resolve(JSON.parse(String(event.data))); socket.close(); };
      socket.onerror = () => { clearTimeout(timeout); reject(new Error("websocket connection failed")); };
    });
    fixtures.push({ name: "websocket_diff_updated", request: { method: "WEBSOCKET", path: "/ws", authenticated: false }, response: { status: 101, contentType: null, body: scrub(message) } });
  };

  try {
    await request("health", "/health", {}, false);
    await request("unauthenticated_queue", "/api/queue", {}, false);
    await request("browser_session_exchange", "/api/browser/session", { method: "POST", body: JSON.stringify({}) });
    await request("github_auth_status", "/api/github/auth/status");
    await request("github_auth_configure", "/api/github/auth/configure", {
      method: "POST", body: JSON.stringify({ capability: "read", clientId: "Iv1.fixtureRead" }),
    });
    await request("github_auth_device_start", "/api/github/auth/read/start", { method: "POST", body: JSON.stringify({}) });
    await request("github_auth_device_poll", "/api/github/auth/read/poll", { method: "POST", body: JSON.stringify({}) });
    await request("github_auth_authenticated_status", "/api/github/auth/status");
    await request("github_auth_disconnect", "/api/github/auth/read", { method: "DELETE" });
    await request("local_pr_requires_read_auth", "/api/local-review/pr", {
      method: "POST", body: JSON.stringify({ remoteUrl: "https://github.com/fixture/repository/pull/1" }),
    });
    await request("workspaces_empty", "/api/workspaces");
    await request("queue_empty", "/api/queue");
    await request("federation_nodes_empty", "/api/federation/nodes");
    await request("open_workspace", "/api/workspaces/open", { method: "POST", body: JSON.stringify({ workspacePath: workspace }) });
    const repos = await request("repos", "/api/repos", {}, false) as { repos: { id: string }[] };
    const repo = repos.repos[0]!;
    await request("repo_diff", `/api/repos/${repo.id}/api/diff`, {}, false);
    await request("repo_diff_ignore_whitespace", `/api/repos/${repo.id}/api/diff?ignoreWhitespace=true`, {}, false);
    await request("repo_revisions", `/api/repos/${repo.id}/api/revisions`, {}, false);
    await request("repo_line_count", `/api/repos/${repo.id}/api/line-count/root.ts?oldRef=HEAD&newRef=HEAD`, {}, false);
    await request("repo_blob", `/api/repos/${repo.id}/api/blob/root.ts?ref=HEAD`, {}, false);
    await request("repo_generated_status", `/api/repos/${repo.id}/api/generated-status/root.ts`, {}, false);
    await request("repo_fullfile", `/api/repos/${repo.id}/api/fullfile/root.ts?side=current`, {}, false);
    await request("repo_comments_empty", `/api/repos/${repo.id}/api/comments`, {}, false);
    await request("create_comment", `/api/repos/${repo.id}/api/comments`, {
      method: "POST", body: JSON.stringify({ comments: [{ id: "fixture-comment", file: "root.ts", line: 1, body: "Fixture comment" }] }),
    }, false);
    await request("repo_comments_saved", `/api/repos/${repo.id}/api/comments`, {}, false);
    await request("comment_import", `/api/repos/${repo.id}/api/comment-imports`, {
      method: "POST", body: JSON.stringify([{ id: "fixture-import", file: "root.ts", line: 1, body: "Imported fixture comment" }]),
    }, false);
    await request("sessions", "/api/sessions", {}, false);
    await request("review_history", "/api/review-history/comments", {}, false);
    await request("btw_threads_empty", "/api/btw/threads", {}, false);
    await request("btw_ask_validation", "/api/btw/ask", { method: "POST", body: JSON.stringify({}) }, false);
    await websocketDiffUpdate();
    await request("ui_state_empty", "/api/ui-state?key=fixture", {}, false);
    await request("ui_state_put", "/api/ui-state", { method: "PUT", body: JSON.stringify({ key: "fixture", revision: 0, value: { selectedRepo: repo.id } }) }, false);
    await request("export_prompt", "/api/export/prompt", {}, false);
    await request("new_session", "/api/sessions/new", { method: "POST", body: JSON.stringify({ label: "fixture round" }) }, false);
    await request("comments_json", `/api/repos/${repo.id}/api/comments-json`, {}, false);
    await request("comments_output", `/api/repos/${repo.id}/api/comments-output`, {}, false);
    await request("ask_models", "/api/ask/models", {}, false);
    await request("ask_conversations_empty", "/api/ask/conversations", {}, false);
    const questionSet = await request("ask_question_set_create", "/api/ask/question-sets", {
      method: "POST", body: JSON.stringify({ name: "fixture questions", questions: ["What changed?", "What should be tested?"] }),
    }, false) as { questionSet: { id: string } };
    await request("ask_question_sets", "/api/ask/question-sets", {}, false);
    await request("ask_question_set_get", `/api/ask/question-sets/${questionSet.questionSet.id}`, {}, false);
    await request("ask_question_set_update", `/api/ask/question-sets/${questionSet.questionSet.id}`, {
      method: "PUT", body: JSON.stringify({ name: "fixture questions", questions: ["What changed?", "What risks remain?"] }),
    }, false);
    await request("ask_question_set_delete", `/api/ask/question-sets/${questionSet.questionSet.id}`, { method: "DELETE" }, false);
    const conversation = await request("ask_conversation_create", "/api/ask/conversations", {
      method: "POST", body: JSON.stringify({ model: "fixture-model", reasoningEffort: "high", contextTier: "long_context" }),
    }, false) as { conversation: { id: string } };
    const conversationId = conversation.conversation.id;
    await request("ask_conversation_get", `/api/ask/conversations/${conversationId}`, {}, false);
    await request("ask_inline_conversation_reuses_context", "/api/ask/inline-conversations", {
      method: "POST", body: JSON.stringify({ context: { repoId: repo.id, filePath: "root.ts", side: "current", startLine: 1, endLine: 1, selectedCode: "export const root = 3;" } }),
    }, false);
    await request("ask_conversation_model", `/api/ask/conversations/${conversationId}/model`, {
      method: "POST", body: JSON.stringify({ model: "fixture-model" }),
    }, false);
    await request("ask_conversation_settings", `/api/ask/conversations/${conversationId}/settings`, {
      method: "POST", body: JSON.stringify({ model: "fixture-model", reasoningEffort: "medium", contextTier: "long_context" }),
    }, false);
    await request("ask_conversation_message_sse", `/api/ask/conversations/${conversationId}/messages`, {
      method: "POST", body: JSON.stringify({ prompt: "Explain this line", location: { repoId: repo.id, filePath: "root.ts", side: "current", startLine: 1, selectedCode: "export const root = 3;" } }),
    }, false);
    await request("ask_conversation_cancel_idle", `/api/ask/conversations/${conversationId}/cancel`, { method: "POST", body: JSON.stringify({}) }, false);
    const sendingQuestionSet = await request("ask_question_set_for_send", "/api/ask/question-sets", {
      method: "POST", body: JSON.stringify({ name: "send fixture", questions: ["What changed?", "What should be tested?"] }),
    }, false) as { questionSet: { id: string } };
    await request("ask_question_set_combined_sse", `/api/ask/question-sets/${sendingQuestionSet.questionSet.id}/send`, {
      method: "POST", body: JSON.stringify({ conversationId, mode: "combined" }),
    }, false);
    await request("ask_question_set_sequential_sse", `/api/ask/question-sets/${sendingQuestionSet.questionSet.id}/send`, {
      method: "POST", body: JSON.stringify({ conversationId, mode: "sequential" }),
    }, false);
    // Conversation history is ordered by millisecond timestamps. Ensure the
    // archived conversation and fresh conversation never share a timestamp,
    // which would make the frozen fixture ordering nondeterministic.
    await new Promise((resolve) => setTimeout(resolve, 5));
    await request("ask_conversation_fresh", "/api/ask/conversations/fresh", {
      method: "POST", body: JSON.stringify({ model: "fixture-model", reasoningEffort: "low", contextTier: "default" }),
    }, false);
    await request("ask_conversation_history", "/api/ask/conversations?history=true", {}, false);

    const queued = await request("queue_create_local", "/api/queue", {
      method: "POST", body: JSON.stringify({ workspacePath: workspace, title: "parity local review", topic: "fixture-topic" }),
    }) as { item: { id: string } };
    const item = queued.item.id;
    await request("queue_list_with_item", "/api/queue");
    await request("queue_detail", `/api/queue/${item}`);
    await request("queue_reorder", `/api/queue/${item}/reorder`, { method: "POST", body: JSON.stringify({ position: 0 }) });
    await request("queue_add_feedback", `/api/queue/${item}/feedback`, { method: "POST", body: JSON.stringify({ body: "Fixture feedback", path: "root.ts", line: 1 }) });
    await request("queue_feedback_prompt", `/api/queue/${item}/feedback/prompt`);
    await request("queue_reproduce", `/api/queue/${item}/reproduce`);
    await request("queue_export", `/api/queue/${item}/export`, { method: "POST", body: JSON.stringify({ destination: join(root, "review-package") }) });
    await request("queue_open", `/api/queue/${item}/open`, { method: "POST" });
    await request("queue_complete", `/api/queue/${item}/decision`, { method: "POST", body: JSON.stringify({ decision: "completed", body: "Fixture done" }) });
    await request("queue_requeue", `/api/queue/${item}/requeue`, { method: "POST" });
    await request("queue_delete", `/api/queue/${item}`, { method: "DELETE", body: JSON.stringify({ reason: "fixture cleanup" }) });
    await request("queue_history", "/api/queue?history=true&removed=true");
    await request("queue_watch_enable", "/api/queue/watch", { method: "POST", body: JSON.stringify({ workspacePath: workspace, pollIntervalMs: 1000 }) });
    await request("queue_watch_disable", "/api/queue/watch", { method: "POST", body: JSON.stringify({ workspacePath: workspace, enabled: false }) });
    // `queue/watch` performs one asynchronous initial fingerprint check. Let
    // that frozen-server behavior settle before removing the disposable
    // workspace, otherwise its background error pollutes capture output.
    await new Promise((resolve) => setTimeout(resolve, 1_500));
    await request("queue_hook", "/api/queue/hook", { method: "POST", body: JSON.stringify({ workspacePath: workspace }) });
    await request("agent_register", "/api/agents", { method: "POST", body: JSON.stringify({ id: "fixture-agent", provider: "copilot", workspacePath: workspace, surfaceId: "fixture-surface" }) });
    await request("agent_list", "/api/agents");
    await request("agent_heartbeat", "/api/agents/fixture-agent/heartbeat", { method: "POST", body: JSON.stringify({ surfaceId: "fixture-surface" }) });
    await request("agent_reconnect", "/api/agents/fixture-agent/reconnect", { method: "POST", body: JSON.stringify({ dryRun: true }) });
    await request("federation_node_create", "/api/federation/nodes", { method: "POST", body: JSON.stringify({ id: "fixture-node", label: "fixture remote", sshTarget: "fixture@localhost", remotePort: 57140, token: "fixture-node-token" }) });
    await request("federation_node_status", "/api/federation/nodes/fixture-node/status");
    await request("federation_node_disconnect", "/api/federation/nodes/fixture-node/disconnect", { method: "POST" });
    await request("federation_aggregate_queue", "/api/federation/queue");
    await request("federation_node_delete", "/api/federation/nodes/fixture-node", { method: "DELETE" });
  } finally {
    await daemon.close();
    rmSync(root, { recursive: true, force: true });
  }

  mkdirSync(join(output, ".."), { recursive: true });
  writeFileSync(output, `${JSON.stringify({
    version: 1,
    source: "ts-final",
    fixtureWorkspace: relative(root, workspace),
    generatedBy: "scripts/capture-parity-fixtures.ts",
    fixtures,
  }, null, 2)}\n`);
  console.log(`captured ${fixtures.length} TS parity fixtures at ${output}`);
}

await main();
