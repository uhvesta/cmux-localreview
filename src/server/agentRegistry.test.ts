import { describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";

import { runMigrations } from "./db.ts";
import {
  AgentRoutingError,
  getRegisteredAgent,
  markAgentDeliveryFailed,
  registerAgent,
  resolveTerminalTarget,
} from "./agentRegistry.ts";

function makeDb(): Database {
  const db = new Database(":memory:");
  runMigrations(db);
  return db;
}

describe("agent registry terminal routing", () => {
  test("requires an explicitly registered, workspace-bound surface target", () => {
    const db = makeDb();
    expect(() => resolveTerminalTarget(db, undefined, "/work/a")).toThrow(AgentRoutingError);

    registerAgent(db, {
      id: "agent-a",
      provider: "acp",
      workspacePath: "/work/a",
      surfaceId: "surface-a",
    });
    expect(resolveTerminalTarget(db, "agent-a", "/work/a").metadata.surfaceId).toBe("surface-a");
    expect(() => resolveTerminalTarget(db, "agent-a", "/work/b")).toThrow("belongs to /work/a");
  });

  test("persists a failed delivery as disconnected rather than falling back", () => {
    const db = makeDb();
    registerAgent(db, { id: "agent-a", provider: "acp", surfaceId: "surface-a" });
    markAgentDeliveryFailed(db, "agent-a", new Error("cmux socket disappeared"));
    const agent = getRegisteredAgent(db, "agent-a")!;
    expect(agent.status).toBe("disconnected");
    expect(agent.metadata.lastError).toContain("cmux socket disappeared");
    expect(() => resolveTerminalTarget(db, "agent-a", "/work/a")).toThrow("disconnected");
  });
});
