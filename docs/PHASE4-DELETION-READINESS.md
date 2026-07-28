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
| `testdata/parity/ts-final/remote-pr.json` | `internal/daemon/parity_corpus_test.go` | Frozen remote lifecycle provenance/digest evidence; extend to full native remote replay before release. |
| `internal/daemon/parity_corpus_test.go` | `go test`, `bazel test` | Pins hashes and validates corpus structure without a TS runtime. |
| `testdata/parity/ts-final/BUILD.bazel` | `//internal/daemon:daemon_test` | Makes both JSON captures explicit Bazel runfiles. |
| `scripts/verify-frozen-parity-corpus.sh` | CI/release checklist | Native-only integrity gate; retained after Phase 4. |

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

1. Resolve every exception in `parityMatrix`. The matrix currently treats an
   exception as visible debt, not equivalent parity. Add a normalized fixture
   or a direct native replay for each changed contract.
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
6. Re-run `go test ./...`, `bazel test //...`, the native release gate, UI
   build, platform archive smoke tests, and both full browser/Electron
   computer-use passes. Record command output and screenshots in
   `MIGRATION_LOG.md`.

## Current stop line

The native corpus gate is ready. The durable `/ask` fresh/history rows now
replay through the matrix: `history=true` is a read-only compatibility alias
for `includeArchived=true`, and a fresh round archives prior conversations
without opening or replaying a Copilot session. Deletion is **not** ready: the
matrix still contains other explicit compatibility exceptions and the required
two complete Phase-3 browser/Electron passes have not been recorded. Do not
remove `src/` or package dependencies based only on this preparation work.
