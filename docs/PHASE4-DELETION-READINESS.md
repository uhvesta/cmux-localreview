# Phase-4 deletion readiness

This document is the safe hand-off for deleting the frozen TypeScript server.
It deliberately distinguishes the **archived parity corpus**, which remains in
the Go release source tree, from the **capture implementation**, which is
deleted with `src/`. Passing a unit test is not permission to remove either
runtime until every row below is closed.

## Native release gate

```sh
bash scripts/verify-frozen-parity-corpus.sh
bash scripts/verify-go-parity-matrix.sh
bash scripts/verify-native-runtime-boundary.sh
bash scripts/verify-release-archives.sh
```

These commands use only Git (when available), Go, Bazel, and the checked-in
JSON. They do not run Bun/Node, call a capture script, import `src/`, or make
network calls. The first command checks both corpus files against pinned
SHA-256 digests and validates their provenance. The second makes that check a
precondition of native HTTP replay. The third checks that the shipped Go
daemon/CLI surface has no Bun, Node, frozen-server, or ACP-adapter runtime
dependency; React/Electron build-time JavaScript is deliberately out of scope.

The expected corpus is intentionally retained:

| Archived artifact | Native consumer | Why it survives deletion |
| --- | --- | --- |
| `testdata/parity/ts-final/http.json` | `internal/daemon/parity_matrix_test.go` | Frozen HTTP oracle for the native daemon. |
| `testdata/parity/ts-final/remote-pr.json` | `internal/daemon/remote_parity_test.go` | Frozen remote lifecycle source plus direct native auth → queue → status → open → cleanup replay against a disposable Git remote. |
| `internal/daemon/parity_corpus_test.go` | `go test`, `bazel test` | Pins hashes and validates corpus structure without a TS runtime. |
| `testdata/parity/ts-final/BUILD.bazel` | `//internal/daemon:daemon_test` | Makes both JSON captures explicit Bazel runfiles. |
| `scripts/verify-frozen-parity-corpus.sh` | CI/release checklist | Native-only integrity gate; retained after Phase 4. |

`verify-release-archives.sh` also rejects an archive containing anything other
than the two promised Go executables. It is intentionally not a substitute for
executing each foreign-architecture binary on its native release runner. The
tag workflow closes that gap with `native-archive-smoke`: macOS arm64/amd64 and
Linux amd64/arm64 runners download the exact uploaded archive and checksum
manifest, exercise `install.sh` without network access, then run the installed
CLI and daemon. It also checks that `install.sh` is itself an uploaded release
asset before publication.

For Electron, `npm --prefix desktop run verify:package -- <unpacked-app>` is
the matching package-boundary check: it validates `app.asar`, the bundled
`localreviewd`, and sidecar lifecycle using the packaged executable. A real
renderer interaction pass remains a Phase-3 requirement because a display-free
package check cannot prove a BrowserWindow rendered or accepted input.

The desktop artifact job must stage the same Vite output embedded in the Go
sidecar before it builds that binary. A healthy sidecar alone does not prove
the renderer is current: compare a clean-profile packaged window's visible
Queue Home copy/controls with the current UI before recording an Electron pass.
The credential-free minimum smoke is Queue Home local submission → queued card
→ **Open workspace** → actual changed-file diff. Run it against an isolated
Electron user-data directory and daemon data directory; it proves no queue or
OAuth state leaked from a developer's normal desktop profile, but it is not a
replacement for the authenticated `/ask` and GitHub-review Phase-3 cases.

## Frozen TypeScript dependencies to remove

| Path / dependency | Current role | Phase-4 action | Proof required first |
| --- | --- | --- | --- |
| `src/` | The frozen HTTP capture scripts import its server, auth, queue, and review modules. | Delete. | Full native parity audit and two clean Phase-3 passes. |
| `scripts/capture-parity-fixtures.ts` | Creates `http.json` by starting the frozen server. | Delete after the final reviewed capture. | Native-only gate passes with this file absent. |
| `scripts/capture-remote-parity-fixtures.ts` | Creates `remote-pr.json` by importing frozen remote/auth modules. | Delete after the final reviewed capture. | Native-only gate passes with this file absent. |
| `scripts/verify-parity-fixtures.sh` | Runs both capture scripts through Bun. | Delete. | `verify-frozen-parity-corpus.sh` and matrix pass in a checkout with no Bun executable. |
| Bun server/test dependencies in `package.json`, `bun.lock` | Frozen daemon, CLI, ACP adapters, and TS test support. | Prune, retaining only UI build dependencies. | `//ui:dist` plus Electron package build succeeds from a clean install. |
| Server-only `vendor/cmux-hub/*.ts` | Imported only by frozen server behavior. | Delete with `src/`. | Repository import audit proves the React UI does not import them. |
| `scripts/legacy-bin.mjs` and old `package.json` bins | Temporary compatibility redirects. | Delete old bins; preserve the Go installer/wrappers. | `localreview` mapping has been smoke-tested from release archives. |

Historical references in `MIGRATION_LOG.md`, `NATIVE_MIGRATION_SPEC.md`, and
the Git tag `ts-final` are documentation/source-control evidence, not runtime
dependencies. They may continue to mention TypeScript after Phase 4.

## Required deletion change sequence

1. Resolve every non-executable exception in `parityMatrix`. A changed native
   contract is acceptable only when its matrix row executes a normalized
   fixture assertion or a direct native replay; prose alone is not parity.
2. Run the native release gate from a clean worktree and from an unpacked
   release-source archive with `bun` absent from `PATH`.
3. Copy no files: retain the already checked-in corpus plus its Go/Bazel
   consumers exactly as verified. Delete only the three capture commands after
   the final reviewed capture.
4. Delete `src/`, server-only vendor modules, old bins, and server/CLI/Bun test
   dependencies in one reviewable commit. Keep React/Vite build inputs and the
   minimal Electron shell.
5. Add and run an absence gate equivalent to:

   ```sh
   ! rg -n 'src/server|bun +src/|capture-parity-fixtures|capture-remote-parity-fixtures|@zed-industries/.+-acp|@github/copilot-sdk' \
     --glob '!docs/**' --glob '!MIGRATION_LOG.md' --glob '!NATIVE_MIGRATION_SPEC.md' .
   ```

   Review any remaining match individually; do not blanket-ignore source
   files. The Go Copilot SDK remains allowed through `go.mod`.

   The checked-in gate is `scripts/verify-phase4-absence.sh`. Before deletion,
   run it with `--predelete`: that verifies the explicit legacy inventory and
   Go-native corpus consumers without making a false absence claim. After the
   reviewed deletion commit, run the strict form with no argument; it rejects
   retained runtime paths, server-only package dependencies, and non-exempt
   runtime/build references. See
   [`PHASE4-LEGACY-INVENTORY.md`](PHASE4-LEGACY-INVENTORY.md) for the exact
   deletion/preservation split.
6. Re-run `go test ./...`, `bazel test //...`, the native release gate, UI
   build, platform archive smoke tests, and both full browser/Electron
   computer-use passes. Record command output and screenshots in
   `MIGRATION_LOG.md`.

## Current stop line

The native corpus gate is ready. The durable `/ask` fresh/history rows now
replay through the matrix: `history=true` is a read-only compatibility alias
for `includeArchived=true`, and a fresh round archives prior conversations
without opening or replaying a Copilot session. The frozen model/settings rows
also replay with a deliberate native adapter: the historical route discarded
explicit thinking/context selections after a model change, while the native
route must preserve them. The SDK-backed model-list row also replays with a
hermetic SDK facade and advertises the four supported thinking levels to the
native picker. Legacy comment-read rows now replay as a narrow durable-comment
adapter: the historical HTML 404 is rejected in favor of a real JSON
collection, including the empty-to-saved formal-thread lifecycle. Queue-watch
enable/disable now replay using a no-op poll seam: the matrix verifies the
real persisted watcher row, registration, interval clamp, and cancellation
without sleeping for a wall-clock tick. Named question-set delivery now
replays as a native POST-202 plus durable EventSource handoff: both combined
and sequential turns must settle into the existing conversation, and a
subsequent transcript read proves no question was replayed. Ordinary `/ask`
message delivery is covered by the same native handoff: the frozen legacy
request retains its file/line selection, the deterministic fixture turn
settles once, and reopening the transcript cannot resubmit it. Queue reproduction
now replays as a safe snapshot materialization plan and explicitly rejects
retired ACP continuation commands in favor of a fresh SDK-native `/ask`
handoff. Deletion is **not** ready: the matrix now executes every frozen HTTP
row and the remote-PR lifecycle has a direct native replay, but the required
two complete Phase-3 browser/Electron passes have not been recorded. Do not
remove `src/` or package dependencies based only on this preparation work.
