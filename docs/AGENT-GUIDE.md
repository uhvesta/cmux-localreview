# Agent guide

This is the operating contract for a human or coding agent using the native
review daemon. For exact flags, use [CLI workflows](CLI-WORKFLOWS.md).

## The three durable objects

| Object | Meaning | Safe rule |
| --- | --- | --- |
| Queue item | one immutable snapshot or PR head under review | never mutate it; create a new round for new work |
| Review stream | identity linking rounds | local: absolute workspace path + stable topic; remote: canonical PR URL |
| `/ask` conversation | a separate persisted Copilot SDK transcript | never export or deliver it as formal feedback automatically |

Formal review comments, decisions, exports, and Copilot delivery are a
separate channel. An `/ask` answer becomes formal feedback only when a
reviewer deliberately converts it to a review comment.

## Minimal operator loop

```sh
# Start once in a terminal/supervisor.
localreview daemon run --port 0

# Submit exactly the current Git state.
localreview submit --title 'Review parser change' --topic parser-boundaries \
  /absolute/path/to/project

# Queue Home, then choose Open workspace for the intended item.
localreview open
```

Reopening Queue Home, a queue item, a diff, or an `/ask` transcript is
read-only. It must not send a prompt. Explicit `POST` actions in the UI are
the only prompt/delivery triggers.

## Review protocol

1. Start from Queue Home; select the intended local or remote item.
2. Verify its path/PR, branch/base/head, snapshot ID, and current status.
3. Use the diff to create formal comments. Use `/ask` for investigation and
   keep its questions anchored to a selected file and line/range.
4. Check the feedback prompt before copying or selecting **Send through
   Copilot**. That action batches undelivered formal feedback, shows state,
   and prevents duplicate concurrent delivery. Use `queue` normally;
   `interrupt` is an explicit redirect of a live SDK turn.
5. Choose **Approve**, **Request changes**, **Complete**, **Requeue**, or
   **Remove** only for the item actually open. Remove is not a review result.
6. For changed code, submit again with the same absolute path + topic. That
   creates a linked immutable round rather than overwriting old evidence.

## `/ask` protocol

Use inline `/ask` for a file, line, or range when context matters. The daemon
stores workspace/repository paths, side, selected code, range, and conversation
linkage. The side card and full chat are two views of the same conversation.

- Select a model, reasoning level, and context tier before the first turn if
  the authenticated model catalogue offers them.
- Start fresh when beginning a separate review pass; older sessions remain
  readable history.
- Save named question sets and send them as one numbered question or ordered
  turns into the selected conversation.
- Watch the streaming state and use cancel before replacing an in-flight
  question. If the Copilot capability is unavailable, the question/error is
  recorded; do not retry by reopening the review.

## Authentication and safety

- Use `localreview auth` / `localreview github-app`, not `gh`, PATs, shell
  environment tokens, or shared Copilot CLI login state. `github-app` is a
  compatibility CLI name for the current dedicated GitHub OAuth App client
  setup, not a GitHub App installation.
- OAuth credentials are daemon-only OS-secret-store material. Never paste
  client secrets, discovery JSON, browser URLs, or tokens into a handoff.
- The daemon is loopback-only. A remote worker should use the same command on
  that machine and an explicit operator-owned SSH forward; never bind it
  publicly.
- The native runtime has no ACP/terminal-keystroke feedback transport. Do not
  claim a Copilot CLI session will be resumed simply because it was observed.

## Reproduce and hand off

```sh
localreview reproduce <manifest.json> /new-or-empty/destination
localreview reproduce --copilot <queue-item-id> /new-or-empty/destination
```

The latter reports historical Copilot provenance, but starts a fresh native
SDK conversation if opened. It cannot resurrect a process, OAuth session, or
old context window.

Use this non-secret handoff template:

```text
Queue item: <id>
Stream/topic: <path + topic, or canonical PR URL>
Status: <queued|in review|changes requested|approved|completed|removed>
Source: <absolute workspace path or PR URL>
Base → head: <refs/SHAs>
Snapshot: <id/manifest path>
Formal feedback: <none|draft|copied|delivered>
/ask: <conversation id + state, if useful>
Next explicit action: <action>
```

Never include credentials, daemon capabilities, OAuth state, terminal output,
or unredacted transcripts.
