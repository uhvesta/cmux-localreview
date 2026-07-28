---
name: localreview-submit
description: Capture the current Git worktree as an immutable cmux-localreview queue item, retaining cmux and Copilot ACP session context for a later review-feedback loop.
argument-hint: "[title]"
user-invocable: true
---

<!-- cmux-localreview-managed: skill -->

# Submit a local review

Use this skill when the current branch is ready for a human review in
cmux-localreview. The queue submission is a snapshot: it must describe the
current working directory and not a future or hypothetical revision.

1. Confirm the repository path with `pwd`, `git status --short`, and the
   branch/base information that matters to the review.
2. If the caller supplied a title, use it. Otherwise form a short title from
   the branch/change; do not include secrets or terminal output.
3. Submit from the repository root with the command installed by
   `localreview-setup`. It is normally:

   ```sh
   bun '/Users/avestabarzegar/agentmax/cmux-localreview/src/queue-submit.ts' . --title "<review title>"
   ```

4. If this is an existing Copilot CLI ACP session, supply the three ACP fields
   together: `--acp-host 127.0.0.1 --acp-port <port> --acp-session-id <id>`.
   Include `--copilot-session-id <id>` and an `--agent-id` when known. A remote
   agent must be exposed by an SSH *local* forward first; never submit a
   non-loopback host or a bearer token.
5. Report the returned queue ID and snapshot path. Do not claim that feedback
   was injected: human feedback is delivered later through the queue, using an
   ACP prompt against the retained session.

The submission command captures cwd, Git branch/base/head, immutable snapshot,
cmux surface/workspace provenance (when available), originating-agent metadata,
and ACP/Copilot session identifiers atomically. It never captures terminal
transcripts. Use `--watch` only when the caller explicitly wants a new queued
snapshot for later source revisions.
