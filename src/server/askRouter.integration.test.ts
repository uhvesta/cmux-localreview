import { afterEach, describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import express from "express";
import type { Server } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { runMigrations } from "./db.ts";
import { AskService, createAskRouter, formatAskPrompt } from "./askRouter.ts";
import { createAskConversation } from "./askStore.ts";
import { getActiveSessionId, startNewSession } from "./commentsStore.ts";

const roots: string[] = [];
const servers: Server[] = [];

function temporaryWorkspace(): string {
  const root = mkdtempSync(join(tmpdir(), "cmux-localreview-ask-router-"));
  roots.push(root);
  return root;
}

async function listen(app: express.Express): Promise<string> {
  return new Promise((resolve) => {
    const server = app.listen(0, "127.0.0.1", () => {
      servers.push(server);
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("Expected a TCP listener");
      resolve(`http://127.0.0.1:${address.port}`);
    });
  });
}

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => new Promise<void>((resolve) => server.close(() => resolve()))));
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

describe("/ask HTTP integration with an injected Copilot boundary", () => {
  test("formats inline questions with workspace, file, range, side, and selected code", () => {
    const formatted = formatAskPrompt("/review/workspace", "Could this throw path be simplified?", {
      repoId: "repo-fixture",
      filePath: "src/parser.ts",
      side: "current",
      startLine: 20,
      endLine: 23,
      selectedCode: "if (!input) throw new Error('missing');",
    });
    expect(formatted).toContain("Workspace root: /review/workspace");
    expect(formatted).toContain("File (repository-relative): src/parser.ts");
    expect(formatted).toContain("File (workspace-relative): src/parser.ts");
    expect(formatted).toContain("File (absolute): /review/workspace/src/parser.ts");
    expect(formatted).toContain("Side: current");
    expect(formatted).toContain("Lines: L20-L23");
    expect(formatted).toContain("if (!input) throw new Error('missing');");
    expect(formatted).toEndWith("Could this throw path be simplified?");
  });

  test("resolves nested repositories before giving Copilot a file path", () => {
    const formatted = formatAskPrompt("/review/workspace", "Is this safe?", {
      repoId: "nested-repo",
      filePath: "src/nested.ts",
      side: "base",
      startLine: 3,
      selectedCode: "throw new Error('no');",
    }, new Map([["nested-repo", "/review/workspace/packages/nested"]]));
    expect(formatted).toContain("Repository root: /review/workspace/packages/nested");
    expect(formatted).toContain("File (workspace-relative): packages/nested/src/nested.ts");
    expect(formatted).toContain("File (absolute): /review/workspace/packages/nested/src/nested.ts");
    expect(formatted).toContain("Side: base");
  });

  test("persists question sets and one shared inline conversation without requiring a real Copilot login", async () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const app = express();
    const workspaceRoot = temporaryWorkspace();
    const { router, askService } = createAskRouter({
      db,
      workspaceRoot,
      repoRoots: new Map([["repo-fixture", workspaceRoot]]),
    });
    const sentPrompts: string[] = [];
    // Keep this at the public service boundary: routes, persistence, SSE, and
    // model validation are exercised exactly as production uses them while no
    // test needs a logged-in Copilot CLI subprocess.
    Object.assign(askService as unknown as Record<string, unknown>, {
      listModels: async () => [{ id: "fixture-model", name: "Fixture model" }],
      setModel: async () => undefined,
      abort: async () => true,
      send: async (_conversationId: string, prompt: string, onDelta: (delta: string) => void) => {
        sentPrompts.push(prompt);
        onDelta("fixture response");
        return { messageId: sentPrompts.length, content: "fixture response", aborted: false };
      },
    });
    app.use(router);
    const baseUrl = await listen(app);

    try {
      const models = await (await fetch(`${baseUrl}/api/ask/models`)).json() as { models: { id: string }[] };
      expect(models.models.map((model) => model.id)).toEqual(["fixture-model"]);

      const createdSet = await fetch(`${baseUrl}/api/ask/question-sets`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ name: "Review checks", questions: ["Is the error handled?", "Is the test meaningful?"] }),
      });
      expect(createdSet.status).toBe(201);
      const questionSet = (await createdSet.json() as { questionSet: { id: string; name: string; questions: { body: string; position: number }[] } }).questionSet;
      expect(questionSet.questions.map((question) => question.body)).toEqual(["Is the error handled?", "Is the test meaningful?"]);

      const updatedSet = await fetch(`${baseUrl}/api/ask/question-sets/${questionSet.id}`, {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ name: "Ordered review checks", questions: ["Second becomes first", "Then this one"] }),
      });
      expect(updatedSet.status).toBe(200);
      expect((await updatedSet.json() as { questionSet: { questions: { body: string }[] } }).questionSet.questions.map((question) => question.body))
        .toEqual(["Second becomes first", "Then this one"]);

      const inlineContext = {
        repoId: "repo-fixture",
        filePath: "src/parser.ts",
        side: "current",
        startLine: 20,
        endLine: 23,
        selectedCode: "if (!input) throw new Error('missing');",
      };
      const createdConversation = await fetch(`${baseUrl}/api/ask/inline-conversations`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ model: "fixture-model", context: inlineContext }),
      });
      expect(createdConversation.status).toBe(201);
      const conversation = (await createdConversation.json() as { conversation: { id: string; context: unknown }; reused: boolean; shared: boolean }).conversation;
      expect(conversation.context).toBeNull();

      const reusedConversation = await fetch(`${baseUrl}/api/ask/inline-conversations`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ model: "fixture-model", context: inlineContext }),
      });
      expect((await reusedConversation.json() as { conversation: { id: string }; reused: boolean; shared: boolean })).toMatchObject({
        conversation: { id: conversation.id }, reused: true, shared: true,
      });

      const otherLocation = { ...inlineContext, filePath: "src/other.ts", startLine: 3, endLine: 3, selectedCode: "return input;" };
      const otherInline = await fetch(`${baseUrl}/api/ask/inline-conversations`, {
        method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ context: otherLocation }),
      });
      expect((await otherInline.json() as { conversation: { id: string }; reused: boolean; shared: boolean })).toMatchObject({
        conversation: { id: conversation.id }, reused: true, shared: true,
      });

      // Reopening an inline location or restoring its side-chat transcript is
      // strictly a database read. It must never create a Copilot session or
      // replay the prior question merely because a reviewer refreshed a page.
      expect((await fetch(`${baseUrl}/api/ask/conversations/${conversation.id}`)).status).toBe(200);
      expect((await fetch(`${baseUrl}/api/ask/conversations/${conversation.id}`)).status).toBe(200);
      expect(sentPrompts).toEqual([]);

      const inlineReply = await fetch(`${baseUrl}/api/ask/conversations/${conversation.id}/messages`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ prompt: "Could this throw path be simplified?", location: inlineContext }),
      });
      const inlineSse = await inlineReply.text();
      expect(inlineSse).toContain("event: started");
      expect(inlineSse).toContain("fixture response");
      expect(inlineSse).toContain("event: done");

      const sequential = await fetch(`${baseUrl}/api/ask/question-sets/${questionSet.id}/send`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ conversationId: conversation.id, mode: "sequential" }),
      });
      expect(sequential.status).toBe(200);
      expect(await sequential.text()).toContain("event: question_done");
      expect(sentPrompts).toEqual([
        "Could this throw path be simplified?",
        "Second becomes first",
        "Then this one",
      ]);

      const transcript = await (await fetch(`${baseUrl}/api/ask/conversations/${conversation.id}`)).json() as {
        conversation: { context: unknown };
        messages: { role: string; body: string; location: unknown }[];
      };
      expect(transcript.conversation.context).toBeNull();
      expect(transcript.messages[0]).toMatchObject({ role: "user", body: "Could this throw path be simplified?", location: inlineContext });

      const settings = await fetch(`${baseUrl}/api/ask/conversations/${conversation.id}/settings`, {
        method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ model: "fixture-model", contextTier: "long_context" }),
      });
      expect(settings.status).toBe(200);
      expect((await settings.json() as { conversation: { model: string } }).conversation).toMatchObject({ model: "fixture-model" });

      // Formal review feedback lives in a completely separate table and this
      // flow must not create any queue feedback as a side effect.
      expect((db.query("SELECT COUNT(*) AS count FROM queue_feedback").get() as { count: number }).count).toBe(0);
    } finally {
      await askService.close().catch(() => undefined);
      db.close();
    }
  });

  test("keeps Ask history per review round and makes prior contexts read-only", async () => {
    const db = new Database(":memory:");
    runMigrations(db);
    let activeReviewSessionId = getActiveSessionId(db);
    const workspaceRoot = temporaryWorkspace();
    const app = express();
    const { router, askService } = createAskRouter({
      db,
      workspaceRoot,
      getReviewSessionId: () => activeReviewSessionId,
      repoRoots: new Map([["repo-fixture", workspaceRoot]]),
    });
    Object.assign(askService as unknown as Record<string, unknown>, {
      listModels: async () => [{ id: "fixture-model", name: "Fixture model" }],
      send: async () => ({ messageId: 1, content: "fixture", aborted: false }),
    });
    app.use(router);
    const baseUrl = await listen(app);
    const context = { repoId: "repo-fixture", filePath: "src/a.ts", side: "current", startLine: 4, endLine: 4, selectedCode: "return ok;" };
    try {
      const firstResponse = await fetch(`${baseUrl}/api/ask/inline-conversations`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ context }) });
      const first = (await firstResponse.json() as { conversation: { id: string; reviewSessionId: number } }).conversation;
      expect(first.reviewSessionId).toBe(activeReviewSessionId);

      const freshResponse = await fetch(`${baseUrl}/api/ask/conversations/fresh`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ model: "fixture-model" }) });
      const fresh = (await freshResponse.json() as { conversation: { id: string; reviewSessionId: number } }).conversation;
      expect(fresh.id).not.toBe(first.id);
      const current = await (await fetch(`${baseUrl}/api/ask/conversations`)).json() as { conversations: { id: string }[] };
      expect(current.conversations.map((conversation) => conversation.id)).toEqual([fresh.id]);
      const history = await (await fetch(`${baseUrl}/api/ask/conversations?history=1`)).json() as { conversations: { id: string; archivedAt: number | null }[] };
      expect(history.conversations.find((conversation) => conversation.id === first.id)?.archivedAt).toBeNumber();

      const historicalSend = await fetch(`${baseUrl}/api/ask/conversations/${first.id}/messages`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ prompt: "still there?", location: context }) });
      expect(historicalSend.status).toBe(409);
      const resumed = await fetch(`${baseUrl}/api/ask/conversations/${first.id}/resume`, { method: "POST" });
      expect(resumed.status).toBe(200);

      activeReviewSessionId = startNewSession(db, "next review");
      const newRound = await fetch(`${baseUrl}/api/ask/inline-conversations`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ context }) });
      const next = (await newRound.json() as { conversation: { id: string; reviewSessionId: number } }).conversation;
      expect(next).toMatchObject({ reviewSessionId: activeReviewSessionId });
      expect(next.id).not.toBe(first.id);
      expect((await (await fetch(`${baseUrl}/api/ask/conversations`)).json() as { conversations: { id: string }[] }).conversations.map((conversation) => conversation.id)).toEqual([next.id]);
      expect((await fetch(`${baseUrl}/api/ask/conversations/${first.id}/resume`, { method: "POST" })).status).toBe(409);
    } finally {
      await askService.close().catch(() => undefined);
      db.close();
    }
  });

  test("honors Stop when it arrives before a cold Copilot session is ready", async () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const conversation = createAskConversation(db, {});
    const service = new AskService(db, temporaryWorkspace());
    let resolveSession: ((value: unknown) => void) | undefined;
    Object.assign(service as unknown as Record<string, unknown>, {
      sessionFor: () => new Promise((resolve) => { resolveSession = resolve; }),
    });
    try {
      const pending = service.send(conversation.id, "Will be cancelled", () => undefined);
      await Promise.resolve();
      expect(await service.abort(conversation.id)).toBe(true);
      resolveSession!({ sending: false, session: {} });
      expect(await pending).toMatchObject({ content: "", aborted: true });
    } finally {
      await service.close().catch(() => undefined);
      db.close();
    }
  });
});
