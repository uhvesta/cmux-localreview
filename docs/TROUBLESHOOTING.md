# Troubleshooting and recovery

Use the smallest recovery action that fixes the failed boundary. Do not delete
snapshots, mirrors, or daemon state as a first response.

## Fast triage

```sh
bun --version
git --version
bun test
bun run typecheck
./src/localreview-github-app.ts status
copilot --version
bun src/localreview-open.ts --home
```

The daemon discovery file is normally
`~/.local/share/cmux-localreview/daemon.json`, or
`$CMUX_LOCALREVIEW_DATA_DIR/daemon.json` for an isolated environment. It holds
an access token: inspect it only locally and never paste it into chat, tickets,
shell history, or source control.

## Queue Home says “Bearer token required”

Cause: the UI was opened directly at `/`, `/queue`, or `/review` without the
one-time bootstrap fragment supplied by `localreview-open`.

```sh
bun src/localreview-open.ts --home
bun src/localreview-open.ts /absolute/path/to/workspace --base origin/main
```

Use the Queue Home recovery field only with a token read from the owner-only
local discovery file. If the daemon restarted, open a fresh URL with
`localreview-open` instead of reusing an old bookmark.

## The reviewer says “Failed to fetch diff data”

First ensure a local workspace is still a Git worktree and that the selected
base exists:

```sh
git -C /absolute/path/to/workspace status
git -C /absolute/path/to/workspace fetch --all --prune
git -C /absolute/path/to/workspace show-ref --verify refs/remotes/origin/main
bun src/localreview-open.ts /absolute/path/to/workspace --base origin/main
```

For a GitHub PR worktree, never manually check out branches inside the managed
cache. Select **Refresh remote PRs** in Queue Home, then open the workspace
reviewer again. Refresh rebuilds the cached PR worktree at the resolved head
and provides `origin/<base>` for comparisons like `origin/main → HEAD`.

If it still fails, report the queue-item ID and exact selected base/target,
not just the generic error.

## A remote PR cannot be added, refreshed, or published

```sh
bun src/localreview-github-app.ts status
```

| Symptom | Cause | Recovery |
| --- | --- | --- |
| `No GitHub credential is available` | The daemon cannot read the local GitHub CLI login and no optional App override is connected | Run `gh auth login --hostname github.com` as the daemon user, then refresh. Or configure/connect **PR read** with `localreview-github-app connect --capability read`. |
| PR cannot resolve | Wrong URL, missing App installation, or inaccessible repository | Verify canonical URL and install the PR read App on that repository. |
| Review is stale | PR head changed after opening | Refresh remote PRs, reopen, then publish. |
| Inline comment rejected | Anchor is outdated/unsupported | Refresh and correct the anchor; the publish App never silently redirects a review. |
| Cache missing | Worktree/mirror was cleaned | Re-add the PR URL. |

Approval, request-changes, and comments affect GitHub. Confirm the target PR
and head SHA in the detail view before publishing.

## `/ask` has no model, authentication fails, or streaming stalls

`/ask` is a fresh Copilot SDK chat using the dedicated **Copilot /ask** GitHub
App connection; it is not an existing ACP session and does not reuse a
Copilot CLI, `gh`, or environment login.

```sh
copilot --version
bun src/localreview-github-app.ts status
```

Connect the Copilot App and reload the reviewer. If `/ask` still fails, restart
the daemon and start a new conversation; retain the displayed model and error
for diagnosis. Cancel a streaming turn before issuing a new one. `/ask` transcript entries never become formal review
feedback unless a reviewer explicitly converts an answer to a comment.

If Copilot cannot find the project skills:

```sh
sh scripts/localreview-setup.sh /absolute/path/to/project
# Then run /skills reload in Copilot CLI.
```

Unmanaged `.github/skills` files are intentionally preserved. The setup output
identifies a skipped file so its owner can decide how to reconcile it.

## ACP feedback is unavailable, busy, or fails delivery

Feedback reaches an existing session only while the queue item has a valid
loopback endpoint, Copilot ACP is listening, and the session can be loaded:

```sh
copilot --acp --port 4123
bun src/queue-submit.ts . --title 'Review' \
  --acp-host 127.0.0.1 --acp-port 4123 --acp-session-id "$ACP_SESSION_ID"
```

For a remote agent, SSH-forward the ACP port and submit the local loopback port
instead. LAN/public hosts are rejected by design. Choose `queue` to wait for an
in-flight daemon-issued turn, or `interrupt` to cancel it. A successful batch
is recorded, so retries do not duplicate delivery. If the listener/session is
gone, use **Copy feedback prompt** or reproduce the snapshot and create a new
ACP session; a session ID cannot restore the old context by itself.

## cmux or terminal `/btw` fails

cmux is required only for terminal `/btw`; there is intentionally no
focused-terminal fallback. That prevents sending a prompt into the wrong agent.

1. Start cmux and ensure its control socket is reachable by the daemon.
2. Register or reconnect the agent with workspace and cmux `surfaceId`.
3. Confirm a current heartbeat and no cmux error in the agent status.
4. Select that explicit target, then send `/btw` again.

If cmux is unavailable, use `/ask` or copy a formal feedback prompt instead.

## Remote queue node is disconnected, empty, or slow

Local and remote queues are deliberately separate lists. Remote loading is lazy
so a failed host does not hide local work.

```sh
# On remote host
sh scripts/localreview-remote-daemon.sh --port 4311 --data-dir /srv/localreview

# On review machine; run the printed ssh command
sh scripts/localreview-remote-tunnel.sh \
  --ssh-target reviewer@worker.example --remote-port 4311 --local-port 5311
```

The tunnel runs with `BatchMode=yes`; an SSH password prompt means keys, host
verification, or SSH policy must be fixed first. Verify the remote discovery
token was transferred securely and the remote port matches the daemon. Then use
**Retry**/**Connect** in Queue Home. **Disconnect** keeps the configuration but
stops aggregation; **Remove** forgets the local node configuration.

The repository includes a fake loopback-tunnel fixture. A real remote host
still needs its own SSH and process-supervision validation.

## Snapshot/reproduction failure or an empty queue

Snapshots require a Git repository but should not mutate it:

```sh
git -C /absolute/path/to/workspace status
git -C /absolute/path/to/workspace fsck --no-dangling
mkdir /tmp/reproduced-review
bun src/localreview-reproduce-copilot.ts QUEUE_ID /tmp/reproduced-review
```

The reproduction destination must be new or empty. Keep a manifest that fails
checksum verification for diagnosis; do not trust or overwrite a failed
artifact. To distinguish UI state from submission, submit once by CLI:

```sh
bun src/queue-submit.ts /absolute/path/to/workspace --title 'Queue smoke test' --json
bun src/localreview-open.ts --home
```

If an ID is returned but the queue is empty, ensure both commands use the same
`CMUX_LOCALREVIEW_DATA_DIR`. That environment variable intentionally creates an
isolated queue; avoid running multiple daemons against one data directory.

## Safe reset boundaries

- Refresh or requeue an item before submitting another copy.
- Use remote clean-up controls only for cache-owned paths, never a normal
  source checkout.
- Restarting the daemon preserves persisted queue data. An interrupted ACP
  delivery remains retryable unless it was recorded delivered.
- Do not delete the discovery document, SQLite database, or artifact directory
  to solve a UI problem; those removals discard recovery history.

## What to include in a bug report

Include the queue-item ID, local/remote source, exact visible error, action,
and selected diff base/target. Include `bun --version`, `git --version`, and
the output of `localreview-github-app status` or `copilot --version` with
secrets redacted. Never include bearer tokens, discovery documents, ACP session IDs,
or terminal transcripts.
