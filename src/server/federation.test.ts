import { afterEach, describe, expect, test } from "bun:test";
import { Database } from "bun:sqlite";
import { createServer, type Server } from "node:http";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { runMigrations } from "./db.ts";
import { FederationTunnelManager, setFederationNodeEnabled, upsertFederationNode } from "./federation.ts";

const dirs: string[] = [];
const servers: Server[] = [];
const fakeTunnel = fileURLToPath(new URL("./testFixtures/fakeSshTunnel.ts", import.meta.url));

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => new Promise<void>((resolve) => server.close(() => resolve()))));
  while (dirs.length) rmSync(dirs.pop()!, { recursive: true, force: true });
});

async function listenRemote(expectedToken: string, counter: { requests: number }): Promise<number> {
  const server = createServer((req, res) => {
    counter.requests += 1;
    if (req.headers.authorization !== `Bearer ${expectedToken}`) {
      res.writeHead(401, { "content-type": "application/json" });
      res.end('{"error":"unauthorized"}');
      return;
    }
    if (req.url === "/health") {
      res.writeHead(200, { "content-type": "application/json" });
      res.end('{"ok":true}');
      return;
    }
    if (req.url === "/api/queue") {
      res.writeHead(200, { "content-type": "application/json" });
      res.end('{"items":[{"id":"remote-review","status":"queued"}]}');
      return;
    }
    res.writeHead(404).end();
  });
  servers.push(server);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("expected TCP address");
  return address.port;
}

describe("federation loopback tunnel fixture", () => {
  test("opens an on-demand loopback tunnel, authenticates, caches GETs, and reconnects", async () => {
    const token = "remote-test-token";
    const requests = { requests: 0 };
    const remotePort = await listenRemote(token, requests);
    const binDir = mkdtempSync(join(tmpdir(), "cmux-localreview-fake-ssh-"));
    dirs.push(binDir);
    writeFileSync(join(binDir, "ssh"), `#!/bin/sh\nexec bun ${JSON.stringify(fakeTunnel)} "$@"\n`, { mode: 0o755 });
    const db = new Database(":memory:");
    runMigrations(db);
    const node = upsertFederationNode(db, { label: "fixture remote", sshTarget: "fixture@remote", remotePort, token });
    const manager = new FederationTunnelManager(db, join(binDir, "ssh"));
    try {
      expect(manager.status(node.id).state).toBe("disconnected");
      const first = await manager.request<{ items: { id: string }[] }>(node.id, "/api/queue", {}, 60_000);
      expect(first.items[0]?.id).toBe("remote-review");
      expect(manager.status(node.id).state).toBe("connected");
      const requestsAfterFirst = requests.requests;
      const cached = await manager.request<{ items: { id: string }[] }>(node.id, "/api/queue", {}, 60_000);
      expect(cached).toEqual(first);
      expect(requests.requests).toBe(requestsAfterFirst);
      manager.stop(node.id);
      expect(manager.status(node.id).state).toBe("disconnected");
      await manager.connect(node.id);
      expect(manager.status(node.id).state).toBe("connected");
    } finally {
      manager.stopAll();
      db.close();
    }
  });

  test("keeps an explicitly disconnected node out of lazy aggregation until it is re-enabled", () => {
    const db = new Database(":memory:");
    runMigrations(db);
    try {
      const node = upsertFederationNode(db, { label: "paused", sshTarget: "fixture@remote", remotePort: 57140, token: "token" });
      expect(setFederationNodeEnabled(db, node.id, false)?.enabled).toBe(false);
      const manager = new FederationTunnelManager(db);
      expect(manager.status(node.id).state).toBe("disabled");
      expect(setFederationNodeEnabled(db, node.id, true)?.enabled).toBe(true);
      expect(manager.status(node.id).state).toBe("disconnected");
    } finally { db.close(); }
  });
});
