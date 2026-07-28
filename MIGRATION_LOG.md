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
- Fixed the Go developer build contract by generating a real Go module vendor
  tree alongside the existing `vendor/difit` and `vendor/cmux-hub` UI sources;
  plain `go build ./...` and `go test ./...` now work without a hidden
  `GOFLAGS=-mod=mod` requirement.

Validated:

- `bun scripts/capture-parity-fixtures.ts` generated the corpus cleanly.
- `go build ./...`, `go test ./...`, and `bazel test //...` passed after the
  capture.

Decided:

- The `vendor/` directory is shared intentionally: Go dependencies use
  `vendor/modules.txt`, while the existing UI remains under `vendor/difit` and
  `vendor/cmux-hub`. `go mod vendor` must preserve those two directories.

Next:

- Expand the Phase 0 corpus to the remaining frozen routes (full-file/context,
  comment imports, `/ask` conversation streaming/cancel, `/btw`, remote PR,
  and authenticated GitHub flows) using hermetic fakes where external network
  access would otherwise be required. Then add fixture-driven Go parity tests
  before marking Phase 0 complete.
