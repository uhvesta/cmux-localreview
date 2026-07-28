import { cpSync, existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import type { QueueItem } from "./queueStore.ts";
import { materializeSnapshot, readSnapshotManifest, verifySnapshotManifest } from "./snapshots.ts";

export interface ReviewPackageManifest {
  version: 1;
  exportedAt: string;
  queueItem: Pick<QueueItem, "id" | "title" | "body" | "kind" | "remoteUrl" | "agentId" | "agentProvider" | "copilotSessionId" | "status" | "decisionBody">;
  snapshot: ReturnType<typeof readSnapshotManifest>;
  feedback: unknown[];
}

/**
 * Writes a portable, credential-free package by first creating a sibling
 * temporary directory and atomically renaming it into place.
 */
export function exportReviewPackage(item: QueueItem, feedback: unknown[], destination: string): string {
  if (!item.snapshotManifestPath) throw new Error("This queue item has no retained snapshot to export");
  const snapshot = verifySnapshotManifest(item.snapshotManifestPath);
  const output = resolve(destination);
  const temp = `${output}.tmp-${process.pid}-${Date.now()}`;
  if (existsSync(output)) throw new Error(`Export destination already exists: ${output}`);
  mkdirSync(temp, { recursive: true, mode: 0o700 });
  try {
    const sourceDir = dirname(item.snapshotManifestPath);
    for (const repo of snapshot.repos) cpSync(join(sourceDir, repo.bundle), join(temp, repo.bundle));
    const portableSnapshot = { ...snapshot, workspacePath: "." };
    writeFileSync(join(temp, "snapshot-manifest.json"), `${JSON.stringify(portableSnapshot, null, 2)}\n`, { mode: 0o600 });
    const manifest: ReviewPackageManifest = {
      version: 1,
      exportedAt: new Date().toISOString(),
      // Deliberately select fields; no environment, authorization, or remote
      // credentials are copied into a review package.
      queueItem: { id: item.id, title: item.title, body: item.body, kind: item.kind, remoteUrl: item.remoteUrl, agentId: item.agentId, agentProvider: item.agentProvider, copilotSessionId: item.copilotSessionId, status: item.status, decisionBody: item.decisionBody },
      snapshot: portableSnapshot,
      feedback,
    };
    writeFileSync(join(temp, "review-package.json"), `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o600 });
    renameSync(temp, output);
    return output;
  } catch (error) {
    rmSync(temp, { recursive: true, force: true });
    throw error;
  }
}

export async function materializeReviewPackage(packagePath: string, destination: string) {
  const root = resolve(packagePath);
  const manifestPath = join(root, "snapshot-manifest.json");
  if (!existsSync(manifestPath)) throw new Error(`No snapshot-manifest.json in ${root}`);
  const manifest = await materializeSnapshot(manifestPath, destination);
  const pkg = JSON.parse(readFileSync(join(root, "review-package.json"), "utf8")) as ReviewPackageManifest;
  return { manifest, package: pkg };
}
