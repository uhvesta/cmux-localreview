import type { Server as HttpServer } from "node:http";
import { WebSocketServer, type WebSocket } from "ws";

export type WsMessage =
  | { type: "diff-updated"; repoId: string }
  | { type: "comments-updated"; sessionId: number }
  | { type: "btw-update"; threadId: number };

/**
 * Thin broadcast hub over a `ws` WebSocketServer attached to the same
 * http.Server Express is already listening on (see traps in HANDOFF.md:
 * Bun's native WebSocket needs `Bun.serve`, which doesn't compose cleanly
 * with an Express app; `ws` attaches via the standard Node `upgrade` event,
 * which Bun's `node:http` compatibility layer supports).
 */
export class WsHub {
  private wss: WebSocketServer;
  private clients = new Set<WebSocket>();

  constructor(httpServer: HttpServer, path = "/ws") {
    this.wss = new WebSocketServer({ server: httpServer, path });
    this.wss.on("connection", (ws) => {
      this.clients.add(ws);
      ws.on("close", () => this.clients.delete(ws));
    });
  }

  broadcast(message: WsMessage): void {
    const payload = JSON.stringify(message);
    for (const client of this.clients) {
      if (client.readyState === client.OPEN) {
        client.send(payload);
      }
    }
  }

  get clientCount(): number {
    return this.clients.size;
  }

  close(): void {
    for (const client of this.clients) client.terminate();
    this.clients.clear();
    this.wss.close();
  }
}
