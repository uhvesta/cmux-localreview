# cmux-localreview — Native Migration Spec

This spec defines the migration of cmux-localreview from a Bun/TypeScript server + CLI stack to **native Go binaries built with Bazel**, with TypeScript/JavaScript confined to the UI layer only. It supersedes the runtime/stack sections of `SPEC.md`; the *behavior* described in `SPEC.md` (workspace model, diff views, comments, export, btw/ask) remains the product contract and must be preserved.

## Locked decisions

These were decided explicitly and are not open for relitigating during implementation:

1. **Language: Go.** The Copilot SDK has an official Go client (`github.com/github/copilot-sdk/go`, already in `go.mod`), and this branch already carries ~3.6k lines of Go (`internal/{daemon,queue,store,gitdiff,snapshot,copilot,agent}`) with Bazel `rules_go` wired up. Rust has no Copilot SDK; it is out.
2. **Minimize Node/JS.** The daemon, servers, and every CLI are pure Go binaries with zero Node/Bun runtime dependency. JS is allowed in exactly two places: the React UI (build-time only — shipped as static assets) and the Electron shell (which bundles its own runtime).
3. **Distribution: GitHub Releases binaries.** Cross-compiled per-platform archives plus an install script. No Homebrew tap, no npm wrapper, no `go install` support commitments in v1.
4. **GitHub auth: loopback OAuth with device-flow fallback.** Loopback (browser → localhost callback) when a browser is available; device flow for SSH/headless.
5. **Model selection: Copilot SDK models.** The model picker enumerates models from the Copilot Go SDK. Claude/ACP model passthrough and arbitrary ACP agent registries are out of scope.
6. **Website = local browser UI.** The Go daemon serves the built React app on localhost. No hosted/multi-user deployment in this migration (but don't paint it out — see Non-goals).
7. **Cutover: big-bang rewrite, then a validation-and-fix loop.** The TS server is frozen now (bug fixes only if they block validation). The Go server is built to full parity in one push against the frozen TS behavior, cut over, and then hardened via an end-to-end validation loop (computer-use driven) before `src/` is deleted.

## Target architecture

```
┌─────────────────────────────────────────────────────────────┐
│  UI layer (only TS/JS)                                      │
│  vendor/difit React app + our client components             │
│  → Vite build → static assets                               │
│     ├── served by localreviewd on 127.0.0.1 (web version)   │
│     └── loaded by the Electron shell (desktop version)      │
├─────────────────────────────────────────────────────────────┤
│  localreviewd (Go, one binary)                              │
│  HTTP + WebSocket API · SQLite (modernc.org/sqlite, CGO-    │
│  free) · git exec · file watching · Copilot SDK sessions ·  │
│  GitHub auth · queue control plane · snapshots              │
├─────────────────────────────────────────────────────────────┤
│  localreview (Go, one binary, subcommands)                  │
│  open · submit · daemon · reproduce · setup · auth ·        │
│  demo · remote · github-app                                 │
└─────────────────────────────────────────────────────────────┘
```

- **One daemon binary** (`cmd/localreviewd`): the current `localreviewd` skeleton grows to absorb everything `src/server/app.ts` mounts today. It owns all state (SQLite under the existing data dir), serves the UI, and is the single long-running process. Memory target: **idle RSS < 50 MB** with a workspace open (vs. hundreds of MB for the Bun stack); no per-request goroutine leaks under `-race` soak.
- **One CLI binary** (`cmd/localreview`): the twelve npm `bin` entries collapse into subcommands of a single binary (table below). The CLI talks to the daemon over its loopback HTTP API using the existing capability/session auth (`/api/browser/session` exchange), auto-starting the daemon when absent.
- **UI**: `vendor/difit` React app stays as-is functionally. Its build output is embedded in `localreviewd` via `go:embed` (single-file distribution) with a `-ui-dir` override for development. The Vite build runs under Bazel (`rules_js`/genrule invoking `vite build`) so `bazel build //...` produces a complete daemon; Node is a **build-time** dependency only.
- **Electron shell** (`desktop/`): a minimal main process (< ~300 lines of JS) that spawns the bundled `localreviewd` as a sidecar on an ephemeral port, waits for `/health`, and opens a `BrowserWindow` at the daemon URL with the session capability injected. No business logic in Electron. The runtime is bundled (per decision 2); packaged with electron-builder, artifacts published to the same GitHub Releases.

### CLI mapping

| npm bin (delete at end) | Go replacement |
|---|---|
| `cmux-localreview` (`src/cli.ts`) | `localreview open [dir]` (also the no-args default) |
| `global-daemon` | `localreview daemon run\|status\|stop` → execs/`localreviewd` |
| `queue-submit` / `localreview-submit` | `localreview submit` |
| `localreview-open` | `localreview open --queue-item <id>` |
| `localreview-reproduce` | `localreview reproduce` |
| `localreview-reproduce-copilot` | `localreview reproduce --copilot` |
| `localreview-setup` | `localreview setup` |
| `localreview-github-app` | `localreview github-app` |
| `localreview-remote` | `localreview remote submit` |
| `localreview-remote-daemon` | `localreview remote daemon` |
| `localreview-demo` | `localreview demo` |

Flag names, env vars (`CMUX_LOCALREVIEW_DATA_DIR`, `CMUX_WORKSPACE_ID`, `CMUX_SURFACE_ID`, …), data-dir layout, and DB schemas are preserved exactly; the Go daemon must open existing TS-era databases via the already-landed legacy migration path (`2031102`).

## Functionality inventory (parity checklist)

Everything below exists in the frozen TS server and must exist in Go before cutover. Items marked ✅ have already landed on this branch; verify rather than rebuild.

**Core review** — workspace scan + repo identity (✅ activation), git diff serving (✅), revisions/diff bases (✅), file content/blob + line count (✅), full-file projection with gates, context expansion, file watching → WebSocket `diff-updated`, comments CRUD + anchoring/re-anchoring + tombstones (✅), comment imports, review-history comments, sessions + "New Review" (✅ persistence), ui-state (✅), export prompt (clipboard + send-to-cmux via the Unix-socket NDJSON client), comments-json/comments-output endpoints.

**Agent layer (Copilot Go SDK)** — ask conversations: create/fresh/list/get, messages with streaming over WebSocket, cancel, resume, settings, **per-conversation model switch** (`POST /api/ask/conversations/:id/model`) and **`GET /api/ask/models`** backed by SDK model enumeration; inline conversations; question sets CRUD + send; btw threads + ask (terminal mode via cmux sendText + response-dir watcher stays; the ACP/Node transport is **dropped** — btw's agent transport becomes the Copilot SDK, per decision 2/5). Submission context assembly, agent registry (✅ Go), snapshot reproduce (✅).

**Model selection (net-new UX)** — a model picker in the ask/btw UI: workspace-default model persisted in SQLite, per-conversation override, model list fetched from `GET /api/ask/models` (SDK-enumerated, cached, with a sensible default when offline). Selection must survive restarts and be reflected in conversation metadata.

**Platform** — queue control plane + Queue Home read models (✅), daemon lifecycle/singleton/port file, capability→httponly session exchange (✅ bootstrap auth), secret store (macOS Keychain via `security`, Linux libsecret via `secret-tool`, encrypted-file fallback), GitHub auth (below), remote PR submission with github.com-restricted tokens, federation, wsHub broadcast semantics (message shapes unchanged — the UI must not need edits beyond the API base), path-traversal guards on all file-serving routes.

**GitHub auth (rebuilt in Go, `internal/githubauth`)** — primary: **loopback OAuth** — the CLI/desktop opens the browser to GitHub's authorize URL; the daemon (or a transient listener for CLI-only use) receives the callback on `127.0.0.1:<ephemeral>` with `state` verification; fallback: **device flow** (print user code + verification URL; poll). Triggers: `localreview auth login`, first Copilot SDK use, Electron first-run. Tokens go in the secret store, never in SQLite or logs. `localreview auth status|logout` complete the surface. The Electron app reuses the daemon's flow — no separate auth code path.

## Build system (Bazel)

- `MODULE.bazel` already pins `rules_go` + Go SDK; add `rules_js`/`aspect_rules_js` (or a hermetic genrule with a pinned Node toolchain) for the Vite UI build, and wire `go:embed` of the UI output into `//cmd/localreviewd`.
- Targets: `//cmd/localreview`, `//cmd/localreviewd`, `//internal/...` (all with tests), `//ui:dist`, `//desktop:app` (electron-builder invoked via genrule; may be CI-only if Bazel-hermetic Electron packaging fights back — pragmatism over purity here), `//release:archives` producing `localreview_<os>_<arch>.tar.gz` for darwin/linux × arm64/amd64 (Windows explicitly deferred).
- `bazel test //...` is the gate; keep `go test ./...` working for inner-loop speed.
- CI: on tag push, build all platform archives + checksums + the Electron DMG/AppImage, attach to a GitHub Release; publish `install.sh` (detect platform, download, verify checksum, install to `~/.local/bin`).

## Execution plan (big-bang)

**Phase 0 — freeze.** Tag the TS server state (`ts-final`). From here `src/` receives no feature work. Record a behavioral snapshot: for every route in the inventory, capture request/response fixtures from the running TS server against a fixture workspace (script this; the fixtures become the parity test corpus).

**Phase 1 — port to parity.** Build out `internal/daemon` until every inventory item passes its parity fixture. Suggested internal order (dependency-driven, not milestoned for release): file/blob/expansion routes → full-file projection → comments-adjacent remainder → sessions/export/send-to-cmux → wsHub + file watching → Copilot ask/btw + model selection → GitHub auth + secret store → remote PR/federation → CLI subcommands. Every port lands with Go unit tests plus its parity fixtures green.

**Phase 2 — cutover.** `localreview open` serves everything from Go; npm `bin` entries repointed to error with a migration message; Electron shell built against the Go sidecar; release pipeline produces installable artifacts.

**Phase 3 — validation & fix loop (computer-use).** Drive the *real* UI with the Claude-in-Chrome computer-use tools against both the local web version and the Electron app, executing a scripted checklist derived from `SPEC.md`'s acceptance criteria plus the new surfaces:

1. Multi-repo workspace: open a fixture dir with ≥2 nested repos (staged/unstaged/untracked mix); verify repo tree, diffs, unified/split/full-file views, gates, context expansion.
2. Comments: place (including on expanded/full-file lines), edit, resolve/tombstone, restart daemon, verify persistence; export prompt and verify repo-relative paths.
3. Ask/btw: run a conversation through the Copilot SDK, verify streaming renders, cancel mid-stream, resume; switch models mid-conversation and verify `GET /api/ask/models` contents appear in the picker and the choice persists.
4. Queue: submit via `localreview submit`, open from Queue Home, complete/requeue.
5. Auth: fresh-profile loopback OAuth end-to-end in the browser; device-flow path in a terminal-only run; token lands in keychain; logout revokes local state.
6. Electron: cold start (sidecar spawn → window), all of the above smoke-level, quit kills the sidecar, second launch reuses state.
7. Memory: record daemon RSS at idle and after the full checklist; must meet the < 50 MB idle target and show no growth trend across repeated checklist runs.

Each failure gets a minimal repro + fix + re-run; the loop exits when the checklist passes twice consecutively on a clean profile.

**Phase 4 — deletion.** Remove `src/`, `package.json` server/CLI deps, bun test infra (UI build devDeps stay); `vendor/cmux-hub` TS modules that were only server-side are deleted once their Go ports are green. Update `README.md`, `SPEC.md` stack section, and `HANDOFF.md` (or retire it).

## Non-goals

- Hosted/multi-user web deployment (keep `localreview remote daemon` a loopback-shaped Go binary so this stays viable later, but build nothing for it).
- Windows support, Homebrew, npm distribution, `go install` guarantees.
- ACP/Node agent adapters (claude-code-acp et al.) — removed, not ported. If demand returns, they'd re-enter as optional external subprocesses without touching the core.
- Porting the React UI to anything else, or restyling it. The UI changes only where the API base or model picker requires.
- Rewriting `vendor/difit` build tooling beyond making it runnable under Bazel.

## Risks & traps

- **Copilot SDK auth coupling**: SDK sessions need the GitHub/Copilot token plumbed from the secret store; validate early (Phase 1, not Phase 3) that headless token → SDK session → streamed completion works, since model selection and ask/btw all sit on it.
- **WebSocket parity**: the UI's ws message shapes are the de facto contract; diff any Go-emitted frame against TS fixtures byte-for-byte where feasible.
- **SQLite migration**: TS-era DBs must open cleanly; test against copies of real `~/.local/share/cmux-localreview` data, not just fixtures.
- **Electron sidecar lifecycle**: orphaned daemons on crash — use a parent-pid watchdog in `localreviewd` when spawned with `--parent-pid`.
- **Bazel + Vite hermeticity**: if `rules_js` fights the vendored difit setup, fall back to a pinned-Node genrule; do not let build purity block Phase 1.
- **Big-bang dark period**: the parity fixture corpus from Phase 0 is the safety net — invest in it before porting, or Phase 3 becomes archaeology.
