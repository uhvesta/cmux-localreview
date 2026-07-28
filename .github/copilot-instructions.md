<!-- cmux-localreview:start -->
<!-- cmux-localreview-managed: project instructions -->
# cmux-localreview integration

When a user asks to submit the current work for local review, use the
`/localreview-submit` skill. Submit an immutable snapshot from the current
repository instead of describing mutable work from memory. Preserve cwd,
branch/base/head and cmux provenance, but never collect tokens, terminal
transcripts, credentials, or ACP/session metadata.

Formal review feedback is distinct from `/ask` side-chat. `/ask` conversations
must never be exported, sent through Copilot, posted as GitHub review comments,
or included in approval/request-changes payloads unless the reviewer explicitly
converts an answer into a review comment. Native feedback uses the explicit
queue delivery action, never ACP or raw terminal keystrokes.

Use `/localreview-reproduce` to inspect an immutable queued snapshot and be
explicit about what cannot be resumed (an expired SDK/CLI process,
authentication, SSH tunnel, or terminal history).

## Installed command

To submit a review from this checkout, run:

```sh
localreview queue-submit . --title "<review title>"
```
<!-- cmux-localreview:end -->
