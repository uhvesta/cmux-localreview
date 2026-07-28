# Native migration journal

This is the durable state record required by `AGENT_LOOP.md`. Entries are
deliberately factual: a green unit test does not imply end-to-end parity.

## 2026-07-28 — Phase 0 started: frozen HTTP fixture corpus

Done:

- Added `NATIVE_MIGRATION_SPEC.md` and `AGENT_LOOP.md` to the repository as
  the authoritative migration contract and iteration protocol.
- Added `scripts/capture-parity-fixtures.ts`, which starts the frozen Bun
  daemon against a disposable multi-repository workspace (changed, untracked,
  and nested-repository inputs) and writes normalized request/response
  fixtures to `testdata/parity/ts-final/http.json`.
- Captured 53 deterministic contracts across bootstrap auth, workspace and
  reviewer routes, comments/session/UI state, `/ask` question-set storage,
  local queue lifecycle, agent routing, and federation lifecycle.
- Extended the corpus to 59 contracts with diff query variants, line counts,
  blobs, generated-file detection, full-file projection, and comment import.
- Extended it again to 68 contracts with a hermetic GitHub App device-flow
  configure/start/poll/status/logout sequence, browser-session exchange,
  read-only PR auth rejection, and `/btw` validation/list behavior. The
  fixture auth service uses an in-memory secret store and synthetic responses;
  no credential is captured or written to disk.
- Extended it to 80 contracts with the real `/ask` model picker,
  conversation/inline reuse, model/settings mutation, transcript/history,
  cancel, and combined/sequential question-set SSE frames. A disposable
  SDK-shaped session supplies deterministic deltas, so this captures the
  frozen router's actual HTTP and streaming shape without launching Copilot.
- The capture is repeatable: two consecutive runs produce byte-identical
  `http.json`; UUIDs, hashes, temporary paths, timestamps, and snapshot
  bundle names are normalized while response field presence is retained.
- Added a separate, repeatable remote-PR corpus using a disposable bare Git
  remote plus synthetic GitHub App/API responses. It covers successful remote
  queueing, retained snapshot metadata, cache/worktree status, opening, and
  safe cleanup without touching GitHub or the user's cache.
- Captured the WebSocket `diff-updated` frame after a real workspace file
  mutation. `scripts/verify-parity-fixtures.sh` now runs both local and remote
  capture programs twice and requires byte-identical output. This completes
  the Phase 0 frozen-contract baseline; Phase 1 ports must add focused Go
  fixture tests before claiming each route as complete.
- Fixed the Go developer build contract by generating a real Go module vendor
  tree alongside the existing `vendor/difit` and `vendor/cmux-hub` UI sources;
  plain `go build ./...` and `go test ./...` now work without a hidden
  `GOFLAGS=-mod=mod` requirement.

Validated:

- `bun scripts/capture-parity-fixtures.ts` generated the corpus cleanly.
- `go build ./...`, `go test ./...`, and `bazel test //...` passed after the
  capture.
- `bun test --path-ignore-patterns='**/vendor/**'` passed: 106 tests, 0
  failures. This is the last green frozen-TS suite before further Go parity
  work.

Decided:

- The `vendor/` directory is shared intentionally: Go dependencies use
  `vendor/modules.txt`, while the existing UI remains under `vendor/difit` and
  `vendor/cmux-hub`. `go mod vendor` must preserve those two directories.
- The frozen TypeScript contract is tagged `ts-final` and pushed. No future
  feature work should modify `src/`; only a validation-blocking bug fix may do
  so, followed by recapture of the affected fixture.

Next:

- Port the frozen surfaces in focused, independently testable Go increments;
  each increment must retain repeatable TypeScript fixture capture as a
  regression check. The remaining high-risk surfaces are `/ask` streaming,
  GitHub App auth, remote PR lifecycle, and the WebSocket/browser cutover.

## 2026-07-28 — Phase 1: reviewer-file projection slice

Done:

- Ported the reviewer line-count and generated-file-status routes to the Go
  daemon, including repository-path containment checks and working-tree/ref
  semantics matching the frozen TypeScript contract.
- Ported full-file gate projection, so a reviewer can reveal the opposite-side
  changed lines while retaining the same `afterLine`, hidden line range, and
  content structure as the frozen full-file response.
- Matched Go JSON responses to the frozen TypeScript media type
  (`application/json; charset=utf-8`).
- Added native endpoint coverage through a real loopback daemon with a nested
  Git repository plus focused generated-path/generated-header recognition
  cases.

Validated:

- `go test ./...` passed.
- `bazel test //...` passed.
- `bash scripts/verify-parity-fixtures.sh` passed twice internally and
  confirmed the frozen TypeScript corpus remains deterministic.

Not yet claimed:

- This is one reviewer-file surface, not reviewer E2E parity. Browser and
  Electron validation remain deferred until the Go HTTP route set is complete.

## 2026-07-28 — Phase 1/2 hardening increment

Done:

- Made the parity verification gate non-mutating. It now captures each frozen
  TypeScript corpus into two temporary files, checks them for repeatability,
  and checks the result against the committed oracle. Running the verifier can
  no longer silently replace `testdata/parity/ts-final`.
- Added Go reviewer support for portable comment imports, including anchored
  thread/reply validation, deterministic import IDs, reply deduplication,
  tombstone handling, and warnings for replies whose parent cannot be found.
- Added a dedicated `/ask` runtime seam with only the daemon-owned Copilot
  credential capability, safe model-result/SSE helpers, and no ambient CLI or
  `gh` credential path. It is not yet connected to the live message route.
- Added the minimal Electron sidecar shell. It starts `localreviewd`, waits
  for loopback health, exchanges the capability via a URL fragment, prevents
  renderer Node access, and opens external links outside Electron. The daemon
  now supports `--parent-pid`; Electron supplies it and the daemon exits if
  the parent disappears.
- Extended `docs/CLI-WORKFLOWS.md` with authoritative native daemon, desktop,
  queue, setup-skill, auth, reproduction, watcher, and current remote-status
  instructions.

Validated:

- `go test ./...`
- `bazel test //...` (15 targets)
- `bash scripts/verify-parity-fixtures.sh`
- `node --check desktop/main.mjs`

Not yet claimed:

- The Electron package has syntax coverage only; a packaged macOS/Linux
  launch is still required.
- `/ask` persistence and cancellation are present, but the live SDK streaming
  message route, authenticated model catalogue, and complete question-set send
  behavior still need end-to-end wiring and browser validation.
- Remote PR, federation, and the remaining frozen API surfaces are not yet
  production-complete in Go.

## 2026-07-28 — Phase 1 agent/queue increment

Done:

- Connected explicit `/ask` POST turns to the daemon-owned Go SDK runtime.
  Streaming deltas append only to their immutable pending assistant row and
  are published over a per-conversation SSE endpoint; reads/reloads remain
  database-only and cannot resend a prompt.
- Added SDK cancellation, immediate idempotent settlement, an inline prompt
  envelope containing workspace-relative path, repository path, side, range,
  and selected code, plus a test proving durable transcript/no replay.
- Ported the queue hook and durable watcher routes. They validate workspaces,
  fingerprint every repository, capture immutable snapshots only after a
  source change, resume after daemon restart, and supersede stale queue rounds.
- Expanded the native CLI with `daemon run|status|stop`, `submit`, remote
  daemon mode, GitHub App compatibility flow commands, and
  `reproduce --copilot`. The latter creates a fresh SDK `/ask` setup only;
  it explicitly does not resume historic ACP/agent state.

Validated:

- `go test ./...`
- `bazel test //...` (15 targets)
- `bash scripts/verify-parity-fixtures.sh`
- A local daemon lifecycle smoke: status verified matching discovery/health
  PID, then stop terminated the verified daemon and removed its discovery file.

Not yet claimed:

- The message transport has deterministic injected-runtime coverage but needs
  a real GitHub OAuth credential and SDK completion in the browser validation
  loop.
- `/btw`, remote PR lifecycle, federation transport, complete UI wiring, and
  packaged Electron checks remain unfinished.

## 2026-07-28 — Phase 3 browser pass 1 (native web daemon)

Validated manually in the local browser against a fresh `localreviewd` and a
disposable Git workspace:

- Opened Queue Home with the daemon capability in the URL fragment. The UI
  exchanged it for its local browser session; no manual-token recovery panel
  appeared.
- Submitted a local snapshot through Queue Home with an absolute workspace
  path and stable topic, then opened that workspace explicitly from the queue.
- Confirmed the actual split diff rendered the staged base and uncommitted
  target content.
- Changed a tracked file while the reviewer was open, observed **Changes
  detected — click to reload**, clicked it, and confirmed the rendered target
  line updated from `const after = 2` to `const after = 3`.

Not yet claimed:

- This is the first of the two required clean Phase-3 passes. It covers the
  web Queue Home/reviewer/watch loop only; comments, `/ask` with real OAuth,
  `/btw`, queue decisions, remote PR UI, device flow, Electron, restart, and
  memory checks remain in the validation matrix.

## 2026-07-28 — Phase 3 restart/comment follow-up

Validated manually in the local browser after a real daemon stop and restart:

- A direct `/review?queueItem=…` URL with a new daemon browser capability
  restored the persisted active workspace and the same durable review session;
  it no longer failed with `Failed to fetch workspace repos: 404`.
- The previously created inline comment remained attached to `main.go:2` after
  restart.
- The comment's two-step **Resolve thread → Resolve** flow removed it from the
  active reviewer. The native tombstone route keeps it from reappearing on a
  reload/import.

Implemented alongside this validation:

- Native workspace restoration starts the diff watcher without creating a new
  session, preserving comments and `/ask` identity across restarts.
- Durable workspace-level `/ask` model/reasoning/context defaults, explicit
  SDK model-list states, loopback OAuth UI support, and hardened Electron/tag
  release packaging.

Not yet claimed:

- This follow-up does not count as a full second Phase-3 clean run. The
  remaining explicit flows include real Copilot completion/model switching,
  question sets, `/btw`, queue decisions, remote PR read-only UI, device auth,
  Electron launch/quit, and RSS measurements.
