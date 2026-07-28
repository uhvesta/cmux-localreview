# Native cutover audit

This is the Phase-2/Phase-4 deletion checklist. It records dependencies found
by repository search, rather than treating green Go tests as proof that the
TypeScript control plane can be removed.

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
| `scripts/capture-*.ts` | Capture/verify the frozen oracle | Replace them with immutable archived fixtures or retire the capture command after final comparison. |
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
