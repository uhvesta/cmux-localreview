#!/usr/bin/env bun
// Scripted fake ACP agent for testing AcpSession without a real provider
// (HANDOFF.md: "a ~50-line bun script speaking NDJSON on stdio"). Variants
// selected via argv[2]:
//   normal            — answers everything, streams two message chunks.
//   write-permission  — also issues a request_permission for a write/edit
//                        tool call, then echoes back the outcome so the
//                        test can assert it was denied.
//   hang              — never responds to initialize, to test the client's
//                        startup timeout.
import { Readable, Writable } from "node:stream";
import { AgentSideConnection, ndJsonStream, type Agent } from "@zed-industries/agent-client-protocol";

const variant = process.argv[2] ?? "normal";

const stream = ndJsonStream(
  Writable.toWeb(process.stdout) as WritableStream<Uint8Array>,
  Readable.toWeb(process.stdin) as ReadableStream<Uint8Array>,
);

new AgentSideConnection((conn) => {
  const agent: Agent = {
    async initialize(params) {
      if (variant === "hang") {
        return new Promise(() => {
          // never resolves
        });
      }
      return { protocolVersion: params.protocolVersion, agentCapabilities: {} };
    },
    async newSession() {
      return { sessionId: "fake-session-1" };
    },
    async loadSession() {
      throw new Error("session/load not supported by fake agent");
    },
    async authenticate() {
      return {};
    },
    async prompt(params) {
      const sessionId = params.sessionId;
      await conn.sessionUpdate({
        sessionId,
        update: {
          sessionUpdate: "agent_message_chunk",
          content: { type: "text", text: "Looking at the file..." },
        },
      });

      if (variant === "write-permission") {
        const result = await conn.requestPermission({
          sessionId,
          toolCall: { toolCallId: "tc-1", title: "Write file", kind: "edit" },
          options: [
            { optionId: "allow", name: "Allow", kind: "allow_once" },
            { optionId: "deny", name: "Deny", kind: "reject_once" },
          ],
        });
        await conn.sessionUpdate({
          sessionId,
          update: {
            sessionUpdate: "agent_message_chunk",
            content: { type: "text", text: `permission outcome: ${JSON.stringify(result.outcome)}` },
          },
        });
      }

      await conn.sessionUpdate({
        sessionId,
        update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: " Done." } },
      });

      return { stopReason: "end_turn" };
    },
    async cancel() {},
  };
  return agent;
}, stream);
