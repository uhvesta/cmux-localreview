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

For an unattended macOS/Linux bootstrap that also projects the managed Copilot
skills into a known workspace, use `sh ./install.sh --setup /absolute/path/to/project`.
Add `--personal-skills` only when you also want the managed copies under
`~/.copilot/skills`. This remains credential-free: configure the dedicated
public OAuth client IDs later with `localreview github-app configure`.

`localreviewd` owns the loopback HTTP server, SQLite data, browser session
capability, embedded UI, Git diff operations, and SDK conversation runtime.
`localreview` is the only operator CLI; it never opens the SQLite database
directly. The command is self-describing: `localreview --help` prints the
command groups and `localreview <command> --help` prints the accepted flags.

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
LOCALREVIEWD_PATH="$PWD/../bazel-bin/cmd/localreviewd/localreviewd_/localreviewd" npm run dev
```

The packaged application bundles `localreviewd` as an Electron resource. The
tag-release pipeline publishes the native `tar.gz` archives, their
`checksums.txt` manifest, the matching `install.sh`, plus a macOS DMG and Linux
AppImage. The archive is SHA-256 verified by the installer; it is not presented
as a signed artifact. For a local unpacked desktop smoke artifact, install the
desktop package dependencies and run `bazel build //desktop:app`; this target
is deliberately host-local and manual because Electron's runtime is
platform-specific.
browser UI receives the daemon capability only in a URL fragment and trades it
for the same local HttpOnly cookie described above.

Run the repeatable sidecar lifecycle check before a desktop release. It uses
the native daemon (not a mock or Electron renderer) to verify cold health,
idle RSS below 50 MB, clean restart using the same data directory, and parent
watchdog cleanup after a simulated shell crash:

```sh
cd desktop
npm run verify:sidecar
```

This does not replace a real Electron UI pass: after packaging, open the app,
make one durable review change, quit, relaunch, and verify that it remains
while the prior sidecar PID is absent from `ps`.

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

## Review a GitHub pull request locally (without queueing or publishing)

Use this path when the goal is to inspect a pull request, ask Copilot inline
questions, and leave no formal review record. It uses only the daemon's
dedicated **PR read** credential to resolve and cache a detached PR worktree;
it does not insert a queue item, send formal feedback, or publish anything to
GitHub.

```sh
# Opens the cached PR diff in local-only mode. /ask remains available after
# the separately configured Copilot capability is connected.
localreview open --pr https://github.com/owner/repo/pull/123

# Print the capability URL for Firefox/Chrome rather than opening the default
# browser. The token stays in the URL fragment.
localreview open --no-open --pr https://github.com/owner/repo/pull/123
```

Queue Home exposes the same distinction in its **Remote** column: enter the
PR URL and choose **Review locally** for a read-only `/ask` session, or choose
**Add PR** only when the PR should enter the formal review queue. Reopening a
local-only review reads the existing cached diff and `/ask` transcript; it
does not resend a prompt to Copilot. If you later want a formal queue round,
choose **Add PR** or run the explicit remote command below. `submit` and its
compatible `queue-submit` alias take a local Git workspace path; they do not
accept a PR URL.

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
CLI login. The compatibility command is named `github-app`, but the current
implementation uses a dedicated **GitHub OAuth App client registration** for
each capability; it is not a GitHub App installation flow. The native browser
flow uses PKCE, so setup needs only the public client ID:

```sh
localreview auth login --capability copilot --client-id "$COPILOT_APP_CLIENT_ID" --no-wait

localreview auth status
localreview auth logout copilot
```

The default flow is state-verified browser OAuth through the registered stable
callback `http://127.0.0.1:8787/oauth/callback`; PKCE protects the exchange
without shipping or storing an OAuth client secret. Use `--device` for an
SSH/headless host. Tokens are stored only through the OS secret store and
never returned to the browser or CLI output. When credentials or the Copilot SDK are
unavailable, the picker reports an unauthenticated/fallback state instead of
pretending a prompt was delivered. Once connected, turns stream through the
selected persistent conversation; loading a transcript never starts a turn.

For operators migrating from the former `localreview-github-app` executable,
the native CLI retains its setup vocabulary as a thin, secure alias:

```sh
localreview github-app guide
localreview github-app configure --capability copilot --client-id "$COPILOT_APP_CLIENT_ID"
localreview github-app connect --capability copilot       # browser loopback (default)
localreview github-app connect --capability copilot --device
localreview github-app status
localreview github-app disconnect --capability copilot
```

`github-app configure` saves only the public client ID. The loopback flow uses
PKCE; neither the UI nor the native daemon accepts, stores, or ships an OAuth
client secret.

### Deterministic `/ask` and `/btw` acceptance fixture (source checkouts only)

Use the separate `localreviewd-e2e` Bazel target when validating the browser
or Electron UX without contacting GitHub or Copilot. It exposes three fixed
SDK-shaped models and sends visibly delayed streaming responses, so model
selection, live status, cancellation, `/btw`, and durable transcript reloads
exercise the same Go route/SSE/SQLite code as a real session.

It is **not** an authentication mode, credential fallback, or release feature:
the production `localreviewd` binary has no fixture flag, and release archives
and the packaged Electron sidecar do not contain this target.

```sh
# Terminal 1: keep this running and use a disposable profile.
fixture_data="$(mktemp -d /tmp/cmux-localreview-e2e.XXXXXX)"
CMUX_LOCALREVIEW_DATA_DIR="$fixture_data" \
  sh scripts/run-e2e-copilot-fixture.sh --port 0

# Terminal 2: open Queue Home in Firefox/Chrome (or print the authenticated URL).
CMUX_LOCALREVIEW_DATA_DIR="$fixture_data" \
  bazel run //cmd/localreview:localreview -- open --no-open
```

Submit/open a disposable Git workspace, open **/ask**, select a fixture model,
send a question, and use **Stop response** while words are streaming. Reload
to verify that the durable transcript returns without a second prompt. Open
the `/btw` panel and send an explicit Copilot question to exercise its separate
thread. To use the Electron shell against this test daemon, build the fixture
then launch Electron with `LOCALREVIEWD_PATH` set to the resulting
`bazel-bin/cmd/localreviewd-e2e/localreviewd-e2e_/localreviewd-e2e` path and a
fresh `CMUX_LOCALREVIEW_DATA_DIR`; this is a developer-only test invocation.
The repeatable route-level acceptance gate is:

```sh
scripts/verify-e2e-copilot-fixture.sh
```

It covers queue/open, the browser capability exchange, live SSE deltas,
per-conversation model settings, the exact inline `/ask` create/reopen/no-
replay sequence and anchor persistence, cancellation, separate `/btw`, and
restart transcript persistence. It also verifies Queue Home’s immutable
submit/open/complete/requeue/remove lifecycle and confirms that reopening a
queue item does not replay an existing Copilot turn. It does not represent an
OAuth success or validate an external Copilot service; run the dedicated real-
auth pass separately.

## File-change reload workflow

When an opened workspace changes, the daemon fingerprints both Git status and
the actual binary diff. This detects content-only edits that remain `M` in
`git status`. The reviewer displays **Changes detected — click to reload**;
click it to refetch the diff. Reloading a diff does not create a queue item or
send anything to Copilot.

## Remote and GitHub workflows

Use a dedicated `read` capability to queue a pull request for local inspection:

```sh
localreview github-app connect --capability read
localreview remote submit --title 'Review owner/repo#123' \
  https://github.com/OWNER/REPOSITORY/pull/123
localreview open
```

The daemon resolves the PR and pins its managed worktree to the resolved head.
Refresh it in Queue Home after a push. Local review, `/ask`, and saved local
decisions do not need the `write` capability. **Save locally** records only a
local decision. **Publish to GitHub** is an explicit, opt-in action for an
open remote PR that requires both `read` and `write`; it re-resolves the head
immediately before publishing and refuses a stale or closed pull request. A
failed publication does not save a misleading local decision.

### Federating a remote daemon over SSH

Run the remote daemon on the remote machine with its normal loopback listener;
do **not** bind it to a LAN address or reverse-proxy it:

```sh
# remote host
localreview daemon --port 57140
# or let it choose a port and read ~/.local/share/cmux-localreview/daemon.json
```

On the local machine, obtain the remote daemon's discovery token through your
normal SSH/admin channel and add a node. The token is sent only to the local
daemon over its browser capability channel, stored in the platform secret
store under a federation-specific account (never SQLite), and used only in an
`Authorization` header inside a temporary SSH forward; it is never returned by
Queue Home.

```sh
printf '%s\n' "$REMOTE_DAEMON_DISCOVERY_TOKEN" | localreview federation add \
  --id work-lab --label 'Work lab' --ssh user@work-lab.example \
  --port 57140 --token-stdin
localreview federation connect work-lab
localreview federation queue --refresh
# Permanently remove local metadata, secure-store capability, cache, and tunnel.
localreview federation delete work-lab
```

Each fetch uses `ssh -N -T -o BatchMode=yes -o ExitOnForwardFailure=yes -L
127.0.0.1:EPHEMERAL:127.0.0.1:REMOTE_PORT user@host`. The daemon never listens
outside loopback, and forwarding uses no shell interpolation. Queue Home lazily
caches each node's queue/workspace response for 15 seconds; use **Refresh** or
`?refresh=true` to bypass the cache. **Disconnect** kills the local forward and
clears its cache; **Retry/Connect** establishes a fresh forward. A failed node
is retained with its error so other local/remote queues remain usable.

Use SSH keys or your platform's SSH agent. `BatchMode=yes` means password or
host-key prompts fail visibly rather than hanging the browser; first verify
`ssh user@host true` in a terminal. No historical Bun tunnel helper or ACP
session forwarding is required or supported.

For a service account, a nonstandard SSH port, or a bastion, configure a normal
OpenSSH `Host` alias and use that alias as `--ssh`. To keep a daemon independent
of the account's global `~/.ssh/config`, set
`CMUX_LOCALREVIEW_SSH_CONFIG=/absolute/path/to/ssh_config` before starting it.
The config must be a regular file that is not group- or world-writable; the
daemon passes it as OpenSSH's `-F` argument. This controls SSH routing only:
the remote daemon capability remains in the local OS secret store.

## Validation checklist

```sh
go test ./...
bazel test //...

# Release/native parity verification: does not execute the retired TS server.
bash scripts/verify-frozen-parity-corpus.sh
bash scripts/verify-go-parity-matrix.sh

# Pre-Phase-4 oracle determinism only (requires frozen TS/Bun sources).
bash scripts/verify-parity-fixtures.sh

# Build the embedded browser UI before a distributable daemon build.
bash scripts/stage-ui-assets.sh
bazel build //cmd/localreviewd:localreviewd //cmd/localreview:localreview
```

For a manual smoke test: create a disposable Git repository with one modified
tracked file, run `localreview daemon`, queue it with `localreview
queue-submit`, use Queue Home to open it, edit the file again, and click the
reload notice. Confirm the diff changes and browser console has no new error.
