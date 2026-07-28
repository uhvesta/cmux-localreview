# Copilot conversations, CLI sessions, and remote workers

## Supported native model

The Go daemon uses fresh, persisted Copilot SDK conversations. It deliberately
does **not** attach to Copilot CLI ACP sessions or inject terminal keystrokes
through cmux. This avoids routing a prompt into a tool call, permission prompt,
or the wrong terminal.

There are two distinct native conversation uses:

| Use | Starts when | Context | Does reopening send anything? |
| --- | --- | --- |
| `/ask` | reviewer explicitly sends a question | selected workspace/file/range plus durable `/ask` transcript | No |
| Formal feedback delivery | reviewer explicitly sends queued formal feedback | feedback prompt for exactly one queue item | No |

Both use the dedicated `copilot` OAuth capability and show unavailable/busy/
streaming states. Neither inherits a Copilot CLI login or external ACP session.

## Multiple local review conversations

Submit one item per independently reviewable workspace/topic, then start or
select a `/ask` conversation on each item. The queue identity prevents rounds
from being overwritten:

```sh
localreview submit --topic parser /work/parser
localreview submit --topic ui /work/ui
localreview open
```

In Queue Home, open the intended item. The `/ask` model picker governs its
fresh SDK conversation. Use **Start fresh** for a new review pass and select
old sessions only to read history. Inline questions across files use the same
chosen conversation when the reviewer selects it; they retain their exact code
anchor without resending prior transcript messages.

### Practical multi-session runbook

There are two intentionally different forms of parallelism:

| You need | Supported setup | What is not connected automatically |
| --- | --- | --- |
| Parallel coding agents | Run one Copilot CLI in each branch/worktree/cmux surface. Run `localreview setup` in each project before submitting its snapshot. | The daemon never reads, resumes, or sends prompts to those terminal sessions. |
| Parallel review research | Submit distinct workspace/topic queue items and create a `/ask` conversation for each review item. | A reopened item reads its transcript only; it does not replay a question or copy context into another item. |
| Parallel machines | Run one loopback daemon and one local secure-store authorization per machine, then add the remote daemon through SSH federation. | OAuth credentials, daemon discovery tokens, and Copilot CLI sessions are never copied across hosts. |

For a remote worker that also needs `/ask`, authorize its daemon on that
worker—normally with device flow—and install the Copilot CLI there. A local
daemon's `copilot` credential is not a remote credential:

```sh
# On the remote worker, after installing localreview and Copilot CLI.
localreview remote daemon run --port 4311 --data-dir /srv/cmux-localreview
localreview auth login --capability copilot --client-id YOUR_REMOTE_COPILOT_CLIENT_ID --device
localreview auth status
```

Keep each daemon's data directory private to that host. The native runtime can
keep durable conversations for multiple review items, but users should not
infer a throughput guarantee or that an existing Copilot CLI terminal session
will be reused. Use Queue Home's busy/error state and explicit retry/cancel
controls for each review turn.

## Formal feedback delivery

Formal comments remain separate from `/ask`. The reviewer can copy a review
prompt whose file anchors are relative to the submitted workspace, or choose
**Send through Copilot**. Delivery is
item-scoped, explicit, serialized, and written to its delivery history. `queue`
waits for the current SDK turn; `interrupt` is an explicit redirect policy.

Do not use `/ask` as a way to accidentally approve, request changes, publish
to GitHub, or deliver formal feedback. Converting an answer to a formal comment
is the deliberate boundary.

## Existing Copilot CLI sessions

You may still run multiple Copilot CLI sessions in cmux for coding work, for
example one per branch. They are separate from cmux-localreview’s native
conversation system:

```sh
cd /work/parser && copilot
cd /work/ui && copilot
```

Install project skills with `localreview setup /work/parser` and
`localreview setup /work/ui`, then use `/localreview-submit` to create the
corresponding immutable queue items. The skills do not capture session IDs or
send feedback into a live terminal. Copy a formal feedback prompt when a human
needs to paste it into one of these CLI sessions.

## Remote workers

Run a loopback-only native daemon on each remote worker:

```sh
# On the remote worker.
localreview remote daemon run --port 4311 --data-dir /srv/cmux-localreview
```

Never expose the daemon or a Copilot listener on a public interface. Queue
Home's native federation transport creates a short-lived loopback SSH forward
on demand; validate SSH host identity and supervisor behavior before relying
on it. Do not copy discovery tokens, OAuth credentials, or copied terminal
content to the remote host.

## Authentication boundary

Configure the `copilot` capability using `localreview github-app` or
`localreview auth`. The former is a compatibility command name: today it
configures a dedicated GitHub OAuth App client rather than a GitHub App
installation. Tokens live only in OS secure storage. The browser UI gets an
HttpOnly local session, not an OAuth token. `gh`, environment variables, PATs,
and an existing Copilot CLI credential are intentionally not fallbacks.

## Validation boundary

The deterministic `localreviewd-e2e` fixture exercises queue, transcript,
streaming, and multi-conversation UI mechanics without contacting GitHub or
Copilot. It is not evidence that a self-hosted OAuth registration can obtain
private repository access or that Copilot will complete a real turn. Follow
[GitHub OAuth setup](GITHUB-OAUTH-SETUP.md#important-current-limitation) for
the production preflight and test the exact intended capability on each host.
