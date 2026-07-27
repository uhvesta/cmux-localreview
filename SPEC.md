# cmux-localreview — Spec Prompt

You are building **cmux-localreview**: a local, browser-based code-review tool in the spirit of **difit**, integrated with **cmux** the way **cmux-hub** is, with the multi-repo workspace model and Full File view of **localreview**. It lives in this directory (`cmux-localreview/`), as its own standalone project.

Three sibling projects are available for reference and for copying code:

- `../difit` — TypeScript CLI + React diff viewer (MIT). Source of the GitHub-style diff UI, inline comments, comment→AI-prompt formatting (`src/utils/commentFormatting.ts`), context expansion (`src/client/components/ExpandButton.tsx`), and the server/watcher patterns in `src/server/`.
- `../cmux-hub` — Bun + React diff viewer for cmux. Source of the cmux Unix-socket protocol (`server/cmux.ts`: newline-delimited JSON-RPC over `/tmp/cmux.sock`, path discovered via `/tmp/cmux-last-socket-path`, `sendText`, `listSurfaces`), the review-dir watcher (`server/review-watcher.ts`, `server/review.ts` with `CMUX_WORKSPACE_ID`/`CMUX_SURFACE_ID` binding), WebSocket broadcast plumbing (`server/app.ts`), Shiki highlighting (`server/highlighter.ts`, `server/diff-highlight.ts`), and the Bun build/compile setup (`build.ts`, `build-compile.ts`).
- `../cmux/agent-chat` — cmux's agent chat sidecar. Source of the generic ACP adapter (`adapters/acp.ts`) used for /btw, and the reference for provider registry/permission handling patterns.
- `../localreview` — Rust/Tauri desktop app. **Reference only** (different stack; do not copy code). Its `PRODUCT_SPEC.md` §5.1 (workspace model) and §13.3 (Full File view) are the behavioral contract for those features here.

## Product summary

A CLI (`cmux-localreview [workspace-dir]`) that starts a local web server and opens a review UI for a **workspace**: a directory that may contain many git repositories at arbitrary subpaths. The reviewer browses diffs across all repos, leaves inline comments, exports all feedback as a single AI prompt, and can fire `/btw` side-questions at the agent running in the cmux terminal and see the answers rendered in the UI. Everything persists in SQLite.

## Tech stack

- **Bun** as the server/CLI runtime (gives `bun:sqlite`, built-in WebSocket server, optional single-binary compile). The client is difit's React 19 + Tailwind + Prism app, kept on its Vite build.
- SQLite via `bun:sqlite`. DB file at `~/.local/share/cmux-localreview/<workspace-hash>.db` (hash of the resolved workspace root path), so re-opening the same workspace resumes the same review state. `--db <path>` overrides.
- No network dependencies at runtime; everything is local.

## Vendoring policy

Licenses were checked: **difit is MIT, cmux-hub is MIT, but the cmux repo (including `agent-chat/`) is dual-licensed GPL-3.0-or-later / commercial (Manaflow, Inc.)** — do not copy code from `../cmux` into this project; use it as a design reference only. The ACP need is covered by the official Apache-2.0 SDK instead (see §4).

Strategy: **fork difit as the trunk, cherry-pick from cmux-hub.**

- difit's client and diff machinery are deeply interdependent (`DiffChunk.tsx` ~577 lines, `git-diff.ts` ~873, `useDiffComments.ts` ~467); extracting components piecemeal costs more than taking the whole thing. Copy difit's `src/` wholesale into `vendor/difit/` at a recorded commit, then modify in place. Its Express 5 + simple-git server runs fine on Bun.
- `vendor/cmux-hub/` gets the genuinely standalone modules: `server/cmux.ts` (socket client + dry-run connector — only `node:net`/`node:fs` imports) and `server/review-watcher.ts`.
- Each vendor directory contains the upstream `LICENSE` and a `VENDOR.md` recording upstream repo, commit SHA, file list, and a running log of local modifications. This is a hard fork — upstream difit updates will not merge cleanly, and that's accepted.
- Keep difit's Prism-based highlighting rather than porting to cmux-hub's Shiki pipeline; a highlighter swap is churn with no feature payoff for v1.
- Keep difit's Vite build for the client; Bun is the server/CLI runtime and provides `bun:sqlite`.
- localreview is not vendored (Rust); reimplement the behaviors its spec describes.

## Core features

### 1. Workspace of git repositories

- On startup, scan the workspace directory recursively for git working trees (stop descending at a repo root; respect a sane depth limit and skip `node_modules`, `.git` internals, hidden dirs). Nested worktrees/submodules count as their enclosing repo unless they are independent working trees.
- Repo identity is durable: workspace-relative path + canonical git dir + normalized primary remote URL (localreview §5.2) — not just directory name.
- Left sidebar: repo tree with per-repo change counts; selecting a repo shows its changed-file list; selecting a file shows the diff. A flat "all changed files across repos" mode is also required, files grouped by repo.
- Diff base per repo: default is difit-style `HEAD` vs working tree (`.` semantics: staged + unstaged + untracked), with a per-repo base override (branch/commit, e.g. merge-base with `main`) selectable in the UI and remembered in SQLite. Support difit's special targets (`.`, `staged`, `working`) as the workspace-wide default via CLI flag.
- File watching per repo (difit `file-watcher.ts` / cmux-hub `watcher.ts` pattern): working-tree and ref changes push a `diff-updated` WebSocket event; the client shows a reload affordance rather than yanking the view out from under the reviewer.

### 2. Diff viewing

- GitHub-style unified and side-by-side views (difit's, as forked), Prism syntax highlighting, add/delete coloring, line numbers, word-level intraline highlighting where cheap.
- **Context expansion like GitHub**: between/around hunks, chevron buttons to expand N lines up / N lines down / expand-all-between; expanded lines are real file content fetched from the server and become commentable context rows. difit already ships this end to end (`ExpandButton.tsx` + `/api/blob/<path>` + `/api/line-count`) — the work here is making those endpoints repo-aware, not building the feature.
- **Full File view** (localreview §13.3, adapted):
  - A third view mode showing the complete current file with the diff projected onto it.
  - Additions render inline expanded by default; deleted regions render as **collapsible red gates** (a chevron row labeled with the omitted line range, e.g. `▸ 12 deleted lines (old 240–251)`) that expand in place to show the removed lines. Symmetrically, a Base toggle shows the complete old file with additions as collapsible green gates.
  - Every gate has its own chevron; global controls exist for show/hide-all additions and show/hide-all deletions independently.
  - Deleted files show the full baseline with a deleted-file banner; renames show both paths.
  - Comments can be placed on changed or unchanged lines in this view and must anchor to the same canonical (side, line) identity used by the other views, so a comment made in Full File appears correctly in Unified.
  - Virtualize rows in this view (e.g. `@tanstack/react-virtual`); a 50k-line file must stay usable. Note difit has no virtualization anywhere — this is net-new work, and it is deliberately scoped to Full File view only; Unified/Split keep difit's plain rendering for v1.
- View mode, collapsed-file state, and per-file scroll position persist per session in SQLite.

### 3. Review comments and prompt export

- difit-style inline comments: click/drag line ranges, multi-line comments, edit/delete, optional suggestion blocks. Comments are keyed by (repo, file path, side, line range) and survive diff refreshes via difit's re-anchoring approach where the line content still matches; orphaned comments are kept and flagged, never silently dropped.
- **Export prompt**: one action produces a single markdown prompt aggregating *all* comments across *all* repos in the workspace, grouped by repo then file, each entry as `<workspace-relative-repo>/<file>:<line-range>` plus the comment body (extend difit's `commentFormatting.ts` to be repo-aware). Two delivery modes: copy to clipboard, and **send to cmux** (paste into the connected terminal surface via `sendText`). Exports are recorded in SQLite (timestamp + content) so a session's feedback history is auditable.

### 4. `/btw` questions → agent via ACP, answers in the UI

- A persistent input in the UI (and a "btw" action on any selected line range) that asks a side-question about the code under review without it becoming part of the formal review feedback.
- **Primary transport: ACP (Agent Client Protocol, agentclientprotocol.com).** Use the official `@zed-industries/agent-client-protocol` SDK (Apache-2.0) — not vendored cmux code, which is GPL (see Vendoring). The protocol flow is `initialize → session/new → session/prompt` over stdio NDJSON JSON-RPC, with streamed `session/update` notifications and reverse `session/request_permission` requests; `../cmux/agent-chat/adapters/acp.ts` is the reference for the practical wrinkles (serialize prompts — agents reject overlapping `session/prompt`; handle agents that hang on `initialize`; `session/cancel` for stop). The server spawns/owns one ACP agent session per review session, `cwd` = the workspace root (or the relevant repo root when the question carries file context), so the agent can read the actual code it's asked about. Provider is configurable (`--agent claude|opencode|gemini|<cmd>`, defaulting to claude via `@zed-industries/claude-code-acp`, Apache-2.0). Precedent that this works read-only: cmux's `agent-chat/review-question.ts` runs exactly this shape of headless, read-only review question against a repo root.
- Questions are formatted with their `repo/file:line` context and quoted code, then sent as `session/prompt`. `session/update` deltas stream over our WebSocket into a **BTW panel**: threaded Q&A cards — question with its code context on top, the agent's answer streaming in below as rendered markdown with Shiki-highlighted code blocks, live thinking/tool-activity shown minimally while the turn runs. Answer `session/request_permission` with a read-only auto-approve policy (deny writes/execs by default; btw is a question channel, not an edit channel).
- Prompts are serialized per session (one in-flight turn; queue follow-ups). Store the ACP session id so a restart can try `session/load` to resume; if the provider doesn't support it, start a fresh session — history still renders from SQLite.
- **Secondary mode — ask the terminal agent**: a per-question toggle that instead injects the question into the existing cmux terminal surface via `sendText` (cmux-hub style), for when you want the answer from the agent already holding the working context. Answers there come back via the vendored review-watcher on the btw response directory (`$TMPDIR/cmux-localreview/<workspace-or-surface-id>/btw/<question-id>.md`, binding resolution from cmux-hub's `resolveDefaultReviewDir`), with the injected text instructing the agent to write its answer there.
- Questions and answers persist in SQLite (with which transport answered them) so threads survive restarts; sockets, stdio, and tmp files are just transport.

### 5. Persistence (SQLite)

Single DB per workspace. Tables (adjust as needed, use a tiny hand-rolled migration runner with `PRAGMA user_version`):

- `workspace` (root path, created_at), `repos` (identity fields, base override)
- `sessions` (a review session; one current session, "New Review" freezes it and starts the next — comments and btw threads are session-scoped, frozen sessions remain browsable/exportable, per localreview §5.5)
- `comments` (session, repo, file, side, start/end line, body, anchor content hash, orphaned flag, timestamps)
- `btw_threads` (session, ACP provider + session id for resume, transport) / `btw_questions` / `btw_answers`
- `exports` (session, content, created_at)
- `ui_state` (view mode, collapsed files, viewed-checkbox state per file)

## CLI

`cmux-localreview [dir]` (default `.`) with flags: `--port`/`-p`, `--host`, `--open/--no-open`, `--base <ref>` (workspace default), `--db <path>`, `--agent <provider|cmd>` (ACP agent for /btw, default claude), `--dry-run` (log cmux/ACP sends instead of connecting/spawning), `--clean` (start a fresh session). Auto-shutdown when the last browser tab disconnects, like cmux-hub, with a `--keep-alive` opt-out.

## Non-goals (v1)

GitHub PR integration, difftastic mode, split-pane resizable IDE chrome, SSH/remote workspaces, multi-user. Don't build these; don't design them in either beyond not painting into a corner.

## Milestones

1. **Scaffold + vendoring**: fork difit into `vendor/difit/` and get it building and serving under Bun unchanged; vendor cmux-hub's socket client and review-watcher with `VENDOR.md`s; CLI arg parsing; SQLite layer + migrations.
2. **Workspace + diff core**: repo discovery, per-repo diff (unified + side-by-side), file list sidebar, WebSocket refresh.
3. **Comments + export**: inline comments with persistence, workspace-aware prompt export (clipboard + send-to-cmux).
4. **Context expansion + Full File view**: GitHub-style expand chevrons, then the Full File projection with gates and global add/del toggles, virtualized.
5. **/btw**: vendored ACP client wired to a workspace-scoped agent session, streaming BTW panel, read-only permission policy, persistence + `session/load` resume; then the secondary send-to-terminal mode with its response watcher.
6. **Polish**: sessions ("New Review"), viewed checkboxes, keyboard nav (j/k file/hunk, `c` comment, `v` viewed), auto-shutdown, `--dry-run` e2e pass without cmux running.

Each milestone lands with tests (`bun test`) for the server-side logic — repo discovery, diff parsing, comment anchoring/re-anchoring, prompt formatting, btw ACP turn lifecycle against a scripted fake ACP agent (a tiny stdio NDJSON stub), btw file-drop → broadcast — and a smoke check that the built binary serves the UI.

## Acceptance criteria

- Point it at a directory containing ≥2 nested git repos with mixed staged/unstaged/untracked changes: both repos appear, diffs are correct, comments on each repo export into one prompt with correct repo-relative paths.
- Full File view: deletions collapsed to red chevron gates by default, each individually expandable, global show/hide for additions and deletions works, comment placed there shows correctly in Unified view.
- Context expansion matches GitHub behavior (expand up/down/all, expanded lines commentable).
- `/btw` question with a line-range context gets a streamed answer from the ACP agent in the BTW panel, and the agent demonstrably read the referenced file (its answer reflects actual file content); write/exec permission requests from the agent are denied. Both question and answer survive a server restart.
- The send-to-terminal /btw mode injects the question into the cmux terminal; a markdown file written to the btw response dir renders as the answer within ~1s.
- Kill and relaunch the server on the same workspace: comments, btw threads, view state, and session history are all intact.
- Everything works with `--dry-run` when cmux isn't running (sends are logged, UI still functions).
