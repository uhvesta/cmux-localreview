# Native migration journal

This is the durable state record required by `AGENT_LOOP.md`. Entries are
deliberately factual: a green unit test does not imply end-to-end parity.

## 2026-07-28 — Native `/ask` review-round history parity

- Made the frozen `ask_conversation_fresh` and
  `ask_conversation_history` lifecycle rows executable in the native parity
  matrix. Starting a fresh review-round conversation archives the prior
  durable conversation; `?history=true` is a compatibility alias for the
  native `?includeArchived=true` read.
- The compatibility read is side-effect free: it neither resumes a historical
  conversation nor creates/replays a Copilot SDK turn. This is covered by
  `TestAskFreshArchivesPriorRoundAndHistoryReadNeverResumesIt` and the frozen
  matrix replay.
- This reduces explicit matrix debt only. It is not a Phase-4 completion
  claim; other exceptions and both required computer-use passes remain open.

## 2026-07-28 — Native `/ask` model-picker parity

- Made frozen `ask_conversation_model` and `ask_conversation_settings`
  requests executable against the native daemon. The matrix adapter checks the
  historical request/status/content-type envelope while requiring the stronger
  native semantic: a model-only change preserves explicit reasoning effort and
  context-tier settings instead of silently clearing them.
- The adapter is intentionally narrow and does not modify the frozen corpus.
  It records a reviewed contract divergence that improves the native picker;
  it is not a generic exception or a Phase-4 completion claim.

## 2026-07-28 — Native `/ask` SDK model-list parity

- Made the frozen `ask_models` row executable using the existing hermetic
  official-SDK facade. Model discovery remains client-level and never opens a
  conversation or sends a prompt.
- The native model response now explicitly lists `low`, `medium`, `high`, and
  `xhigh` thinking levels for its supported picker contract, preventing a
  successful SDK refresh from hiding the UI's Thinking control.

## 2026-07-28 — Native durable-comment read parity

- Made frozen `repo_comments_empty` and `repo_comments_saved` rows executable
  through a narrow matrix adapter. The frozen server returned an HTML 404 for
  both; the native daemon must return a capability-protected JSON collection.
- The adapter validates a real empty collection, then the formal durable
  thread produced by the frozen legacy POST shape (`fixture-comment`, file,
  position, message, channel, and attached status). It does not preserve or
  waive the obsolete 404 behavior.

## 2026-07-28 — Deterministic native queue-watch parity

- Made frozen `queue_watch_enable` and `queue_watch_disable` rows executable
  without wall-clock sleeps. `queueWatchPoll` is a nil-by-default test seam;
  production keeps the real Git polling implementation, while the matrix uses
  a no-op poll to observe synchronous lifecycle behavior.
- The replay checks status/content, canonical workspace path, the persisted
  enabled/disabled row and interval clamp, in-memory watcher registration, and
  cancellation/removal. No synthetic tick or queue snapshot is needed to
  prove the control-plane lifecycle.

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

## 2026-07-28 — Native parity, distribution, and validation hardening

Done:

- Wired the production daemon's lazy Copilot SDK factory. It uses only the
  dedicated daemon-owned Copilot credential, isolates SDK state below the data
  directory, starts only after an explicit models/turn action, and rejects
  unsafe workspace escape, mutation, shell, network, MCP, and memory requests.
  An unconfigured capability reports a concrete unavailable state; it does not
  fabricate a model list or delivery success.
- Added a separate local-only GitHub PR opening path in Queue Home and
  `localreview open --pr <URL>`. It mirrors a PR for diff and `/ask` without a
  queue row or a publication path.
- Completed dedicated GitHub App publishing for remote items: read/write
  credentials remain separate, a fresh immutable-head check rejects stale PRs,
  and a failed publication leaves the local decision untouched. Non-blocking
  COMMENT publication has the same safety boundary.
- Added CLI daemon auto-start with owner-only discovery/health verification,
  sibling-release-binary resolution, and a startup lock. A copied release-style
  pair of binaries was manually exercised with `open --no-open`, `daemon
  status`, and `daemon stop`.
- Ported actual SSH federation transport: loopback-only OpenSSH forwarding,
  daemon secret-store credentials, lazy remote queue/workspace aggregation,
  retry/error/disconnect handling, and hermetic fake-tunnel coverage.
- Closed native comment parity regressions: optimistic revision/channel
  preservation and durable anchor revalidation now survive change, restore, and
  restart without silently relocating a thread.
- Fixed Queue Home's local submission to call the immutable snapshot hook, not
  the bare queue-row endpoint.

Validated:

- Clean local-browser run against a fresh daemon: capability exchange; Queue
  Home local submission; immutable snapshot creation; explicit workspace open;
  rendered split diff; **Complete**; and **Requeue**. The resulting decision
  history and queue count matched the rendered state.
- Packaged macOS Electron run: codesign verification, fresh sidecar and
  health, immutable snapshot metadata, app/sidecar termination, relaunch, and
  restored durable queue state. Idle daemon RSS was 20.5–24.7 MB, under the
  50 MB target.
- `go test ./...`, `bazel test //...` (18 test targets),
  `scripts/verify-go-parity-matrix.sh`,
  `scripts/verify-parity-fixtures.sh`, and `bazel build //desktop:app` passed
  after the integrated changes.

Not yet claimed:

- Phase 3 has not yet passed twice on clean profiles. The remaining required
  real-service checks are GitHub App loopback and device authorization,
  authenticated Copilot model enumeration/stream/cancel/model-switch, `/btw`,
  and a fully click-driven Electron renderer pass. A native confirmation dialog
  interrupted the automation attempt to remove the disposable queue item, so
  removal is not claimed from that pass.
- Phase 4 deletion and a tagged GitHub Release remain intentionally open until
  those two complete Phase-3 passes and the full parity audit are complete.

## 2026-07-28 — Release-install and frozen-parity expansion

Done:

- Made the frozen HTTP/remote-PR fixture corpus release-verifiable without
  executing Bun, Node, `src/`, or the old capture harness. The native gate
  verifies corpus provenance, SHA-256 integrity, uniqueness, and Bazel
  runfiles before matrix replay.
- Expanded exact native fixture replay from 26 to 53 of 81 frozen contracts,
  covering the queue lifecycle and immutable materialization, agent lifecycle,
  revisions, durable `/ask` CRUD/inline reuse/question-set CRUD, and legacy
  comment-save adaptation. Every remaining exclusion is explicit in the
  matrix, including retired ACP behavior and deliberate durable-model-setting
  semantics.
- Fixed release installs to create the same native historical aliases as the
  source installer. The release installer now supports a deterministic staging
  base for archive validation; it never installs Node/Bun launchers.

Validated:

- `scripts/verify-frozen-parity-corpus.sh`,
  `scripts/verify-go-parity-matrix.sh`, and
  `scripts/verify-parity-fixtures.sh` passed.
- `scripts/verify-native-installer.sh` built a Darwin archive, verified its
  checksum through a static curl fixture, installed both binaries plus all
  aliases into a disposable prefix, and exercised daemon start/status/stop.
  The source installer was also run twice against a disposable project and
  preserved user instructions while installing managed Copilot skills.
- `go test ./...`, `bazel test //...` (18 test targets), and a fresh
  `bazel build //desktop:app` archive inspection passed.

Not yet claimed:

- Exact replay is stronger but not yet universal; the remaining rows must be
  either replayed or accepted as documented migration substitutions in the
  final Phase-4 absence audit. Real authenticated service flows and the two
  clean click-driven Phase-3 passes remain required before deletion/tagging.

## 2026-07-28 — Copilot fixture browser regression pass

Done:

- Corrected durable `/ask` SSE URL resolution after a reviewer selects a
  repository. Global daemon streams no longer inherit the repo-scoped API base.
- Made early cancellation terminal and visible: the stream connection wait is
  released immediately, partial text is preserved, and the transcript records
  that Copilot was cancelled rather than looking like it silently stopped.
- Reset an idle SDK session when the reviewer changes model, thinking, or
  context settings, so the next turn actually uses the displayed choice.
- Switched GitHub App authorization to device OAuth by default. The optional
  browser flow now uses only the documented, stable
  `http://127.0.0.1:8787/oauth/callback` registration and never assumes that
  GitHub accepts an arbitrary loopback port.

Validated:

- Fresh browser fixture daemon: Queue Home rendered the global local/remote
  queues and the dedicated GitHub App device-flow selector; a local immutable
  snapshot was submitted and shown as a queue item.
- Browser fixture review: chose Claude Sonnet 4.6, observed an explicit
  `thinking…` streaming state, then received an answer identifying
  `claude-sonnet-4.6`; cancelled a second live turn and observed the durable
  cancelled marker. A fresh browser page restored the transcript, selected
  model, and cancelled turn without resending either prompt.
- `go test ./internal/askruntime ./internal/daemon ./internal/githubauth
  ./cmd/localreview`, TypeScript checking, focused UI tests, the isolated
  Copilot fixture acceptance gate, and the fixture Bazel build passed.

Not yet claimed:

- The fixture validates deterministic SDK protocol behavior, not a real
  GitHub/Copilot authorization grant. Actual device authorization, real SDK
  model enumeration, `/btw`, inline question click-through, Electron renderer
  click-through, and a second clean full Phase-3 run remain release gates.

## 2026-07-28 — Native `/btw` and expanded `/ask` parity pass

Done:

- Removed the retired ACP/terminal payload from the reviewer `/btw` panel and
  inline file action. Both now send the native `transport: "copilot"` request,
  label the conversation as a private Copilot SDK side chat, surface pending
  delivery, and retain a polling fallback for a completed response.
- Added deterministic fixture coverage for inline `/ask` creation, anchored
  context, event streaming, reopening the same location without replay, and
  restart persistence.
- Extended frozen native parity replay to cover `/ask` fresh/history, model
  discovery, model/settings updates, and idle cancellation. Native picker
  behavior intentionally preserves explicit thinking/context choices instead
  of silently clearing them.

Validated:

- Live browser fixture review after rebuilding the embedded UI: opened `/btw`,
  asked about `review.go`, observed a distinct streaming Copilot SDK response,
  then observed its completion. The UI did not expose ACP or terminal target
  controls.
- The self-contained `/btw` component smoke, event-source URL tests,
  TypeScript check, isolated Copilot fixture acceptance, and native frozen
  parity matrix all passed.

Not yet claimed:

- This is still fixture-backed validation. A real authenticated SDK session,
  all inline click targets, and the renderer-level Electron checklist remain
  required in each of the two clean Phase-3 passes.

## 2026-07-28 — Restored loopback OAuth as the primary native flow

Done:

- Restored the migration-spec auth contract after auditing the implementation:
  browser OAuth through the stable, registered
  `http://127.0.0.1:8787/oauth/callback` is again the default for CLI and
  Queue Home. Device OAuth is explicitly retained for SSH/headless use.
- Kept the security hardening that motivated the earlier change: the callback
  is fixed and documented, client secrets are required only in the daemon's
  secret store, and neither secrets nor tokens reach browser JavaScript.

Validated:

- GitHub auth API, daemon HTTP contract, CLI tests, TypeScript check, and the
  native `/btw` component smoke pass. The daemon contract verifies default
  loopback start plus explicit device fallback without exposing a client secret.

Not yet claimed:

- A real OAuth App registration and user authorization have not been created
  during this fixture validation; that live authorization remains a Phase-3
  workflow check.

## 2026-07-28 — Recoverable reviewer failures and native PKCE setup

Done:

- Reviewer diff/workspace failures now keep the saved review intact and offer
  in-place retry plus Queue Home navigation. `/ask`, inline `/ask`, and `/btw`
  retain a failed prompt, make its error accessible, offer an explicit retry,
  and reject duplicate sends while a turn is pending.
- Converted the dedicated GitHub OAuth client setup to authorization-code PKCE.
  Loopback browser sign-in now needs only a public client ID and the registered
  callback `http://127.0.0.1:8787/oauth/callback`; the product no longer
  accepts, stores, or ships an OAuth client secret. Issued access tokens remain
  daemon-only in the OS secret store.

Validated:

- Focused reviewer, Queue Home, `/btw`, Go auth/CLI tests, complete Go suite,
  TypeScript check, fixture Copilot acceptance, and frozen parity matrix pass.

Not yet claimed:

- A signed-in GitHub account still needs the interactive one-time registration
  and browser authorization pass before this is counted as a complete live
  Phase-3 OAuth validation.

## 2026-07-28 — Fresh browser recovery pass

Validated in the rebuilt E2E fixture through the browser:

- Started unauthenticated at Queue Home and saw the recoverable local-daemon
  connection form plus exact daemon recovery command; entered the fixture's
  local capability and loaded the preserved queue item.
- Opened that item from Queue Home and confirmed its workspace path, file
  hierarchy, and diff render rather than a blank/error screen.
- Asked a `/btw` question, observed the distinct `Copilot SDK` streaming state,
  then observed the completed fixture response. The prompt was sent once and
  no ACP/terminal target selector appeared.

Not yet claimed:

- This is a browser fixture pass, not real GitHub consent or a real Copilot
  account. The first live account pass requires a user-visible GitHub consent
  page and cannot safely be fabricated by the fixture.

## 2026-07-28 — Live self-hosted OAuth device-flow pass

Validated against a newly created, operator-owned GitHub OAuth App (PKCE
loopback callback registered and Device Flow enabled), rather than `gh`, a
PAT, or a shared token:

- Started the native daemon from a fresh temporary data directory, configured
  only the OAuth App's public client ID, and completed GitHub's real device
  activation and consent screens as `@uhvesta`.
- The native auth poll reported the account connected, and the issued access
  token was present only under the daemon's macOS Keychain service/account.
- Logout removed the local Keychain credential and returned the capability to
  an unauthenticated idle state. GitHub-side consent remains independently
  revocable from GitHub, as documented.
- Rebuilt the macOS Electron archive after reclaiming only the rebuildable
  Bazel cache; codesign verification passed and all four native release
  archives passed structural/checksum validation.

Not yet claimed:

- The in-app automation surface blocks the final `/login/oauth/authorize`
  callback redirect, so the real browser-loopback click-through still needs a
  clean desktop-browser pass. Device flow, which is the SSH/headless fallback,
  is now live-validated end to end.

## 2026-07-28 — Release archive structural gate

Done:

- Added `scripts/verify-release-archives.sh`, a native-only structural gate
  for all four promised v1 archives: darwin/linux × arm64/amd64. It builds
  `//release:archives` plus checksums, verifies both Go binaries are present
  under each platform-specific archive root, and verifies every archive digest
  against the four-line release manifest without executing a foreign binary.

Validated:

- `bash scripts/verify-release-archives.sh` passed locally.

Not yet claimed:

- This validates build payload structure, not platform execution. GitHub
  Actions still has to install/smoke the matching archive on each OS, and the
  tagged GitHub Release remains an exit-condition gate.
