---
name: localreview-feedback
description: Prepare or explicitly deliver formal cmux-localreview feedback while keeping it separate from /ask.
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
3. To send the feedback, use the queue UI's **Send through Copilot** action.
   Choose `queue` by default; use `interrupt` only when the reviewer explicitly
   wants to redirect an in-flight native SDK turn.
4. Delivery is item-scoped and recorded. Never emulate it with terminal
   keystrokes, cmux `send`, or ACP. Do not retry a delivered batch: inspect
   delivery history first.
5. If ACP is unavailable/busy/error, leave the feedback undelivered and give
   the reviewer the copyable prompt and recovery action. An error must not be
   treated as a delivered review.

Use `/ask` for a fresh, persistent Copilot SDK side conversation. It is
intentionally distinct from formal feedback delivery.
