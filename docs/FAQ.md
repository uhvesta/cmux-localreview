# Frequently asked questions

## Getting started

### What is the fastest safe way to try this locally?

From this checkout, configure the repository you want to review, submit it,
then open Queue Home:

```sh
sh scripts/localreview-setup.sh /path/to/repository
bun src/queue-submit.ts /path/to/repository --title "First local review"
bun src/localreview-open.ts --home
```

The setup command is additive and idempotent. It adds only the
cmux-localreview-managed Copilot skill files and instructions it owns.

### Why does opening `/queue` directly say “Bearer token required”?

The browser UI controls a loopback daemon that requires an authenticated local
session. Open it with `bun src/localreview-open.ts --home`; the CLI appends a
one-time, 60-second bootstrap code in the URL fragment, which the client
immediately exchanges for an HttpOnly cookie and removes. If you are already
on Queue Home, use its local token-recovery control rather than placing the
token in a URL query parameter.

### Why is Queue Home separate from the reviewer?

Queue Home is the global landing page: it aggregates local submissions and
configured remote nodes. A reviewer is intentionally opened for a selected
workspace/item, which keeps the active path and review context explicit.

## Submission and review

### What does a local submission contain?

It contains an immutable Git snapshot, queue metadata, intended base ref,
captured cwd/branch information, and best-effort cmux and agent provenance.
It does not capture terminal transcripts. Snapshot creation uses a temporary
Git index, leaving the source working tree and index unchanged.

### Can I submit a pull request instead of a local path?

Yes:

```sh
bun src/queue-submit.ts https://github.com/OWNER/REPOSITORY/pull/123
```

`gh` must already be authenticated. The daemon resolves the base/head commits,
then creates a managed cache mirror/worktree. Refresh when the PR head changes
before reviewing or publishing a decision.

### Why does a remote PR show no diff or an old diff?

First refresh the item in Queue Home or its detail view. The review is pinned
to an exact remote head, so a changed PR head is intentionally treated as
stale rather than silently reviewed as a different revision. When opening the
reviewer manually, use the base shown on the item (for example `origin/main`)
and reopen the workspace after refresh.

### When should I requeue, complete, or submit again?

- **Requeue**: the same immutable snapshot needs another pass.
- **Complete**, **Approve**, or **Request changes**: review of that exact item
  is finished. It leaves the active queue and remains in history.
- **Remove**: discard an item from the active queue without reviewing it. This
  is safe to use for duplicates or abandoned work; its audit record remains.
- **Submit again**: create a new immutable review round. For local work, use
  the same path and `--topic <stable-name>`; for a remote review, submit the
  same PR URL. The new item links to the old one rather than overwriting it.

Do not treat requeue as a way to update an item’s source state.

### Will approve or request changes write to GitHub?

For a remote PR, these are intentional GitHub Review actions through `gh`.
Inline anchors are used when GitHub accepts them; otherwise the system uses a
summary-review fallback. Confirm the PR URL and current head SHA before using
either action.

## Copilot, ACP, and `/ask`

### What does the setup script install for Copilot CLI?

It installs project-local skills under `.github/skills/` and a bounded managed
section in `.github/copilot-instructions.md`:

- `/localreview-submit`
- `/localreview-feedback`
- `/localreview-reproduce`

Run `/skills reload` in an already-open Copilot CLI session, or start a new
one. Use `--personal` only when you also want the skills under
`~/.copilot/skills`.

### What is the difference between a Copilot session ID and an ACP session ID?

The Copilot session ID is saved for resume guidance. The ACP session ID plus a
loopback host and port identifies the existing structured-agent session that
can receive formal review feedback. Supplying a session ID alone cannot prove
the agent process or tunnel is still alive.

### Why is “Send through ACP” unavailable?

The submission must have included all of `--acp-host`, `--acp-port`, and
`--acp-session-id`, and the referenced ACP agent must still be reachable. An
unavailable/busy/error state is a recovery signal, not a request to fall back
to terminal keystrokes. Copy the feedback prompt or reproduce a fresh setup
when appropriate.

### What does the ACP delivery policy mean?

`queue` is the default: wait for a daemon-issued turn to become idle. Use
`interrupt` only when redirecting a daemon-issued in-flight turn is the right
review decision. Undelivered feedback remains available for retry/copy, which
prevents accidental duplicate delivery.

### Is `/ask` sent to the agent whose code I am reviewing?

No. `/ask` is a separate Copilot SDK conversation with its own persistent
transcript, model/session controls, streaming, and cancellation. It does not
enter exported prompts, ACP delivery, GitHub comments, approvals, or request-
changes payloads unless a reviewer explicitly converts an answer to a formal
review comment.

### How do inline `/ask` questions retain context?

Ask from the selected file, line, or range. The conversation stores the repo,
file, side, line/range, selected code, and its conversation linkage. Follow-up
questions reuse that conversation rather than re-sending a made-up history.

## Reproduction and remote operation

### Can I recreate a review on another machine?

Yes, into a new or empty destination:

```sh
bun src/localreview-reproduce-copilot.ts <queue-id> /tmp/reproduced-review
```

It verifies/materializes the retained snapshot and prints either still-live
ACP connection details (if recorded) or a fresh `copilot --acp --port …`
command. It cannot revive an agent process, SSH forward, or expired login.

### How do I prepare a remote worker without exposing its daemon?

Install the project and run:

```sh
# Remote worker
sh scripts/localreview-remote-daemon.sh --port 4311 --data-dir /srv/localreview

# Local reviewer: print the SSH forward, inspect it, then run it
sh scripts/localreview-remote-tunnel.sh \
  --ssh-target reviewer@worker.example --remote-port 4311 --local-port 5311
```

Add the node in Queue Home using the remote discovery token. The implementation
only forwards `127.0.0.1` endpoints; it does not configure a public listener,
copy a token over SSH, or install a service manager unit for you.

### What can I safely clean up?

Use the remote item’s **Clean up** control for managed worktrees. Removing a
reusable mirror is an explicit additional choice. Never use queue cleanup to
delete a regular developer checkout; resubmit the PR URL later to recreate a
managed remote review.

## Troubleshooting

### Copilot models show unauthenticated or unavailable

Run `gh auth login --hostname github.com` on the machine that runs the daemon.
Queue Home uses that credential in memory by default. You may instead configure
and connect the optional dedicated **Copilot /ask** GitHub App,
then retry model selection. The daemon passes that App token explicitly to the
SDK and disables stored Copilot CLI, `gh`, and environment authentication.
Confirm Copilot CLI is installed and on `PATH` (or set `COPILOT_CLI_PATH` for
the daemon environment). A fresh `/ask` conversation is not an ACP feedback
session.

### GitHub PR submission fails

Confirm `gh auth status --hostname github.com` succeeds (or connect the optional
**PR read** GitHub App), then confirm it is installed on the repository,
then submit or refresh again. Connect **PR publish** only before publishing a
formal review. The queue UI should show its remote error/cache state instead
of hiding it.

### A remote daemon is disconnected

Verify the remote daemon is listening loopback-only, the remote discovery
token is correct, and local SSH batch authentication works. Retry or reconnect
the node from Queue Home. Disconnecting a node stops its use locally; it does
not delete the remote daemon or its queue data.

### I need an agent-friendly status report

Use the handoff template in [Agent guide](AGENT-GUIDE.md#useful-handoff-template).
Include queue ID, status, path/PR, base/head, snapshot, ACP state, and the next
action—never credentials or tokens.
