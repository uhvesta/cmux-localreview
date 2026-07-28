---
name: localreview-feedback
description: Deliver formal cmux-localreview feedback safely.
user-invocable: true
---

# Deliver formal review feedback

Keep `/ask` exploration separate from formal review feedback. Prefer copying a
feedback prompt when explicit native Copilot delivery is unavailable. Never use
terminal keystrokes, cmux send, or ACP as a feedback fallback. Delivery is
explicit, item-scoped, and recorded; reopening a reviewer must not retry it.
Use the reviewer’s **Copy feedback prompt** or **Send through Copilot** action;
choose queue delivery normally and interrupt only when the reviewer explicitly
wants to cancel an in-flight native SDK turn.
