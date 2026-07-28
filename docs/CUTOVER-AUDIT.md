# Native cutover audit

This is the Phase-2/Phase-4 deletion checklist. It records dependencies found
by repository search, rather than treating green Go tests as proof that the
TypeScript control plane can be removed.

For the exact frozen-corpus dependency graph and deletion sequence, see
[`PHASE4-DELETION-READINESS.md`](PHASE4-DELETION-READINESS.md).

## Installed runtime boundary — now enforced

- `package.json` historical bins point only at `scripts/legacy-bin.mjs`. That
  script emits a migration command and exits 64; it never imports `src/`.
- `scripts/localreview-install.sh` builds and installs `localreview` and
  `localreviewd` from Bazel. Its compatibility command wrappers `exec` the Go
  CLI, never Bun.
- macOS/Linux setup and GitHub App helpers dispatch to `localreview`; the old
  remote-tunnel helper errors instead of silently starting TS.
- `//ui:dist` produces a portable Vite artifact. Node/Bun remain build-time
  inputs only; `localreviewd` continues to embed static files with `go:embed`.

## Deliberately retained until Phase 4

| Retained path | Why it remains | Required proof before deletion |
| --- | --- | --- |
| `src/` | Frozen TS parity oracle | Every fixture route has native parity coverage; two clean Phase-3 passes. |
| `scripts/capture-*.ts` | Pre-Phase-4 oracle capture | `scripts/verify-frozen-parity-corpus.sh` proves the checked-in corpus without Bun (now landed); delete capture scripts only after the final comparison and corpus archival review. |
| `package.json` + `bun.lock` | React/Vite build inputs | Keep UI dependencies only; remove server/CLI, ACP, and Bun-test dependencies. |
| `vendor/cmux-hub/*.ts` | TS oracle imports them | Confirm no native route or UI module needs them, then delete server-only modules with `src/`. |
| legacy docs (`README.md`, `SETUP.md`, `FAQ.md`, `HANDOFF.md`) | Historical setup material | Rewrite/retire after the native Phase-3 checklist is complete. |

## Remaining hard gates

1. `bazel test //...`, `go test ./...`, fixture verification, and `bazel build
   //ui:dist` must be green from a clean checkout.
2. Produce all four native archives and both Electron artifacts from a tag;
   install and smoke-test each supported platform archive.
3. Complete the documented browser and Electron Phase-3 flows twice in a row,
   including auth, model switching, stream/cancel, restart, queue decisions,
   remote lifecycle, and idle RSS measurement.
4. Search the final tree for runtime paths into `src/`, `bun src/`, ACP Node
   adapters, and server-only `vendor/cmux-hub` imports. Each must be removed,
   not redirected, before Phase 4 is claimed.

Until those proofs exist, this document is a cutover audit—not a completion
claim.

## Phase-4 deletion readiness (native-only parity)

The frozen HTTP and remote-PR captures are independently release-verifiable
without a TypeScript runtime:

```sh
bash scripts/verify-frozen-parity-corpus.sh
bash scripts/verify-go-parity-matrix.sh
```

`verify-frozen-parity-corpus.sh` checks provenance, fixture uniqueness, and
pinned SHA-256 digests for both JSON files in Go and Bazel. It rejects a dirty
corpus in a Git checkout and never launches Bun/Node or imports `src/`.

Before deleting paths, complete this checklist in one reviewed change:

1. Run the two native-only commands above from a clean checkout and from the
   release archive staging directory.
2. Resolve every explicit non-executable row in
   `internal/daemon/parity_matrix_test.go`; a reason is debt, not Phase-4
   proof. Add normalized native fixtures where exact historical JSON is no
   longer the contract.
3. Move the two JSON captures and their SHA pins into the release source tree
   unchanged; delete `scripts/capture-parity-fixtures.ts`,
   `scripts/capture-remote-parity-fixtures.ts`, and
   `scripts/verify-parity-fixtures.sh` only after this check passes without
   them.
4. Remove `src/`, server-only vendor modules, Bun test infrastructure, and
   obsolete package dependencies. Then enforce an absence test for imports and
   runtime commands that reference those paths.
5. Re-run both full Phase-3 computer-use passes and all release archive smoke
   tests after deletion. A fixture corpus alone does not validate UI behavior.
