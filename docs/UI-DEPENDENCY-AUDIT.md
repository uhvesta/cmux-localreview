# UI dependency audit

This audit is the package-removal boundary for Phase 4. It describes the
current root `package.json` and `bun.lock`, not an intent to delete anything
yet. The frozen TypeScript capture surface is still present, so removing the
legacy packages before the Phase-4 stop line would make the established parity
corpus impossible to regenerate or inspect.

Run the renderer proof from a clean temporary directory:

```sh
bash scripts/verify-ui-clean-install.sh
```

The script copies only `package.json`, `bun.lock`, and `vendor/difit` to a
temporary directory, uses `bun install --frozen-lockfile --ignore-scripts`,
and builds Vite directly. It neither starts the retired server nor changes the
working tree. A successful run proves the checked-in lock can produce the UI;
it is not authorization to remove the frozen runtime.

## Dependency classification

| Package | Keep after Phase 4? | Evidence |
| --- | --- | --- |
| `react`, `react-dom`, `lucide-react`, `@floating-ui/react`, `@tanstack/react-virtual`, `react-hotkeys-hook` | Yes | Direct renderer imports under `vendor/difit/src/client`. |
| `diff`, `mermaid`, `prism-react-renderer`, `prism-svelte`, `prismjs`, `react-markdown`, `remark-breaks`, `remark-gfm` | Yes | Diff/rendering, syntax, and Markdown modules under `vendor/difit/src/client`. |
| `vite`, `@vitejs/plugin-react`, `typescript`, `tailwindcss`, `@tailwindcss/postcss`, `postcss`, `autoprefixer` | Yes | Renderer build configuration. |
| `vitest`, `happy-dom`, `@testing-library/*`, `@types/react`, `@types/react-dom`, `@tsconfig/strictest` | Yes | Renderer tests and TypeScript checks. |
| `@types/node` | Re-evaluate after Electron/build scripts are finalized | It is not imported by the renderer, but Vite/build configuration can require Node declarations. Retain until the clean UI and Electron CI jobs prove otherwise. |
| `undici` | Remove with frozen server tests | Only `vendor/difit` server tests and setup import it. |
| `@parcel/watcher`, `commander`, `express`, `open`, `simple-git`, `ws` | Remove in the reviewed Phase-4 deletion commit | Their live imports are root `src/` or `vendor/difit/src/server`/`src/cli`, not the renderer. |
| `@github/copilot-sdk`, `@zed-industries/agent-client-protocol`, `@zed-industries/claude-code-acp` | Remove in the reviewed Phase-4 deletion commit | They are JavaScript Copilot/ACP dependencies of the retired server. The native daemon uses Go modules instead. |
| `@types/bun`, `@types/express`, `@types/ws` | Remove in the reviewed Phase-4 deletion commit | Root frozen TypeScript compiler settings/source only. |

## Safe deletion patch shape

Do these edits together, only after the two required Phase-3 browser/Electron
passes and the native release gate are recorded:

1. Delete frozen `src/`, server-only vendored TypeScript modules, the old
   compatibility bins, and capture implementations as specified in
   [PHASE4-DELETION-READINESS.md](PHASE4-DELETION-READINESS.md).
2. Change the root UI build command to invoke Vite directly. Do not retain
   `vendor/difit/tsconfig.cli.json`: it includes its retired server/CLI tree.
3. Prune the legacy-only package rows above from both `package.json` and
   `bun.lock` with `bun install --lockfile-only`; do not hand-edit the lock.
4. Run `bash scripts/verify-ui-clean-install.sh`, the Electron packaging
   verification, native release gates, `go test ./...`, `bazel test //...`,
   and `bash scripts/verify-phase4-absence.sh` (strict mode).

The clean-install script intentionally builds Vite without `tsc
tsconfig.cli.json`; that compiler project is a frozen server/CLI input. Until
the deletion commit removes it, `bun run vendor-difit-build` is a larger
compatibility build and is not evidence that the UI package boundary is clean.
