# Troubleshooting and recovery

Use the smallest recovery action. Do not delete daemon state, snapshots, or
managed PR caches to resolve a UI issue.

## Check the native boundary first

```sh
localreview daemon status
localreview open --no-open
git --version
```

The discovery record is `${CMUX_LOCALREVIEW_DATA_DIR}/daemon.json` when the
variable is set, otherwise `~/.local/share/cmux-localreview/daemon.json`. It
contains a bearer capability. Do not paste it—or an `open --no-open` URL—into
logs, tickets, prompts, or shell history.

## “Bearer token required” or a blank UI

Open the UI through the CLI, not by typing `/`, `/queue`, or `/review` into a
browser:

```sh
localreview open
localreview open /absolute/path/to/workspace
```

The token belongs in a URL fragment only; the UI immediately exchanges it for
an HttpOnly local cookie. If the daemon restarted, generate a new URL rather
than reusing an old bookmark. Verify the browser and daemon use the same
`CMUX_LOCALREVIEW_DATA_DIR`.

## “Failed to fetch diff data”

For a local item, verify the original submission workspace is a Git worktree,
then open the saved queue item from Queue Home:

```sh
git -C /absolute/path/to/workspace status
git -C /absolute/path/to/workspace diff --binary HEAD
localreview open
```

Do not manually alter a daemon-managed remote PR worktree. For a remote item,
refresh it in Queue Home after the pull request head changes, then reopen the
item. Record the visible queue ID, selected base/head, and exact error before
filing a bug.

## Queue appears empty or a submission is missing

Make sure submitting and opening use the same daemon environment:

```sh
localreview daemon status
localreview submit --title 'Queue smoke test' /absolute/path/to/workspace
localreview open
```

`CMUX_LOCALREVIEW_DATA_DIR` deliberately isolates queues. A snapshot
submission requires a Git repository; it does not modify the worktree/index.

## Copilot `/ask` is unavailable, models do not load, or streaming stops

`/ask` uses only the dedicated `copilot` OAuth capability. It does not use
`gh`, a PAT, environment token, or Copilot CLI login state.

```sh
localreview auth status
localreview github-app connect --capability copilot
```

Browser loopback OAuth is the default and requires the registered callback
`http://127.0.0.1:8787/oauth/callback`; it uses PKCE and needs only the public
client ID. Use `localreview auth login --client-id <id> --loopback`. Device flow
is the explicit fallback for headless machines.
Return to the existing `/ask` transcript after authentication; do not re-submit
a question simply because the panel was reopened. If a turn is visibly
streaming, cancel it before starting a different turn. A saved error means
delivery failed and is useful diagnostics, not a hidden retry request.

The native daemon starts the Copilot SDK lazily only after an explicit model
load or prompt. Its isolated SDK state lives under the daemon data directory
(`copilot-sdk/`, mode `0700`); it never adopts an ambient `gh` or Copilot CLI
login. An explicit `copilot GitHub App is not connected` error therefore means
the dedicated capability still needs to be authorized, not that the prompt was
silently sent.

The official Go SDK still launches the **Copilot CLI binary** as its isolated
runtime. The daemon prefers a current `copilot` executable found on `PATH` and
uses the SDK-bundled runtime only when none is installed. Its local login is
not used in either case: the daemon passes only the dedicated `copilot` OAuth
capability to that child process. If the model picker says that the CLI runtime
disconnected or reset its connection, run the following locally, then reopen
`/ask` and press **Refresh chat**:

```sh
copilot --version
copilot update
localreview daemon stop
localreview open
```

Restarting creates a fresh SDK runtime; it never replays saved `/ask` turns.
If the error persists after the CLI is current, reconnect only the dedicated
capability (`localreview github-app connect --capability copilot`) rather than
sharing a `gh` token or a Copilot CLI session.

## GitHub PR cannot be added or appears stale

Connect the least-privileged `read` capability and check its state:

```sh
localreview github-app connect --capability read
localreview auth status
localreview remote submit https://github.com/OWNER/REPOSITORY/pull/123
```

Typical causes are an incorrect canonical PR URL, missing OAuth application
access, or a new PR head. Refresh the remote item rather than reviewing an old
head. **Publish to GitHub** needs both connected `read` and `write` App
capabilities. It checks the saved snapshot head against GitHub immediately
before creating a review; a changed or closed PR returns a stale-head error
and does not write or save a local decision. Choose **Save locally** when no
GitHub write is intended.

## Feedback delivery is unavailable or already running

Review the formal feedback prompt and use **Copy feedback prompt** if it must
go elsewhere. **Send through Copilot** requires the separate `copilot`
capability and an explicit selected item. The daemon serializes delivery per
item, records successful delivery, and leaves a failed batch undelivered for a
deliberate retry. Opening the review never retries it.

## Skills do not show up in Copilot CLI

```sh
localreview setup --dry-run /absolute/path/to/project
localreview setup /absolute/path/to/project
```

Restart Copilot CLI or reload skills. The installer intentionally skips an
unmanaged file of the same name; inspect that result and reconcile it manually
instead of deleting user configuration.

## A remote daemon cannot be reached

On the worker, verify its local process and port:

```sh
localreview remote daemon run --port 4311 --data-dir /srv/cmux-localreview
```

Keep it on loopback. The native federation transport opens an ephemeral local
SSH forward and reports its retryable error on the node card. Fix SSH
keys/host verification (`ssh user@host true`) and press Retry/Connect; do not
make the daemon public or transfer a discovery token through an untrusted
channel.

## Snapshot reproduction fails

Use a new or empty destination and retain failed artifacts for diagnosis:

```sh
mkdir -p /tmp/localreview-reproduction
localreview reproduce <manifest.json> /tmp/localreview-reproduction
```

The destination must be empty. Snapshot verification failure should never be
worked around by editing bundles or overwriting the original source checkout.

## What to include in a report

Include the queue ID, local path or PR URL, state, selected base/head, action,
and exact visible error. Include `localreview daemon status` and relevant
versions with secrets redacted. Never include OAuth tokens, discovery JSON,
tokenized browser URLs, or full terminal transcripts.
