import express, { type Router } from "express";

import type { DiffApp } from "../../vendor/difit/src/server/server.ts";
import type { DiffSelection } from "../../vendor/difit/src/types/diff.ts";

import { buildGates } from "./fullFileProjection.ts";

function isPathSafe(filePath: string): boolean {
  if (!filePath) return false;
  const normalized = filePath.replace(/\\/g, "/");
  if (normalized.startsWith("/")) return false;
  return !normalized.split("/").some((segment) => segment === "..");
}

/**
 * GET /api/fullfile/<path>?side=current|base — full-file projection for
 * SPEC.md §2's Full File view. Mounted per-repo (before diffApp.app),
 * analogous to the comments router.
 */
export function createFullFileRouter(diffApp: DiffApp, selection: DiffSelection): Router {
  const router = express.Router();

  router.get(/^\/api\/fullfile\/(.*)$/, async (req, res) => {
    const rawPath = req.params[0] ?? "";
    let filePath: string;
    try {
      filePath = decodeURIComponent(rawPath);
    } catch {
      res.status(400).json({ error: "Invalid file path" });
      return;
    }
    if (!isPathSafe(filePath)) {
      res.status(400).json({ error: "File path outside repository" });
      return;
    }

    const side = req.query.side === "base" ? "base" : "current";

    try {
      const diffData = await diffApp.parser.parseDiff(selection, false);
      const file = diffData.files.find((f) => f.path === filePath || f.oldPath === filePath);
      if (!file) {
        res.status(404).json({ error: "File not found in diff" });
        return;
      }

      if (side === "current" && file.status === "deleted") {
        res.json({ side, path: filePath, oldPath: file.oldPath, status: file.status, deleted: true });
        return;
      }
      if (side === "base" && file.status === "added") {
        res.json({ side, path: filePath, oldPath: file.oldPath, status: file.status, added: true });
        return;
      }

      const ref = side === "current" ? diffData.targetCommitish : diffData.baseCommitish;
      const blobPath = side === "base" && file.oldPath ? file.oldPath : filePath;
      const buffer = await diffApp.parser.getBlobContent(blobPath, ref ?? "HEAD");
      const lines = buffer.toString("utf-8").split("\n");
      if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();

      const gates = buildGates(file.chunks, side);

      res.json({
        side,
        path: filePath,
        oldPath: file.oldPath,
        status: file.status,
        lines,
        gates,
      });
    } catch (error) {
      console.error("Error building full file view:", error);
      res.status(500).json({ error: "Failed to build full file view" });
    }
  });

  return router;
}
