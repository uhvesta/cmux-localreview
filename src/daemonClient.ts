import { fileURLToPath } from "node:url";

import { readDiscovery, type DaemonDiscovery } from "./server/daemonPaths.ts";

export interface DaemonClient {
  discovery: DaemonDiscovery;
  baseUrl: string;
  request: <T>(path: string, init?: RequestInit) => Promise<T>;
}

export interface ConnectDaemonOptions {
  /** Start the loopback daemon when its discovery entry is absent or stale. */
  startIfNeeded?: boolean;
  timeoutMs?: number;
}

function urlFor(discovery: DaemonDiscovery): string {
  return `http://127.0.0.1:${discovery.port}`;
}

async function fetchWithTimeout(url: string, init: RequestInit, timeoutMs: number): Promise<Response> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await fetch(url, { ...init, signal: controller.signal });
  } finally {
    clearTimeout(timeout);
  }
}

async function isHealthy(discovery: DaemonDiscovery, timeoutMs = 750): Promise<boolean> {
  if (!Number.isInteger(discovery.port) || discovery.port < 1 || discovery.port > 65_535 || !discovery.token) return false;
  try {
    const response = await fetchWithTimeout(`${urlFor(discovery)}/health`, {}, timeoutMs);
    return response.ok;
  } catch {
    return false;
  }
}

function startDaemonProcess(): void {
  const daemonEntry = fileURLToPath(new URL("./global-daemon.ts", import.meta.url));
  Bun.spawn({
    cmd: [process.execPath, daemonEntry],
    stdin: "ignore",
    stdout: "ignore",
    stderr: "ignore",
  });
}

function sleep(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

/**
 * Reads the owner-only discovery record and verifies that it belongs to a
 * responsive daemon.  The first caller starts a detached daemon if needed;
 * concurrent callers simply converge on the discovery record it writes.
 */
export async function connectDaemon(options: ConnectDaemonOptions = {}): Promise<DaemonClient> {
  const timeoutMs = options.timeoutMs ?? 5_000;
  const current = readDiscovery();
  if (!current || !(await isHealthy(current))) {
    if (options.startIfNeeded === false) throw new Error("cmux-localreview daemon is not running");
    startDaemonProcess();
    const deadline = Date.now() + timeoutMs;
    let discovered: DaemonDiscovery | undefined;
    while (Date.now() < deadline) {
      discovered = readDiscovery();
      if (discovered && await isHealthy(discovered)) break;
      await sleep(100);
    }
    if (!discovered || !(await isHealthy(discovered))) {
      throw new Error("cmux-localreview daemon did not become ready; run global-daemon to inspect its error output");
    }
    return createClient(discovered, timeoutMs);
  }
  return createClient(current, timeoutMs);
}

function createClient(discovery: DaemonDiscovery, timeoutMs: number): DaemonClient {
  const baseUrl = urlFor(discovery);
  return {
    discovery,
    baseUrl,
    async request<T>(path: string, init: RequestInit = {}) {
      const headers = new Headers(init.headers);
      headers.set("authorization", `Bearer ${discovery.token}`);
      if (init.body && !headers.has("content-type")) headers.set("content-type", "application/json");
      const response = await fetchWithTimeout(`${baseUrl}${path}`, { ...init, headers }, timeoutMs);
      const raw = await response.text();
      let payload: unknown = raw;
      try { payload = raw ? JSON.parse(raw) : undefined; } catch { /* retain text response */ }
      if (!response.ok) {
        const detail = typeof payload === "object" && payload && "error" in payload
          ? String((payload as { error: unknown }).error)
          : raw || response.statusText;
        throw new Error(`daemon ${response.status} ${path}: ${detail}`);
      }
      return payload as T;
    },
  };
}
