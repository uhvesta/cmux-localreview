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

For a headless machine add `--device`. Return to the existing `/ask` transcript
after authentication; do not re-submit a question simply because the panel was
reopened. If a turn is visibly streaming, cancel it before starting a different
turn. A saved error means delivery failed and is useful diagnostics, not a
hidden retry request.

## GitHub PR cannot be added or appears stale

Connect the least-privileged `read` capability and check its state:

```sh
localreview github-app connect --capability read
localreview auth status
localreview remote submit https://github.com/OWNER/REPOSITORY/pull/123
```

Typical causes are an incorrect canonical PR URL, missing OAuth application
access, or a new PR head. Refresh the remote item rather than reviewing an old
head. **Publish to GitHub** is currently unavailable by design: the native
daemon rejects it clearly and does not turn it into a local decision. Choose
the plainly labelled local save action instead.

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

Keep it on loopback. SSH forwarding and federated aggregation are explicit
operator-managed steps during the migration. Fix SSH keys/host verification
and validate the forward yourself; do not make the daemon public or transfer a
discovery token through an untrusted channel.

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
