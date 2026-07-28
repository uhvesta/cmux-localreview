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
