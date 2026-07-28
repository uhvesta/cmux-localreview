# Phase-4 retired-runtime inventory

This is the deletion manifest for the final TypeScript-to-Go cutover. It is a
pre-deletion record, not evidence that these paths are gone. Run
`bash scripts/verify-phase4-absence.sh --predelete` while this inventory still
exists; run the same command without an argument only after the deletion
commit.

## Delete in the Phase-4 commit

| Runtime group | Current paths/dependencies | Reason it is still present today | Phase-4 action |
| --- | --- | --- | --- |
| Frozen daemon and old CLIs | `src/` (top-level CLI wrappers, `global-daemon.ts`, and `server/`) | It generated the final TypeScript parity corpus and remains a forensic oracle only. | Delete all of `src/`. |
| ACP/agent adapters | `src/server/acp*.ts`, `agentProvider.ts`, `terminalBtw.ts`, their tests/fixtures | ACP adapters are explicitly out of scope for the Go/Copilot-SDK core. | Delete with `src/`; do not replace with subprocess adapters. |
| Frozen capture harness | `scripts/capture-parity-fixtures.ts`, `scripts/capture-remote-parity-fixtures.ts`, `scripts/verify-parity-fixtures.sh` | Re-captures are useful only until the final frozen-corpus review. | Delete after the native corpus gates have passed in the deletion candidate. |
| Compatibility package runtime | `scripts/legacy-bin.mjs`, `scripts/legacy-bin.test.mjs`, root `package.json` `bin` redirects | It makes legacy npm entry points fail with migration guidance during Phases 1–3. | Delete redirects and old `bin` entries; preserve native installer wrappers. |
| Root server/CLI dependencies | Root `package.json` entries for Express, Commander, `simple-git`, `open`, watcher/WS, JS Copilot SDK, ACP packages, and their server-only type/test dependencies | They support only the frozen Bun server/CLI/test harness. | Prune after proving `//ui:dist` and Electron package from a clean install. Keep only React/Vite build inputs and any UI-only test dependencies. |
| Server-only vendored TS | `vendor/cmux-hub/{cmux,logger,review-watcher,review}.ts` | Imported solely by frozen server behavior. | Delete `.ts` modules after confirming React imports none. Keep licensing/provenance records as needed. |
| Vendored difit server/CLI | `vendor/difit/src/{server,cli}/` and `vendor/difit/tsconfig.cli.json` | These are an older Express/simple-git server and Commander CLI that happen to live beside the retained React renderer. Vite starts at `src/client`; it does not import either tree. | Delete both source trees and the CLI TypeScript config. Remove the `tsconfig.cli.json` project reference from `vendor/difit/tsconfig.json` and the root script that invokes it. Preserve `vendor/difit/src/{client,types,utils}` because the renderer imports the latter two. |
| Root TypeScript server config | root `tsconfig.json` | It type-checks only `src/**` and declares Bun globals; it does not type-check the retained renderer. | Delete it with `src/` and remove `@types/bun`. The retained renderer is type-checked directly through `vendor/difit/tsconfig.json`. |

## Preserve after deletion

| Artifact | Native consumer | Why it remains |
| --- | --- | --- |
| `testdata/parity/ts-final/http.json` | `TestFrozenTypeScriptParityMatrix` | Immutable HTTP behavioral oracle. |
| `testdata/parity/ts-final/remote-pr.json` | `TestFrozenRemotePullRequestLifecycleParity` | Immutable remote PR lifecycle oracle. |
| `internal/daemon/parity_corpus_test.go` | Go/Bazel release tests | Pins corpus SHA-256/provenance. Its historical `generatedBy` strings are intentional. |
| `scripts/verify-frozen-parity-corpus.sh`, `scripts/verify-go-parity-matrix.sh` | Native release gate | Verify/replay the archive without Bun, Node, `src/`, or capture scripts. |
| `vendor/difit/src/{client,types,utils}`, `vendor/difit/{vite.config.ts,tsconfig.json,postcss.config.js}` | Vite/React build | The renderer entry graph uses only these source groups. The retained TypeScript is build-time UI code, not a server/CLI runtime. |

## Gate behavior

`--predelete` confirms that the complete retired inventory is still explicit,
the preserved corpus has Go-native consumers and passes its Go/Bazel replay,
the retained renderer imports none of the server-only `vendor/cmux-hub`
modules, and the Go runtime boundary is already clean. It must not claim the
frozen files are absent.

The pre-delete check deliberately exercises the native frozen-corpus and
parity-matrix gates. It never launches Bun/Node or imports `src/`; it is safe
to use as the final stop-line test immediately before a reviewed deletion
commit. It is not a replacement for the two clean browser/Electron Phase-3
passes recorded in `MIGRATION_LOG.md`.

## Exact deletion commit order

This order is intentionally concrete so Phase 4 can be performed as one
reviewable cutover rather than a sequence of half-native states.

1. From a clean checkout, run the Phase-3 exit evidence and the native
   pre-delete gate. Do not mutate the frozen parity JSON after this point.
2. Remove `src/`, the three capture scripts, legacy bin script/test, the four
   `vendor/cmux-hub/*.ts` modules, and the vendored difit `src/server` and
   `src/cli` trees. Delete root `tsconfig.json` and
   `vendor/difit/tsconfig.cli.json`; remove the latter from the renderer
   `tsconfig.json` project references.
3. Prune root `package.json`/`bun.lock` in the same commit. The following are
   retired direct dependencies: `@github/copilot-sdk`, both `@zed-industries`
   ACP packages, `@parcel/watcher`, `commander`, `express`, `open`,
   `simple-git`, `ws`, `@types/bun`, `@types/express`, and `@types/ws`.
   `prism-svelte` has no renderer import and is retired too. Remove the root
   `bin` map and scripts that invoke legacy tests or `tsconfig.cli`; keep only
   Vite/React/UI-test scripts. Re-resolve the lockfile rather than editing it
   by hand.
4. From a fresh dependency installation, run the strict absence gate, UI
   clean-install build, Go/Bazel suite, release-archive gate, and the two
   complete clean-profile browser/Electron passes. A missing token or remote
   service must exercise the UI's recovery path rather than turn this into a
   skipped workflow.

The strict form fails if any retired path/module remains, if root package
dependencies still name the retired JS server/ACP runtime, or if a non-exempt
runtime/build source refers to `src/server`, Bun capture commands, ACP, or the
JS Copilot SDK. Documentation and frozen-corpus provenance are deliberately
outside that textual scan. This separates auditable historical references from
live code paths.
