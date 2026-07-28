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

## Formal feedback delivery

Formal comments remain separate from `/ask`. The reviewer can copy a fully
qualified review prompt or choose **Send through Copilot**. Delivery is
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

Run a loopback-only native daemon on each remote worker and keep normal SSH
operations explicit:

```sh
# On the remote worker.
localreview remote daemon run --port 4311 --data-dir /srv/cmux-localreview
```

Never expose the daemon or a Copilot listener on a public interface. Native
SSH federation remains an operator-managed migration feature, so validate your
own SSH forward, host identity, and supervisor behavior before relying on it.
Do not copy discovery tokens, OAuth credentials, or copied terminal content to
the remote host.

## Authentication boundary

Configure the `copilot` capability using `localreview github-app` or
`localreview auth`. Tokens live only in OS secure storage. The browser UI gets
an HttpOnly local session, not an OAuth token. `gh`, environment variables,
PATs, and an existing Copilot CLI credential are intentionally not fallbacks.
