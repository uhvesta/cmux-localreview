import { describe, expect, test, afterEach } from "bun:test";
import type { Server } from "node:http";
import { createServer } from "node:http";
import { WebSocket } from "ws";

import { WsHub } from "./wsHub.ts";

const serversToClose: Server[] = [];

afterEach(() => {
  while (serversToClose.length) serversToClose.pop()!.close();
});

async function listen(): Promise<{ server: Server; port: number }> {
  return new Promise((resolve) => {
    const server = createServer();
    server.listen(0, "127.0.0.1", () => {
      serversToClose.push(server);
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("expected AddressInfo");
      resolve({ server, port: address.port });
    });
  });
}

function waitForOpen(ws: WebSocket): Promise<void> {
  return new Promise((resolve, reject) => {
    ws.once("open", () => resolve());
    ws.once("error", reject);
  });
}

describe("WsHub", () => {
  test("broadcasts to connected clients", async () => {
    const { server, port } = await listen();
    const hub = new WsHub(server);
    const client = new WebSocket(`ws://127.0.0.1:${port}/ws`);
    await waitForOpen(client);

    const messages: unknown[] = [];
    client.on("message", (data) => messages.push(JSON.parse(data.toString())));

    hub.broadcast({ type: "diff-updated", repoId: "abc" });
    await new Promise((r) => setTimeout(r, 50));

    expect(messages).toEqual([{ type: "diff-updated", repoId: "abc" }]);
    client.close();
    hub.close();
  });

  test("fires onLastClientDisconnected only once client count reaches zero", async () => {
    const { server, port } = await listen();
    let fired = 0;
    const hub = new WsHub(server, "/ws", { onLastClientDisconnected: () => fired++ });

    const clientA = new WebSocket(`ws://127.0.0.1:${port}/ws`);
    const clientB = new WebSocket(`ws://127.0.0.1:${port}/ws`);
    await Promise.all([waitForOpen(clientA), waitForOpen(clientB)]);
    expect(hub.clientCount).toBe(2);

    clientA.close();
    await new Promise((r) => setTimeout(r, 50));
    expect(fired).toBe(0); // one client remains

    clientB.close();
    await new Promise((r) => setTimeout(r, 50));
    expect(fired).toBe(1);

    hub.close();
  });
});
