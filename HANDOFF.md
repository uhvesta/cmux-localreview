# cmux-localreview — Implementation Handoff

You are implementing the project defined in `SPEC.md` (same directory). Read it fully first; it is the contract. This document is the execution plan: what's already been verified, where to take code from, what order to build in, and the traps found during research. Where this document and `SPEC.md` disagree, `SPEC.md` wins — flag the conflict.

## Ground truth already verified (don't re-derive)

- `../difit` (MIT), `../cmux-hub` (MIT) — safe to vendor. `../cmux` is **GPL-3.0-or-later/commercial dual-licensed** — reference only, never copy from it.
- difit already implements GitHub-style context expansion end to end: `src/client/components/ExpandButton.tsx` (expand up/down/all, 20-line steps) backed by `GET /api/blob/<path>?ref=` and `GET /api/line-count/<path>` in `src/server/server.ts`. These endpoints are also the primitives for Full File view.
- difit's server is Express 5 + simple-git; it runs under Bun. Client is React 19 + Tailwind 4 + Prism, built with Vite. Comments live in localStorage (`useLocalComments.ts`, keyed `difit-comments-<commitHash>`) plus server-memory endpoints (`/api/comments*`) — this is what SQLite replaces.
- difit has **no virtualization anywhere**. Virtualization is net-new and scoped to Full File view only.
- cmux-hub's `server/cmux.ts` is standalone (only `node:net`, `node:fs`): NDJSON JSON-RPC over the socket at the path read from `/tmp/cmux-last-socket-path` (fallback `/tmp/cmux.sock`), 5s request timeout, `sendText(text, surfaceId)`, `listSurfaces()`, plus a `createDryRunConnector()` for development without cmux. `server/review-watcher.ts` is likewise standalone (recursive fs watch + debounce + stale-dir re-resolution).
- ACP: use `@zed-industries/agent-client-protocol` (Apache-2.0, verified on npm) and `@zed-industries/claude-code-acp` (Apache-2.0) as the default `--agent`. `../cmux/agent-chat/adapters/acp.ts` is the reference for protocol wrinkles: serialize `session/prompt` calls (agents reject overlap — chain turns on a promise), guard `initialize`/`session/new` with a startup timeout (agents can hang silently), `session/cancel` to stop, `session/update` notifications carry the streamed content, reverse `session/request_permission` must be answered. `../cmux/agent-chat/review-question.ts` is precedent for read-only Q&A sessions.
- cmux-hub's `server/review.ts` `resolveDefaultReviewDir()` shows the instance-binding order for tmp dirs: explicit override → `CMUX_WORKSPACE_ID` → `CMUX_SURFACE_ID` → pid.

## Project layout

```
cmux-localreview/
  SPEC.md  HANDOFF.md
  package.json            # bun; workspaces or plain — keep it simple
  src/
    cli.ts                # arg parsing (commander, vendored with difit, is fine)
    server/               # bun entry; mounts the (modified) difit express app
      db.ts               # bun:sqlite open + migration runner (PRAGMA user_version)
      workspace.ts        # repo discovery + durable repo identity
      acp.ts              # ACP session manager built on the official SDK
      btw.ts              # btw threads: ACP path + terminal path
    client/               # our components: repo sidebar, FullFileView, BtwPanel
  vendor/
    difit/                # difit src/ forked wholesale; LICENSE + VENDOR.md
    cmux-hub/             # cmux.ts, review-watcher.ts; LICENSE + VENDOR.md
```

`vendor/difit/` is a hard fork: modify it in place, log every modification in `VENDOR.md` (upstream commit SHA, file, why). Don't fight to keep it pristine — but don't scatter changes either; prefer extending via `src/` where a clean seam exists.

## Build order

Follow the six milestones in `SPEC.md`. Per-milestone notes:

### M1 — Scaffold + vendoring
Copy difit's `src/`, `vite.config.ts`, tailwind/postcss configs into `vendor/difit/` (record the SHA from `cd ../difit && git rev-parse HEAD`). Get `bun vendor-difit-server` serving the unmodified difit UI against a single test repo before touching anything — that's the M1 gate. Then add `src/server/db.ts` and the migration runner. Copy the two cmux-hub files. Write both `VENDOR.md`s now, not later.

### M2 — Workspace + multi-repo
`workspace.ts`: recursive scan (skip `node_modules`, hidden dirs; stop descending at a repo root; depth cap ~6). Namespace all difit API routes with a repo segment (`/api/repos/<repoId>/diff`, `/api/repos/<repoId>/blob/...`) and instantiate difit's diff parser per repo. Client: repo tree sidebar with change counts + the flat all-repos file list grouped by repo. Wire per-repo file watching → WebSocket `diff-updated` (difit's watch endpoint is SSE-ish; replace with one Bun WebSocket, cmux-hub `app.ts` broadcast pattern).

### M3 — Comments + export
Replace `useLocalComments`/localStorage with server API backed by SQLite (schema in SPEC). Keep difit's anchoring data (line content snapshot) so re-anchoring survives; add the orphaned flag. Extend `src/utils/commentFormatting.ts` to prefix `<repo>/<file>:<lines>`. Export action: aggregate all repos → clipboard, and → cmux via `sendText` (respect `--dry-run`). Record exports in SQLite.

### M4 — Context expansion (repo-aware) + Full File view
Expansion mostly works once M2's route namespacing is done — verify expanded lines are commentable. Full File view is the big custom build:
- Server: `GET /api/repos/<id>/fullfile/<path>?side=current|base` returns full content + the hunk projection (compute gates server-side: for `current`, deletions become gate records `{afterLine, oldStart, oldEnd, lines[]}`; symmetric for `base`).
- Client: new `FullFileView` component (in `src/client/`, not inside vendor): virtualized rows (`@tanstack/react-virtual`), gate rows with chevrons, global show/hide-all for additions and deletions independently, comment anchoring mapped to the same (side, line) identity as unified view. Deleted-file banner, rename dual paths.
- Test the projection math as pure functions — it's the bug farm (gate placement around adjacent hunks, EOF gates, files with only deletions).

### M5 — /btw
`acp.ts`: one agent process per review session via the official SDK; `cwd` = workspace root or repo root when the question has file context. Serialize prompts; startup timeout ~10s with a clear UI error; answer `session/request_permission` allow-read/deny-write-exec; store ACP session id, attempt `session/load` on restart, fall back to fresh. Stream `session/update` → WebSocket → BtwPanel (question card + code context, streamed markdown answer, pending state). Secondary terminal mode: `sendText` the question with the write-your-answer-to-`<btwDir>/<questionId>.md` instruction; review-watcher on that dir → broadcast. Persist everything (SPEC schema).

### M6 — Polish
Sessions/New Review freeze, viewed checkboxes, keyboard nav (difit has `react-hotkeys-hook` wired — extend it), auto-shutdown on last WebSocket disconnect (`--keep-alive` opt-out), `--clean`.

## Testing & verification loop

- `bun test` for server logic. Priority order: Full File gate projection, repo discovery (fixture: tmp dir with 2 nested repos + non-repo noise), comment anchor/re-anchor/orphan, repo-aware prompt formatting, migration runner.
- ACP: test the turn lifecycle against a **scripted fake agent** — a ~50-line bun script speaking NDJSON on stdio that answers `initialize`/`session/new`, streams a canned `session/update` sequence, and (in one variant) issues a `session/request_permission` for a write so you can assert the denial. Also make a variant that never answers `initialize` to test the timeout.
- End-to-end smoke without cmux: `--dry-run` + fixture workspace; assert the UI serves, diffs render, a comment round-trips through SQLite, and an export logs the would-be sendText.
- Manual check with real cmux + real claude-code-acp once per milestone 5+ change; everything else must not require cmux running.
- Create fixture repos in tests programmatically (`git init` in tmp dirs); never depend on the sibling checkouts' git state.

## Traps

- **License**: nothing from `../cmux` gets copied. If you're tempted, reimplement against the Apache SDK.
- **Path traversal**: difit's `/api/blob/(.*)` style routes + our repo namespacing = check that resolved paths stay inside the repo root. Add the check in M2, test it.
- **Express-on-Bun**: works, but don't mix Express's server with Bun's WebSocket — run one Bun server and mount the Express app as the fetch handler, or port routes to Bun.serve as you touch them. Pick one in M1 and stay consistent.
- **Untracked files**: difit's `.` semantics include untracked; multi-repo scan must not treat a repo nested inside another repo's untracked dir as belonging to the outer repo.
- **Comment identity across views**: Full File comments must use the same (repo, file, side, line) key as unified — no view-specific comment records, or M4's acceptance criterion fails.
- **50k-line file**: keep gate expansion lazy (gate contents fetched or at least not mounted until expanded), or virtualization won't save you.
- **`session/load`**: many agents don't support it — treat failure as normal, not an error state.

## Definition of done

The acceptance criteria in `SPEC.md` §Acceptance, executed literally, each demonstrated at least once: multi-repo workspace with mixed staged/unstaged/untracked changes; Full File gates + global toggles + cross-view comment; GitHub-parity expansion; streamed ACP /btw answer that reflects real file content with write-permission denial; terminal-mode /btw file-drop; full restart persistence; complete `--dry-run` operation without cmux. Ship each milestone green (`bun test` + typecheck + the smoke run) before starting the next.
