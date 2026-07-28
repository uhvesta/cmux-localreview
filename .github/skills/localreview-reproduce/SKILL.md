---
name: localreview-reproduce
description: Materialize a cmux-localreview snapshot safely without claiming a historic Copilot session can resume.
argument-hint: "<queue-id> <empty-directory>"
user-invocable: true
---

<!-- cmux-localreview-managed: skill -->

# Reproduce a queued review

Use this skill to inspect the exact immutable source state behind a queue item.
The target must be new or empty; never overwrite a developer's existing tree.

1. Ask for the queue ID and an empty destination path.
2. Run:

   ```sh
   localreview reproduce --copilot <queue-id> <empty-directory>
   ```

3. Report the materialized cwd and the printed commands. Historic Copilot
   session information is provenance only; do not claim it can be resumed.
4. Open the reproduced workspace with `localreview open <empty-directory>` to
   start a fresh native `/ask` conversation. It does not inherit private old
   context.

Reproduction includes the retained snapshot and recorded metadata; it does not
restore SSH tunnels, Copilot authentication, terminal history, or expired SDK
or ACP processes.
