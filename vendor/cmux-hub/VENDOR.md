# Vendored: cmux-hub (standalone modules only)

- Upstream: https://github.com/azu/cmux-hub (MIT)
- Vendored commit: `c35b3de14860f32b1348f9669a38d4ba9c1c1eeb`
- Local path: `../../cmux-hub` (sibling checkout used for the copy)

## What was copied

Only the genuinely standalone modules (`node:net`/`node:fs`/`node:os`/`node:path`
and Bun built-ins only — no imports into the rest of cmux-hub's app):

- `cmux.ts` (from `server/cmux.ts`) — Unix-socket JSON-RPC client: `sendText`,
  `sendKey`, `listSurfaces`, `notify`, `sendComment`, `sendCommand`, plus
  `createDryRunConnector()` for `--dry-run`.
- `review-watcher.ts` (from `server/review-watcher.ts`) — recursive fs watch +
  debounce + periodic stale-dir re-resolution, broadcasts a `review-updated`
  event.
- `review.ts` (from `server/review.ts`) — `resolveDefaultReviewDir()` binding
  order (override → `CMUX_WORKSPACE_ID` → `CMUX_SURFACE_ID` → pid),
  `resolveReviewDirs`, `isPathInsideReviewDirs` (path-escape guard),
  `listReviewFiles`, `removeDirSafe`.
- `logger.ts` (from `server/logger.ts`) — trivial debug/info logger,
  vendored only because `review-watcher.ts` imports it.

Everything else in cmux-hub (React client, Express/Bun app, Shiki highlighter,
build scripts) is **not** vendored — SPEC.md scopes the client/diff UI to
difit, not cmux-hub.

## Local modifications

None yet. These files are used as-is for M1; `--btw` (M5) will consume
`resolveDefaultReviewDir`/`review-watcher.ts` for the terminal-mode response
path, and the cmux socket client will be wired into export "send to cmux" in
M3. Any local edits will be logged here.
