#!/usr/bin/env bun
/**
 * Tiny `ssh -L` substitute used only by the federation integration test.
 *
 * It understands the exact `-L 127.0.0.1:local:127.0.0.1:remote` invocation
 * emitted by FederationTunnelManager and proxies TCP on loopback.  Keeping
 * this separate from production lets the test exercise real lazy tunnel,
 * health, auth, cache, disconnect, and reconnect behavior without requiring
 * an SSH daemon or credentials on the test machine.
 */
import { connect, createServer } from "node:net";

const forwardIndex = process.argv.indexOf("-L");
const forward = forwardIndex >= 0 ? process.argv[forwardIndex + 1] : undefined;
const match = forward?.match(/^127\.0\.0\.1:(\d+):127\.0\.0\.1:(\d+)$/);
if (!match) {
  console.error("fakeSshTunnel requires -L 127.0.0.1:<local>:127.0.0.1:<remote>");
  process.exit(2);
}

const [, local, remote] = match;
const server = createServer((incoming) => {
  const outgoing = connect({ host: "127.0.0.1", port: Number(remote) });
  incoming.pipe(outgoing);
  outgoing.pipe(incoming);
  const close = () => incoming.destroy();
  incoming.once("error", close);
  outgoing.once("error", close);
});

server.listen(Number(local), "127.0.0.1");
const close = () => server.close(() => process.exit(0));
process.once("SIGTERM", close);
process.once("SIGINT", close);
