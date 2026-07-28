import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync, chmodSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export interface DaemonDiscovery {
  port: number;
  token: string;
  pid: number;
  version: string;
  createdAt: string;
}

export function localreviewDataDir(): string {
  return join(homedir(), ".local", "share", "cmux-localreview");
}

export function daemonDiscoveryPath(): string {
  return join(localreviewDataDir(), "daemon.json");
}

export function daemonDbPath(): string {
  return join(localreviewDataDir(), "daemon.db");
}

export function artifactsDir(): string {
  return join(localreviewDataDir(), "artifacts");
}

export function ensureDaemonDirectories(): void {
  mkdirSync(localreviewDataDir(), { recursive: true, mode: 0o700 });
  mkdirSync(artifactsDir(), { recursive: true, mode: 0o700 });
}

export function newDaemonToken(): string {
  return randomBytes(32).toString("base64url");
}

/** Discovery is deliberately owner-only: it contains the bearer credential. */
export function writeDiscovery(value: DaemonDiscovery): void {
  ensureDaemonDirectories();
  const output = daemonDiscoveryPath();
  const temporary = `${output}.${process.pid}.tmp`;
  writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { encoding: "utf8", mode: 0o600 });
  chmodSync(temporary, 0o600);
  renameSync(temporary, output);
  chmodSync(output, 0o600);
}

export function readDiscovery(): DaemonDiscovery | undefined {
  const path = daemonDiscoveryPath();
  if (!existsSync(path)) return undefined;
  try {
    const value = JSON.parse(readFileSync(path, "utf8")) as DaemonDiscovery;
    if (!Number.isInteger(value.port) || !value.token || !value.pid) return undefined;
    return value;
  } catch {
    return undefined;
  }
}
