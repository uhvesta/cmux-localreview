# Phase-4 deletion candidate (not authorized yet)

This is a mechanically precise cutover plan prepared against commit
`9a18ba7e913545c684608b2fb038b6c3d229c2cc` on
`codex/go-daemon-migration`. It is deliberately **not** a deletion commit and
does not claim that Phase 4 is complete.

## Evidence obtained

On this candidate, the native pre-deletion gate passed:

```sh
bash scripts/verify-phase4-absence.sh --predelete
```

That command ran the native runtime boundary, frozen-corpus integrity, and Go
parity-matrix checks without invoking Bun or importing `src/`. Its explicit
retired inventory is currently:

| Retired group | Tracked files |
| --- | ---: |
| `src/` frozen server/CLI tree | 71 |
| capture and compatibility scripts | 5 |
| `vendor/difit/src/{cli,server}` plus CLI tsconfig | 19 |
| `vendor/cmux-hub/*.ts` server modules | 4 |
| root `tsconfig.json` | 1 |

The pre-delete result proves that the preserved parity corpus is consumed by
Go/Bazel and that the retained renderer does not import the server-only vendored
modules. It does **not** prove the interactive migration exit criteria.

## Stop line currently in force

Deletion must not begin yet. `MIGRATION_LOG.md` explicitly records that neither
of the two required complete, clean-profile Phase-3 acceptance passes exists.
The missing portions include authenticated Copilot SDK stream/cancel/resume and
model switching, fresh desktop-browser loopback OAuth, the complete Queue Home
remote tunnel UI, an authenticated Electron click-through, and two complete
RSS-trended runs. The repository only has the `ts-final` fixture tag; it does
not yet have the required tagged release artifact set.

Before executing the patch below, record two consecutive clean passes of all
seven checks in `NATIVE_MIGRATION_SPEC.md` §Phase 3 for **both** local web and
packaged Electron, then create and verify the release tag/artifacts. Re-run the
pre-delete gate from an otherwise clean tree immediately before mutation. Do
not treat a fixture, API smoke, or one credential-free UI slice as a substitute
for these passes.

## Exact future deletion patch

When the stop line is cleared, make one reviewable commit with this order. Do
not copy or regenerate either JSON corpus.

1. Remove these exact paths:

   ```text
   src/
   scripts/capture-parity-fixtures.ts
   scripts/capture-remote-parity-fixtures.ts
   scripts/verify-parity-fixtures.sh
   scripts/legacy-bin.mjs
   scripts/legacy-bin.test.mjs
   vendor/cmux-hub/cmux.ts
   vendor/cmux-hub/logger.ts
   vendor/cmux-hub/review-watcher.ts
   vendor/cmux-hub/review.ts
   vendor/difit/src/cli/
   vendor/difit/src/server/
   vendor/difit/tsconfig.cli.json
   tsconfig.json
   ```

2. In `vendor/difit/tsconfig.json`, remove only the project reference to
   `./tsconfig.cli.json`. Keep the client/type/utils include graph unchanged.

3. In the root `package.json`, remove the entire compatibility `bin` map and
   all scripts that invoke the frozen runtime, frozen tests, root `tsconfig`,
   `tsconfig.cli`, or `scripts/legacy-bin.test.mjs`. The retained scripts must
   build/type-check/test the Vite renderer only; specifically,
   `vendor-difit-build` must not invoke `tsconfig.cli.json`.

4. Remove these retired direct dependencies from `package.json`:

   ```text
   @github/copilot-sdk
   @parcel/watcher
   @zed-industries/agent-client-protocol
   @zed-industries/claude-code-acp
   commander
   express
   open
   simple-git
   ws
   @types/bun
   @types/express
   @types/ws
   ```

   Keep `prism-svelte`: the retained renderer's `languageLoader.ts` lazy-loads
   it for `.svelte` files. Do not remove a dependency merely because it does
   not appear in a server path.

5. Re-resolve `bun.lock` using the package manager after the manifest changes;
   do not hand-edit its transitive graph. Preserve dependencies imported by the
   retained React/Vite renderer and desktop packaging. Update vendoring
   provenance/documentation so it no longer claims that retired server/CLI
   source is retained.

   Also remove `scripts/legacy-bin.mjs` from `scripts/BUILD.bazel`'s
   `exports_files` list. The strict absence gate scans build metadata, so this
   is a required cutover edit rather than a cosmetic cleanup.

6. Update `README.md`, `SPEC.md`, `HANDOFF.md` (or retire the latter),
   `vendor/difit/VENDOR.md`, and CLI help documentation to describe the
   Go-only runtime and the release installer. Historical migration documents
   and `testdata/parity/ts-final` provenance remain intentionally historical.

## Mandatory post-delete commands

Run these from a clean checkout after the deletion commit. A failure is a
revert/fix-and-repeat condition, not an excuse to weaken an absence gate.

```sh
git diff --check
bash scripts/verify-phase4-absence.sh
bash scripts/verify-frozen-parity-corpus.sh
bash scripts/verify-go-parity-matrix.sh
bash scripts/verify-native-runtime-boundary.sh
bash scripts/verify-ui-clean-install.sh
go test ./...
bazel test //... --test_output=errors
bash scripts/verify-release-archives.sh
bash scripts/verify-native-installer.sh \
  bazel-bin/release/localreview_darwin_arm64.tar.gz \
  bazel-bin/release/checksums.txt
```

Then build and validate the current packaged desktop artifact (not an earlier
copy):

```sh
bazel build //desktop:app
npm --prefix desktop run verify:package -- \
  'desktop/dist/mac-arm64/CMUX Local Review.app'
```

Finally repeat the two complete clean-profile computer-use runs and the
release-tag platform archive workflow required by `AGENT_LOOP.md`. Record
commands, observations, screenshots, sidecar lifecycle, and RSS samples in
`MIGRATION_LOG.md`; only then can the migration exit audit consider Phase 4.

## Scope guard

This candidate intentionally does not modify `package.json`: it is currently
an unrelated user-owned working-tree change. The actual deletion commit must
start from a reviewed package manifest and must stage only the cutover changes.
