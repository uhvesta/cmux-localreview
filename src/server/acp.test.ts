import { describe, expect, test } from "bun:test";
import { fileURLToPath } from "node:url";
import type { SessionNotification } from "@zed-industries/agent-client-protocol";

import { AcpSession, AcpStartupTimeoutError } from "./acp.ts";

const FAKE_AGENT = fileURLToPath(new URL("./testFixtures/fakeAcpAgent.ts", import.meta.url));

function textOf(notification: SessionNotification): string | null {
  const update = notification.update;
  if (
    "content" in update &&
    update.content &&
    !Array.isArray(update.content) &&
    update.content.type === "text"
  ) {
    return update.content.text;
  }
  return null;
}

describe("AcpSession", () => {
  test("initializes, creates a session, and streams session/update chunks for a prompt", async () => {
    const updates: SessionNotification[] = [];
    const session = await AcpSession.spawn({
      command: "bun",
      args: [FAKE_AGENT, "normal"],
      cwd: process.cwd(),
      onUpdate: (n) => updates.push(n),
    });

    try {
      const response = await session.prompt("hello?");
      expect(response.stopReason).toBe("end_turn");
      const texts = updates.map(textOf);
      expect(texts).toEqual(["Looking at the file...", " Done."]);
    } finally {
      session.dispose();
    }
  });

  test("serializes overlapping prompts — a second prompt waits for the first to finish", async () => {
    const order: string[] = [];
    const session = await AcpSession.spawn({
      command: "bun",
      args: [FAKE_AGENT, "normal"],
      cwd: process.cwd(),
      onUpdate: () => {},
    });

    try {
      const p1 = session.prompt("first").then(() => order.push("first"));
      const p2 = session.prompt("second").then(() => order.push("second"));
      await Promise.all([p1, p2]);
      expect(order).toEqual(["first", "second"]);
    } finally {
      session.dispose();
    }
  });

  test("read-only permission policy denies a write/edit tool call", async () => {
    const updates: SessionNotification[] = [];
    const session = await AcpSession.spawn({
      command: "bun",
      args: [FAKE_AGENT, "write-permission"],
      cwd: process.cwd(),
      onUpdate: (n) => updates.push(n),
    });

    try {
      await session.prompt("please write a file");
      const outcomeText = updates.map(textOf).find((t) => t?.startsWith("permission outcome:"));
      expect(outcomeText).toBeDefined();
      expect(outcomeText).toContain('"optionId":"deny"');
      expect(outcomeText).not.toContain('"optionId":"allow"');
    } finally {
      session.dispose();
    }
  });

  test("times out and kills the process if the agent never answers initialize", async () => {
    await expect(
      AcpSession.spawn({
        command: "bun",
        args: [FAKE_AGENT, "hang"],
        cwd: process.cwd(),
        onUpdate: () => {},
        startupTimeoutMs: 300,
      }),
    ).rejects.toBeInstanceOf(AcpStartupTimeoutError);
  });
});
