# Agent guide

> **Migration note:** the agent policy remains useful, but its `bun src/*`,
> ACP, and remote instructions describe the legacy control plane. Use
> [Go CLI workflows](CLI-WORKFLOWS.md) as the executable command reference
> until Go route/CLI parity is complete.

This guide is the operating contract for an agent working with a
`cmux-localreview` checkout. It complements the command reference in the
[README](../README.md); use it to decide what to do, what data to preserve,
and which action is safe to take.

## Mental model

A **queue item** is an immutable review submission. For a local repository it
contains a snapshot of the submitted Git state. For a GitHub pull request it
uses a managed mirror/worktree pinned to the resolved head. The queue item is
therefore the thing a reviewer acts on, rather than a mutable directory on an
agent's machine.

A **review stream** is the durable identity across those immutable items. A
local stream is the absolute workspace path plus a stable topic; a remote
stream is the canonical GitHub PR URL. Use `--topic` whenever a workspace has
more than one independently reviewable change. A new submission with the same
stream identity links back to the prior item as a new review round rather than
overwriting its snapshot.

There are two intentionally separate conversation channels:

| Channel | Purpose | Can change review outcome? |
| --- | --- | --- |
| `/ask` | Explore code, ask follow-up questions, and keep a Copilot SDK transcript. | Only after the reviewer explicitly converts an answer to a review comment. |
| Formal feedback | Review comments, approve/request-changes decisions, exported prompts, and ACP delivery to the originating agent. | Yes. |

Never copy an `/ask` transcript into formal feedback automatically. A human
reviewer must make that conversion deliberately.

## Before doing work

1. Start at **Queue Home** rather than opening a workspace reviewer by
   default. It shows local and remote items and makes selecting a reviewer an
   explicit action.
2. Open Queue Home through the CLI, not by guessing a daemon URL:

   ```sh
   bun src/localreview-open.ts --home
   ```

   This starts or discovers the loopback daemon and places its bearer token in
   the browser URL fragment. Do not paste that URL into tickets, logs, or chat.
3. Treat absolute paths, snapshot IDs, branch/base/head SHAs, and queue IDs as
   provenance. Preserve them in handoffs; do not replace them with a vague
   label such as “workspace root.”

## Normal agent workflows

### 1. Configure a repository once

Run the portable setup wrapper from this checkout. It installs managed Copilot
skills and a bounded section in `.github/copilot-instructions.md`; it does not
replace user-owned instructions or skill files.

```sh
sh scripts/localreview-setup.sh /path/to/repository
```

For an audit-only run:

```sh
sh scripts/localreview-setup.sh --dry-run --json /path/to/repository
```

After setup, a Copilot CLI session may need `/skills reload` (or a new session)
before `/localreview-submit`, `/localreview-feedback`, and
`/localreview-reproduce` are available.

### 2. Submit work for review

Use the project skill when operating inside Copilot CLI, or use the CLI
directly. A local submission captures a snapshot before the queue request.

```sh
bun src/queue-submit.ts /path/to/repository \
  --title "Review parser change" --topic parser-boundaries --base origin/main
```

When an already-running Copilot ACP session should receive feedback later,
pass every ACP field together. The ACP host must be loopback; for a remote
agent, make an SSH local forward first.

```sh
bun src/queue-submit.ts /path/to/repository --title "Review parser change" \
  --agent-id parser-agent --agent-provider copilot --agent-kind copilot-cli \
  --copilot-session-id "$COPILOT_SESSION_ID" \
  --acp-host 127.0.0.1 --acp-port 4123 --acp-session-id "$ACP_SESSION_ID"
```

For an independently changing local worktree, add `--watch`. It queues only a
new Git source fingerprint; it does not mutate the old snapshot.

```sh
bun src/queue-submit.ts /path/to/repository --watch --poll-interval 5000
```

To queue a GitHub PR, submit its URL after connecting the dedicated **PR
read** GitHub App capability. The daemon manages the mirror/worktree cache;
it never falls back to `gh`, a PAT, or a Copilot CLI login.

```sh
bun src/queue-submit.ts https://github.com/OWNER/REPOSITORY/pull/123
```

### 3. Review an item

From Queue Home, select an item and choose **Open workspace**. In the reviewer:

1. Confirm the displayed path, base/head/branch metadata, and snapshot or
   remote cache status. Local queue items open a daemon-owned materialization
   of the retained snapshot and compare its synthetic snapshot commit to its
   captured parent—never to edits made later in the source directory.
2. Inspect the diff and leave formal review comments.
3. Use **Copy feedback prompt** when a human or another tool should receive
   the feedback manually.
4. Use **Send through ACP** only when the item displays the intended live ACP
   endpoint/session. Choose `queue` to wait for idle, or `interrupt` only when
   it is appropriate to cancel a daemon-issued in-flight turn.
5. Use approve, request changes, complete, or requeue only for the queue item
   actually open. Remote decisions are local by default; use the separately
   labelled, confirmed publish controls when you intend a GitHub
   review, so they are deliberate external actions.

If ACP shows busy, error, or unavailable, do not keep retrying blindly. The
queue keeps undelivered feedback so it can be retried or copied without
silently duplicating delivery.

### 3a. Finish, remove, or start a new review round

Approve, request changes, and complete are terminal decisions: the item leaves
the **active** queue immediately and remains in history for provenance. Use
**Remove** on Queue Home to dismiss an item without claiming it was reviewed;
this also removes it from the active queue but retains a small audit record.
Neither action deletes the snapshot from disk. Re-submit the same path + topic
(or PR URL) to create a fresh immutable round linked to the earlier item.

### 4. Ask code questions without changing the review

Use `/ask` for investigation. Create or select a named question set for a
longer review thread, then send it either as one numbered prompt or sequential
turns. For a specific location, create an inline question from a file, line,
or range. Its repository, file, side, range, selected code, and conversation
linkage are retained with the `/ask` transcript.

An agent should make the question precise and include only the code context the
UI supplied. Do not invent file paths, line numbers, or a review conclusion.
If the answer identifies an actionable issue, let the reviewer choose
**Convert to review comment**; that is the boundary that allows it into formal
feedback.

### 5. Reproduce a review elsewhere

Use a new or empty destination. Reproduction verifies and materializes the
stored snapshot; it does not overwrite an existing project.

```sh
bun src/localreview-reproduce-copilot.ts <queue-id> /tmp/reproduced-review
```

The output distinguishes a still-live saved ACP endpoint/session from the
fresh command for a new session. A live endpoint may be unavailable after a
machine restart, tunnel loss, or agent exit; a saved `sessionId` alone does
not guarantee resumability.

### 6. Work with a remote daemon

On the remote host, run the daemon loopback-only under your normal process
supervisor:

```sh
sh scripts/localreview-remote-daemon.sh --port 4311 --data-dir /srv/localreview
```

On the local review machine, print the safe local SSH forward and run it:

```sh
sh scripts/localreview-remote-tunnel.sh \
  --ssh-target reviewer@worker.example --remote-port 4311 --local-port 5311
```

Then add the node in Queue Home with its remote discovery token. The command
uses `127.0.0.1` on both ends, `BatchMode=yes`, and
`ExitOnForwardFailure=yes`; do not change it into a public daemon listener.

## Safety boundaries

- The daemon is a loopback control plane. Keep its discovery JSON and bearer
  token owner-only. Browser tokens belong in URL fragments, never requests,
  issue comments, or agent output.
- Snapshots are immutable. Requeue or submit a new revision; never alter a
  saved snapshot to represent later work.
- ACP delivery uses structured session prompts, not terminal keystroke
  injection. Do not emulate this with `cmux send`.
- A remote PR can become stale if its head changes. Refresh it before making a
  decision, and do not claim a review applies to a different head SHA.
- Cache cleanup applies only to managed remote worktrees/mirrors. Do not point
  cleanup at a developer checkout.
- `gh` credentials, Copilot credentials, daemon tokens, and terminal
  transcripts are not submission metadata. Never add them to titles, bodies,
  agent metadata, or documentation examples.

## Useful handoff template

Use this compact, non-secret handoff when another agent or reviewer needs to
continue work:

```text
Queue item: <id>
Title/status: <title> / <status>
Workspace or PR: <absolute path or PR URL>
Base → head: <base SHA/ref> → <head SHA>
Snapshot: <snapshot id or manifest path>
ACP: <idle|busy|error|unavailable>; session retained: <yes|no>
Next action: <review, refresh, reproduce, or deliver feedback>
```

Do not include the daemon token, an ACP bearer token, or copied terminal
output in this handoff.
