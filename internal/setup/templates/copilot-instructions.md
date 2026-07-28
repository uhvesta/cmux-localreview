# cmux-localreview integration

Use `/localreview-submit` to capture an immutable local review snapshot. It
must use the installed native `localreview` command, never historical
the retired TypeScript runtime, ACP, cmux keystrokes, or terminal injection.

Formal review feedback is distinct from `/ask` side-chat: do not export or
deliver exploratory `/ask` messages unless the reviewer explicitly converts an
answer into a formal review comment. Reopening a queue item, diff, or `/ask`
transcript is read-only and must never resend a prompt.

Use `/localreview-reproduce` only with a new or empty destination. It creates
a fresh native `/ask` conversation when opened; it cannot resume an old
Copilot CLI/ACP process, terminal history, SSH tunnel, or authentication.
