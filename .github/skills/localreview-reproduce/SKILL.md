---
name: localreview-reproduce
description: Materialize a cmux-localreview snapshot and explain or launch a fresh Copilot ACP setup without pretending a dead session can be resumed.
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
   bun '/Users/avestabarzegar/agentmax/cmux-localreview/src/localreview-reproduce-copilot.ts' <queue-id> <empty-directory>
   ```

3. Report the materialized cwd and the printed commands. If the saved ACP
   session is still live, report its loopback endpoint and session ID as
   connection information only. Do not claim that it can be resumed unless a
   live connection confirms it.
4. For a fresh agent, use the printed `copilot --acp --port <port>` launch
   command from the reproduced directory, then create a new session. The fresh
   session does not inherit the old session's private context.

Reproduction includes the retained snapshot and recorded metadata; it does not
restore SSH tunnels, Copilot authentication, terminal history, or expired ACP
processes.
