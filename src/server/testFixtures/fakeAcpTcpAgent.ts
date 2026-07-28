import { createServer, type Server, type Socket } from "node:net";
import { Readable, Writable } from "node:stream";

import { AgentSideConnection, ndJsonStream, type Agent } from "@zed-industries/agent-client-protocol";

/** A small TCP ACP fixture: it proves feedback reaches an existing session. */
export async function startFakeAcpTcpAgent(sessionId = "fixture-live-session"): Promise<{ port: number; prompts: string[]; close(): Promise<void> }> {
  const prompts: string[] = [];
  const sockets = new Set<Socket>();
  const server: Server = createServer((socket) => {
    sockets.add(socket);
    socket.once("close", () => sockets.delete(socket));
    const stream = ndJsonStream(
      Writable.toWeb(socket) as WritableStream<Uint8Array>,
      Readable.toWeb(socket) as ReadableStream<Uint8Array>,
    );
    new AgentSideConnection((conn) => {
      const agent: Agent = {
        async initialize(params) { return { protocolVersion: params.protocolVersion, agentCapabilities: {} }; },
        async newSession() { return { sessionId }; },
        async loadSession() { return {}; },
        async authenticate() { return {}; },
        async prompt(params) {
          prompts.push(params.prompt.map((block) => block.type === "text" ? block.text : "").join(""));
          await conn.sessionUpdate({ sessionId: params.sessionId, update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: "Feedback received." } } });
          return { stopReason: "end_turn" };
        },
        async cancel() {},
      };
      return agent;
    }, stream);
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => { server.off("error", reject); resolve(); });
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("Fake ACP TCP server did not bind a TCP port");
  return {
    port: address.port,
    prompts,
    close: () => new Promise((resolve, reject) => {
      for (const socket of sockets) socket.destroy();
      server.close((error) => error ? reject(error) : resolve());
    }),
  };
}
