# Desktop shell

`main.mjs` is deliberately the only Electron business code. It launches the
bundled `localreviewd` with `--port=0`, waits for its loopback `/health`, reads
the daemon’s capability from its mode-600 discovery file, then opens Queue Home
with that capability in the URL fragment. The web app exchanges the fragment
for its HttpOnly browser cookie; Electron never stores the token.

For development, build the Go daemon and point the shell to it:

```sh
bazel build //cmd/localreviewd:localreviewd
cd desktop
npm install
LOCALREVIEWD_PATH="$(pwd)/../bazel-bin/cmd/localreviewd/localreviewd_/localreviewd" npm run dev
```

Before a desktop release, run the display-free sidecar check after building the
daemon. It verifies the cold-start health handshake, the <50 MB idle-RSS
target, shutdown/restart with the same SQLite profile, and orphan cleanup via
`--parent-pid`:

```sh
npm run verify:sidecar
```

After `npm run package:dir`, validate the unpacked artifact itself—not merely
the checkout daemon:

```sh
npm run verify:package -- "dist/mac-arm64/CMUX Local Review.app"
# Linux: npm run verify:package -- dist/linux-unpacked
```

This inspects `app.asar`, syntax-checks its packaged main process, confirms the
native sidecar is executable, and runs the same lifecycle check against that
bundled binary. It is a release-boundary check; a renderer/UI interaction pass
still needs a real Electron window.

The desktop release job stages the current Vite output into the Go binary
before it packages the sidecar. If a local package shows UI copy or controls
that differ from `vendor/difit`, run `bash scripts/stage-ui-assets.sh`, rebuild
`//cmd/localreviewd:localreviewd`, then package again; never certify the older
artifact based only on its sidecar health.

## Isolated renderer acceptance smoke

Use a disposable Git workspace and a separate Electron user-data directory
when manually exercising the packaged renderer. This prevents a smoke run from
sharing the user's queue, daemon SQLite database, GitHub credentials, or
single-instance lock. In the isolated window, verify this minimum local flow:

1. On Queue Home, enter the disposable workspace path, title, and topic; click
   **Submit local**.
2. Confirm the queued card shows the supplied title and path, then click
   **Open workspace**.
3. Confirm `/review?queueItem=…` opens, the repository/file tree is present,
   and the changed file renders its actual diff in Split or Unified mode.

This smoke covers current packaged renderer loading, Queue Home mutation,
snapshot submission, queue-to-review navigation, and diff rendering without
GitHub or Copilot credentials. It does **not** cover authenticated `/ask`,
GitHub publishing, or a complete review decision lifecycle; those remain
separate Phase-3 acceptance cases.

It deliberately does not claim a full GUI acceptance pass. Follow it with the
Phase-3 Electron checklist in [CLI workflows](../docs/CLI-WORKFLOWS.md#desktop-shell-optional):
open the packaged app, complete one durable review action, quit, relaunch, and
confirm that saved state is present while the previous sidecar PID is gone.

On quit, Electron sends the sidecar `SIGTERM` and escalates to `SIGKILL` only
after three seconds. Packaging places the release `localreviewd` beside the
Electron resources through electron-builder’s `extraResources` rule.

To make a host-native desktop artifact, first build the daemon at the exact
path above, then run `npm run package` from this directory. `npm run
package:dir` is the quicker unpacked-artifact smoke target. The package is
deliberately host-native: GitHub Release CI builds a macOS artifact on macOS
and an AppImage on Linux. Do not cross-package an Electron app with a daemon
from a different OS/architecture.

For CI-like packaging from Bazel after `npm --prefix desktop install`, use
`bazel build //desktop:app`. This explicit local target stages the exact Bazel
daemon beside Electron's host runtime and emits a tarball containing the
unpacked app. It is marked `manual`: Electron downloads platform-specific
runtime files, so it does not belong in the hermetic Go test graph. Tag-release
CI makes the real DMG and AppImage artifacts.

Security/lifecycle invariants:

- Electron passes `--parent-pid`; an unexpected shell exit stops its daemon.
- The shell accepts only the discovery file and `/health` response written by
  the child PID it just spawned, never a stale profile record.
- The window has renderer Node integration disabled, sandboxing and web
  security enabled, permissions denied, and external links open in the system
  browser.
- One Electron process owns one sidecar. A second launch focuses the existing
  window rather than starting another daemon against the same SQLite profile.
