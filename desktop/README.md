# Desktop shell

`main.mjs` is deliberately the only Electron business code. It launches the
bundled `localreviewd` with `--port=0`, waits for its loopback `/health`, reads
the daemon’s capability from its mode-600 discovery file, then opens Queue Home
with that capability in the URL fragment. The web app exchanges the fragment
for its HttpOnly browser cookie; Electron never stores the token.

For development, build the Go daemon and point the shell to it:

```sh
bazel build //cmd/localreviewd
cd desktop
npm install
LOCALREVIEWD_PATH="$(pwd)/../bazel-bin/cmd/localreviewd/localreviewd" npm run dev
```

On quit, Electron sends the sidecar `SIGTERM` and escalates to `SIGKILL` only
after three seconds. Packaging places the release `localreviewd` beside the
Electron resources through electron-builder’s `extraResources` rule.
