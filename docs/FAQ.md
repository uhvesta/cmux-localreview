# Frequently asked questions

## Where do I start?

Start the daemon, submit a local snapshot, then open Queue Home:

```sh
localreview daemon run --port 0
localreview submit --title 'First review' --topic first /absolute/path/to/project
localreview open
```

Queue Home is global. A reviewer opens only after choosing **Open workspace**
on an item.

## Does a workspace support multiple Git repositories?

Yes. Queue identity uses the absolute submitted workspace path plus an optional
topic. Submit each independently reviewable repository/path separately; choose
distinct topics when one path carries more than one review stream. The reviewer
shows the actual saved path, rather than a generic “workspace root” label.

## What happens when I resubmit?

The snapshot is immutable. Re-submitting an active stream is idempotent;
resubmitting after a terminal decision or removal creates a new linked round.
It never replaces old source evidence. **Requeue** reopens the same snapshot;
it does not pick up new filesystem changes.

## Can I remove a queue item without reviewing it?

Yes. Use **Remove** on Queue Home. It leaves the active queue without claiming
approval or changes requested. Its small history record remains, while the
saved snapshot is not rewritten.

## Will opening a review resend anything to Copilot?

No. Loading Queue Home, a reviewer, an inline thread, or a saved `/ask`
conversation performs reads only. A prompt starts only after an explicit
question submission; formal feedback starts only through the delivery action.

## What does `/ask` retain?

It retains a dedicated conversation, transcript, selected model settings, and
for inline questions the workspace/repository, file, diff side, line/range,
and selected code. The inline reply and side-chat are the same conversation.
Use **Start fresh** for a new context while keeping older sessions readable.

`/ask` does not enter exports, formal feedback, decisions, or GitHub payloads
unless a reviewer explicitly converts an answer into a formal comment.

## How do model choice and streaming work?

Connect the `copilot` capability, then use the `/ask` picker. The UI shows
authentication/model availability, turn state, and cancellation. A fallback
model choice means the daemon could not load a live SDK model catalogue; it is
not proof that the question was delivered. See [Troubleshooting](TROUBLESHOOTING.md).

## Do I need `gh` or a personal access token?

No. Native authentication uses dedicated GitHub OAuth App client credentials
separated into `read`, `write`, and `copilot` capabilities, stored only in the
OS secret store. The `github-app` command is a compatibility name, not an
installation-token integration. The browser gets neither OAuth tokens nor a
token stored in web storage. `gh`, PATs, environment variables, and existing
Copilot CLI login are not fallbacks.

## Can I inspect a GitHub PR without publishing feedback?

Yes. Connect the `read` capability, then choose **Review locally** in Queue
Home or run:

```sh
localreview open --pr https://github.com/OWNER/REPOSITORY/pull/123
```

This opens the local diff and `/ask` without creating a queue item or a
publication path. A `write` capability is not needed for local review.

For a queued remote PR, **Save locally** records a local queue decision only.
**Publish to GitHub** is a separate, opt-in action that requires both the
dedicated `read` and `write` GitHub App capabilities. Before it writes, the
daemon re-resolves the PR and refuses to publish if its head SHA changed or
the PR is no longer open. A failed publication never becomes a local decision.

## How do I reproduce a submitted review?

```sh
localreview reproduce <manifest.json> /new-or-empty/destination
localreview reproduce --copilot <queue-id> /new-or-empty/destination
```

Reproduction verifies bundles and refuses a non-empty destination. A recorded
Copilot session ID is provenance only: it cannot revive a stopped process,
expired authentication, or old context.

## Can I connect multiple local/remote Copilot CLI ACP sessions?

Not through the native daemon. ACP and cmux keystroke injection are retired
transport paths. The supported route is one native SDK conversation per
review/feedback target, created by an explicit `/ask` or feedback-delivery
action. See [Copilot sessions](COPILOT-ACP-SESSIONS.md) for the boundary and
remote-worker guidance.

## Can I run a remote daemon?

Yes, loopback-only:

```sh
localreview remote daemon run --port 4311 --data-dir /srv/cmux-localreview
```

Run it under your normal supervisor. The native federation transport creates
an ephemeral loopback-only SSH forward on demand; use SSH keys/agent auth and
avoid putting the daemon discovery token in source, shell history, or chat.
