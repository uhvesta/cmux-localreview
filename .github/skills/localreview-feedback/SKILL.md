---
name: localreview-feedback
description: Prepare or deliver formal cmux-localreview feedback to the exact retained ACP session, safely distinguishing copyable review feedback from separate Copilot SDK /ask conversations.
argument-hint: "<queue-id> [copy|send]"
user-invocable: true
---

<!-- cmux-localreview-managed: skill -->

# Deliver formal review feedback

Use this skill only for formal review comments on a submitted queue item. Do
not include `/ask` questions, answers, or exploratory side-chat transcript in
formal review feedback unless the reviewer explicitly converts an answer into a
review comment.

1. Inspect the queue item and its pending feedback. Preserve file/line anchors
   where they exist.
2. For a portable prompt, use the queue UI's **Copy feedback prompt** action or
   `GET /api/queue/<id>/feedback/prompt`. Copying never changes the agent.
3. To send the feedback to a still-running agent, use the queue UI's **Send
   through ACP** action. Choose `queue` by default; it waits for a daemon-issued
   ACP turn to become idle. Choose `interrupt` only when the reviewer explicitly
   wants to cancel that turn and redirect it.
4. ACP delivery uses one structured `prompt` call to the existing `sessionId`.
   Never emulate it with terminal keystrokes, cmux `send`, or a new chat
   session. Do not retry a delivered batch: inspect delivery history first.
5. If ACP is unavailable/busy/error, leave the feedback undelivered and give
   the reviewer the copyable prompt and recovery action. An error must not be
   treated as a delivered review.

Use `/ask` for a fresh, persistent Copilot SDK side conversation. It is
intentionally distinct from existing-agent ACP feedback.
