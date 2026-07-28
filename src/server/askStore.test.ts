import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";

import { runMigrations } from "./db.ts";
import { createAskConversation, insertAskMessage, listAskMessages, updateAskConversation } from "./askStore.ts";

describe("inline /ask persistence", () => {
  test("retains an exact diff range, selected code, and transcript without entering review tables", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const location = {
      repoId: "repo-1", filePath: "src/example.ts", side: "current" as const,
      startLine: 12, endLine: 15, selectedCode: "one\ntwo\nthree\nfour",
    };
    const conversation = createAskConversation(db, { context: location });
    insertAskMessage(db, { conversationId: conversation.id, role: "user", body: "Is this safe?", location });
    insertAskMessage(db, { conversationId: conversation.id, role: "assistant", body: "It needs a bounds check.", location });

    expect(conversation.context).toEqual(location);
    expect(listAskMessages(db, conversation.id).map((message) => ({ role: message.role, body: message.body, location: message.location }))).toEqual([
      { role: "user", body: "Is this safe?", location },
      { role: "assistant", body: "It needs a bounds check.", location },
    ]);
    expect(db.query("SELECT count(*) AS count FROM queue_feedback").get()).toEqual({ count: 0 });
    expect(db.query("SELECT count(*) AS count FROM comments").get()).toEqual({ count: 0 });
  });
});

describe("shared /ask settings", () => {
  test("persists model, thinking level, and context tier with a conversation", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    const conversation = createAskConversation(db, { model: "gpt-5", reasoningEffort: "high", contextTier: "long_context" });
    expect(conversation).toMatchObject({ model: "gpt-5", reasoningEffort: "high", contextTier: "long_context" });
    expect(updateAskConversation(db, conversation.id, { model: "gpt-5-mini", reasoningEffort: "medium", contextTier: "default" }))
      .toMatchObject({ model: "gpt-5-mini", reasoningEffort: "medium", contextTier: "default" });
  });
});
