import { afterEach, describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import express from "express";
import type { Server } from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { runMigrations } from "./db.ts";
import { createAskRouter } from "./askRouter.ts";

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
  test("persists question sets and inline conversations without requiring a real Copilot login", async () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const app = express();
    const { router, askService } = createAskRouter({ db, workspaceRoot: temporaryWorkspace() });
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
      const conversation = (await createdConversation.json() as { conversation: { id: string; context: unknown }; reused: boolean }).conversation;
      expect(conversation.context).toEqual(inlineContext);

      const reusedConversation = await fetch(`${baseUrl}/api/ask/inline-conversations`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ model: "fixture-model", context: inlineContext }),
      });
      expect((await reusedConversation.json() as { conversation: { id: string }; reused: boolean })).toMatchObject({
        conversation: { id: conversation.id }, reused: true,
      });

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
      expect(transcript.conversation.context).toEqual(inlineContext);
      expect(transcript.messages[0]).toMatchObject({ role: "user", body: "Could this throw path be simplified?", location: inlineContext });

      // Formal review feedback lives in a completely separate table and this
      // flow must not create any queue feedback as a side effect.
      expect((db.query("SELECT COUNT(*) AS count FROM queue_feedback").get() as { count: number }).count).toBe(0);
    } finally {
      await askService.close().catch(() => undefined);
      db.close();
    }
  });
});
