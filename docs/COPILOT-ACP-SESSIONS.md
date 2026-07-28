# Multiple Copilot CLI sessions

> **Retired for the Go runtime:** this is retained only as legacy design
> history. The current migration intentionally removes ACP/URI transport from
> the Go core; `/ask` is SDK-native. See [Go CLI workflows](CLI-WORKFLOWS.md).

This guide describes the current safe setup and the migration target. ACP is
the feedback transport: cmux is for visibility and persistence, never for
keystroke injection.

## Current local setup

Run one ACP listener per Copilot worktree/branch. Choose different loopback
ports and retain each returned ACP `sessionId`.

```sh
# Terminal A: branch feature/parser
copilot --acp --port 4101

# Terminal B: branch feature/ui
copilot --acp --port 4102
```

Use the ACP client/session creation flow for each listener, then submit the
matching worktree with its exact session metadata:

```sh
queue-submit /absolute/path/to/parser --title "Parser" --topic parser \
  --agent-id parser-agent --agent-provider copilot --agent-kind copilot-cli \
  --acp-host 127.0.0.1 --acp-port 4101 --acp-session-id "$PARSER_ACP_SESSION"

queue-submit /absolute/path/to/ui --title "UI" --topic ui \
  --agent-id ui-agent --agent-provider copilot --agent-kind copilot-cli \
  --acp-host 127.0.0.1 --acp-port 4102 --acp-session-id "$UI_ACP_SESSION"
```

Each queue item retains its originating session. **Send through ACP** delivers
only to that item, serializes duplicate delivery, and never runs merely
because you reopen a review. Use `queue` to wait for idle or `interrupt` only
when deliberately redirecting the selected agent.

## Current remote setup

Keep the remote ACP listener private. Create a separate local forward for each
remote session/port, then submit the local forwarded port.

```sh
# On the reviewer machine, one forward per remote ACP listener.
ssh -N -L 127.0.0.1:5101:127.0.0.1:4101 worker.example
ssh -N -L 127.0.0.1:5102:127.0.0.1:4102 worker.example

queue-submit /local/path/for/submitted-state --topic parser \
  --acp-host 127.0.0.1 --acp-port 5101 --acp-session-id "$REMOTE_PARSER_SESSION"
```

For remote PRs, the daemon independently mirrors the pull request with the
dedicated PR-read GitHub App. The ACP forward remains loopback-only and is
only the feedback transport.

## Minimal-copy-paste migration target

The Go CLI will add an `agent start/register` workflow that starts or connects
to Copilot ACP, records cwd/cmux surface/port/session metadata, heartbeats the
agent, and submits the queue item atomically. Queue Home will require one
selected target or an explicit confirmed broadcast and display a separate
delivery ledger for every target. Until that command is shipped, use the
explicit one-item/one-session commands above; do not rely on cmux `send`.

## Safety rules

- Never expose ACP on a public interface; use `127.0.0.1` plus SSH forwarding.
- Do not put daemon tokens, GitHub tokens, or ACP transcript content in shell
  history, queue metadata, or remote nodes.
- Reopening Queue Home, a diff, or an `/ask` transcript must not send a
  prompt to any ACP session.
- `/ask` is a separate fresh Copilot SDK conversation. It does not become
  formal queue feedback unless a reviewer explicitly converts an answer into
  a review comment.
