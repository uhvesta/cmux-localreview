<!-- cmux-localreview:start -->
<!-- cmux-localreview-managed: project instructions -->
# cmux-localreview integration

When a user asks to submit the current work for local review, use the
`/localreview-submit` skill. Submit an immutable snapshot from the current
repository instead of describing mutable work from memory. Preserve cwd,
branch/base/head, cmux provenance, and any supplied Copilot ACP/session
metadata, but never collect tokens, terminal transcripts, or credentials.

Formal review feedback is distinct from `/ask` side-chat. `/ask` conversations
must never be exported, sent through ACP, posted as GitHub review comments, or
included in approval/request-changes payloads unless the reviewer explicitly
converts an answer into a review comment. Existing-agent feedback must use the
queue's structured ACP delivery, not raw terminal keystrokes.

Use `/localreview-reproduce` to inspect an immutable queued snapshot and be
explicit about what cannot be resumed (an expired ACP process, authentication,
SSH tunnel, or terminal history).

## Installed command

To submit a review from this checkout, run:

```sh
bun '/Users/avestabarzegar/agentmax/cmux-localreview/src/queue-submit.ts' . --title "<review title>"
```
<!-- cmux-localreview:end -->
