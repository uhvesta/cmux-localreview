#!/usr/bin/env bun
/**
 * Captures the frozen TypeScript daemon's public HTTP contract against a
 * deterministic, multi-repository workspace.  The resulting corpus is the
 * Phase 0 safety net for the Go cutover: Go tests consume the same request
 * descriptions and compare stable response shape/status before a route is
 * considered ported.
 *
 * Run with: bun scripts/capture-parity-fixtures.ts
 */
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, relative } from "node:path";

import { startGlobalDaemon } from "../src/global-daemon.ts";

type Fixture = {
  name: string;
  request: { method: string; path: string; body?: unknown; authenticated?: boolean };
  response: { status: number; contentType: string | null; body: unknown };
};

const output = join(import.meta.dir, "..", "testdata", "parity", "ts-final", "http.json");
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
    return value.replaceAll(root, "<fixture-root>").replaceAll(workspace, "<workspace>");
  }
  if (Array.isArray(value)) return value.map(scrub);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>)
      .filter(([key]) => !["pid", "createdAt", "updatedAt", "startedAt", "openedAt", "lastHeartbeatAt", "lastConnectedAt"].includes(key))
      .map(([key, item]) => [key, scrub(item)]));
  }
  return value;
}

async function main(): Promise<void> {
  makeRepo(workspace, "root.ts", "export const root = 1;\n");
  makeRepo(join(workspace, "nested"), "nested.ts", "export const nested = 2;\n");
  writeFileSync(join(workspace, "root.ts"), "export const root = 3;\n");
  writeFileSync(join(workspace, "untracked.md"), "fixture\n");
  process.env.CMUX_LOCALREVIEW_DATA_DIR = join(root, "daemon-data");
  const daemon = await startGlobalDaemon({ token, open: false });
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
      request: { method: init.method ?? "GET", path, ...(init.body ? { body: JSON.parse(String(init.body)) } : {}), authenticated },
      response: { status: response.status, contentType: response.headers.get("content-type"), body: scrub(body) },
    });
    return body as Record<string, unknown>;
  };

  try {
    await request("health", "/health", {}, false);
    await request("unauthenticated_queue", "/api/queue", {}, false);
    await request("github_auth_status", "/api/github/auth/status");
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
    await request("ui_state_empty", "/api/ui-state?key=fixture", {}, false);
    await request("ui_state_put", "/api/ui-state", { method: "PUT", body: JSON.stringify({ key: "fixture", revision: 0, value: { selectedRepo: repo.id } }) }, false);
    await request("export_prompt", "/api/export/prompt", {}, false);
    await request("new_session", "/api/sessions/new", { method: "POST", body: JSON.stringify({ label: "fixture round" }) }, false);
    await request("comments_json", `/api/repos/${repo.id}/api/comments-json`, {}, false);
    await request("comments_output", `/api/repos/${repo.id}/api/comments-output`, {}, false);
    await request("ask_models_offline", "/api/ask/models", {}, false);
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
