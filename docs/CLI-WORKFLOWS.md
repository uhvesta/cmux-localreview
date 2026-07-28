# Go CLI workflows

This is the authoritative command reference for the Go migration branch. The
only supported runtime commands are the `localreviewd` daemon and the
`localreview` CLI built by Bazel. Historical `bun src/*.ts` commands and ACP
session commands are migration-era references, not an operational interface.

## Install or build

For a development checkout, build both binaries:

```sh
bazel build //cmd/localreviewd:localreviewd //cmd/localreview:localreview
export PATH="$PWD/bazel-bin/cmd/localreviewd/localreviewd_:$PWD/bazel-bin/cmd/localreview/localreview_:$PATH"
```

Release archives install the same two binaries under `~/.local/bin`:

```sh
sh ./install.sh
```

`localreviewd` owns the loopback HTTP server, SQLite data, browser session
capability, embedded UI, and Git diff operations. `localreview` is the only
operator CLI; it never opens the SQLite database directly.

## Start the daemon and open the UI

```sh
# Keep this process running in a terminal or service supervisor.
localreview daemon run --port 0

# The flag-only spelling remains supported during the migration.
localreview daemon --port 0

# Inspect the exact loopback process recorded in the owner-only discovery file.
localreview daemon status

# Gracefully stop only the daemon whose /health PID matches that record.
localreview daemon stop

# Queue Home. It prints a one-time URL, then opens it unless --no-open is set.
localreview open

# Open an explicit workspace reviewer after registering the workspace.
localreview open /absolute/path/to/workspace

# Print instead of launching a browser.
localreview open --no-open /absolute/path/to/workspace
```

The daemon writes `daemon.json` in `${CMUX_LOCALREVIEW_DATA_DIR}` when set,
otherwise `~/.local/share/cmux-localreview`. It is mode `0600` and contains a
loopback bearer capability. `localreview open` places it only in the URL
fragment; the UI exchanges it for an HttpOnly, SameSite=Strict local cookie.
Do not copy that URL or token into tickets, logs, or prompts.

`daemon status` reads this record and verifies the responding `/health` PID;
`daemon stop` performs the same check before it sends `SIGTERM`. This prevents
a stale port record from ever terminating an unrelated local process. A remote
worker uses the same loopback command after logging in there:

```sh
# Run on the worker itself. It does not expose a TCP service beyond loopback.
localreview remote daemon run --port 4311 --data-dir /srv/localreview
```

## Desktop shell (optional)

The Electron shell has no API or credential logic: it starts the same Go
daemon as a sidecar, passes its PID as a watchdog parent, waits for loopback
health, and opens Queue Home. If Electron crashes, `localreviewd` exits within
one polling interval; normal quit also sends it `SIGTERM`.

```sh
bazel build //cmd/localreviewd:localreviewd
cd desktop
npm install
LOCALREVIEWD_PATH="$PWD/../bazel-bin/cmd/localreviewd/localreviewd" npm run dev
```

The packaged application bundles `localreviewd` as an Electron resource. The
browser UI receives the daemon capability only in a URL fragment and trades it
for the same local HttpOnly cookie described above.

## Install Copilot project skills

Run this once per project, then again after upgrading localreview:

```sh
# Project-local .github skills and a bounded managed instruction block.
localreview setup /absolute/path/to/project

# Preview all changes without writing anything.
localreview setup --dry-run --json /absolute/path/to/project

# Also install personal skills beneath ~/.copilot/skills.
localreview setup --personal /absolute/path/to/project
```

The installer never overwrites unmanaged files. It owns only its named skills
and its marked block in `.github/copilot-instructions.md`. Start a new Copilot
CLI session or reload skills after installation.

## Submit and review a local snapshot

```sh
# Capture an immutable snapshot, then create or reuse the active queue stream.
localreview submit --title 'Review parser change' --topic parser-boundaries \
  /absolute/path/to/workspace

# Open Queue Home, select the item, and choose Open workspace.
localreview open
```

A local stream identity is the cleaned absolute workspace path plus the
optional stable topic (or title when no topic is supplied). Re-submitting an
active stream is idempotent. After a terminal decision or removal, reusing the
same stream creates a new immutable round linked to the prior one.

Queue Home is the primary entry point. It shows local and remote sections; a
reviewer opens only after the explicit **Open workspace** action. **Remove**
dismisses an item without claiming review completion. Removed and terminal
items stay in history, cannot be reopened or receive feedback, and their
snapshot is not altered.

`localreview queue-submit` remains a compatible alias for `localreview submit`
while existing skills and automation are migrated.

## Reproduce a saved snapshot

```sh
localreview reproduce /path/to/manifest.json /new-or-empty/destination
localreview open /new-or-empty/destination

# Materialize the immutable snapshot retained by a queue item and print the
# fresh SDK-native /ask continuation plan (never ACP session resurrection).
localreview reproduce --copilot <queue-item-id> /new-or-empty/destination
```

Reproduction verifies the saved Git bundles and refuses a non-empty
destination. `--copilot` reads the queue item's retained manifest through the
authenticated daemon, then prints the historic session ID only as provenance.
Opening the reproduction starts a fresh SDK-native `/ask` conversation; it
never replays prompts or tries to resurrect ACP merely because a page, queue
item, or conversation is reopened. Add `--json` for automation.

## `/ask`, Copilot authentication, and model choice

`/ask` is a fresh, persisted Copilot SDK conversation. It is separate from
formal review comments and exported feedback. A question created inline keeps
the workspace path, repository, file, side, line/range, and selected text.
Reloading the reviewer reads that saved transcript; it does not send another
prompt.

The Go daemon accepts only its dedicated Copilot credential source. It does
not fall back to `gh`, a PAT, environment variables, or an existing Copilot
CLI login. Register a distinct GitHub OAuth App for each capability, then
store the client secret through stdin (never as a command-line argument):

```sh
printf '%s' "$COPILOT_APP_CLIENT_SECRET" | \
  localreview auth login --capability copilot --client-id "$COPILOT_APP_CLIENT_ID" \
  --client-secret-stdin --no-wait

localreview auth status
localreview auth logout copilot
```

The default flow is state-verified browser OAuth on an ephemeral `127.0.0.1`
callback. `--device` explicitly selects the terminal/device fallback. Tokens
and client secrets are stored only through the OS secret store and never
returned to the browser or CLI output. The fully live SDK message route and
model enumeration are still being completed; until Queue Home reports a live
model catalogue, the picker intentionally reports its unauthenticated or
fallback state instead of pretending requests will work.

For operators migrating from the former `localreview-github-app` executable,
the native CLI retains its setup vocabulary as a thin, secure alias:

```sh
localreview github-app guide
localreview github-app configure --capability copilot --client-id "$COPILOT_APP_CLIENT_ID"
localreview github-app connect --capability copilot       # loopback browser flow
localreview github-app connect --capability copilot --device
localreview github-app status
localreview github-app disconnect --capability copilot
```

`github-app configure` saves only the public client ID. If your registration
uses a confidential client, add `--client-secret-stdin` and pipe the secret;
never place it in a shell history or argument list.

## File-change reload workflow

When an opened workspace changes, the daemon fingerprints both Git status and
the actual binary diff. This detects content-only edits that remain `M` in
`git status`. The reviewer displays **Changes detected — click to reload**;
click it to refetch the diff. Reloading a diff does not create a queue item or
send anything to Copilot.

## Remote and GitHub workflows

The queue UI can display remote nodes and PR records, but SSH federation,
GitHub PR mirroring/publication, and remote setup are not yet operator-ready
in the Go cutover. Do not use historical `global-daemon`, `queue-submit <PR
URL>`, ACP, or remote-tunnel documentation as a supported production recipe.
Use local snapshots until the corresponding Go CLI subcommands land.

## Validation checklist

```sh
go test ./...
bazel test //...
bash scripts/verify-parity-fixtures.sh

# Build the embedded browser UI before a distributable daemon build.
bash scripts/stage-ui-assets.sh
bazel build //cmd/localreviewd:localreviewd //cmd/localreview:localreview
```

For a manual smoke test: create a disposable Git repository with one modified
tracked file, run `localreview daemon`, queue it with `localreview
queue-submit`, use Queue Home to open it, edit the file again, and click the
reload notice. Confirm the diff changes and browser console has no new error.
