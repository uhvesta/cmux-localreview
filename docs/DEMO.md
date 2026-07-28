# Local browser smoke test

The native `localreview demo` command opens Queue Home; it does not fabricate
a server, fixture, or queue item. This short workflow creates a disposable Git
repository that works in Firefox, Chrome, or another browser chosen by your
system.

## Create a safe local fixture

```sh
fixture=$(mktemp -d /tmp/localreview-demo.XXXXXX)
git -C "$fixture" init
git -C "$fixture" config user.email demo@example.invalid
git -C "$fixture" config user.name localreview-demo
printf 'const before = 1\n' >"$fixture/main.ts"
git -C "$fixture" add main.ts
git -C "$fixture" commit -m baseline
printf 'const after = 2\n' >"$fixture/main.ts"
```

## Start, submit, and open

```sh
# Keep this terminal running.
CMUX_LOCALREVIEW_DATA_DIR="$(mktemp -d /tmp/localreview-data.XXXXXX)" \
  localreview daemon run --port 0
```

In a second terminal using the **same** `CMUX_LOCALREVIEW_DATA_DIR` value:

```sh
CMUX_LOCALREVIEW_DATA_DIR=/tmp/localreview-data.REPLACE_ME \
  localreview submit --title 'Browser smoke test' --topic demo "$fixture"
CMUX_LOCALREVIEW_DATA_DIR=/tmp/localreview-data.REPLACE_ME \
  localreview open --no-open
```

Paste the printed loopback URL into Firefox or Chrome, or omit `--no-open` to
use the default browser. The token remains in the URL fragment and is not sent
to the server. Do not share that URL.

In Queue Home, verify the card shows the absolute fixture path, then select
**Open workspace** and verify the `before → after` diff. Change `main.ts`
again and use the visible reload notice to verify a refreshed diff. Queue
loading and diff reload must not send any `/ask` or formal feedback prompt.

## Optional native build smoke

From a source checkout:

```sh
go test ./...
bazel test //...
bash scripts/stage-ui-assets.sh
bazel build //cmd/localreviewd:localreviewd //cmd/localreview:localreview
```

For the packaged Electron shell, `bazel build //desktop:app` produces a
host-local archive. The desktop shell starts the same loopback daemon; it is
not a separate API or credential boundary.

## Clean up

Stop the daemon with `Ctrl-C` (or `localreview daemon stop` in the same data
directory), then remove only the printed temporary fixture/data directories if
you no longer need them. Never use demo cleanup against a real checkout.
