# Operator setup guide

> **Historical guide:** this page describes the TypeScript/ACP deployment.
> During the Go-daemon migration, use [Go CLI workflows](CLI-WORKFLOWS.md) for
> supported commands. In particular, do not substitute the `bun src/*`, ACP,
> or remote-tunnel examples below for the current `localreview` CLI.

This guide is for a person or automation agent preparing a machine to submit
and review work. Run the command for the machine's role, then verify the
expected result before moving to the next step.

## Roles and prerequisites

| Machine role | Required | Optional integrations |
| --- | --- | --- |
| Local reviewer or submitter | Bun, Git, this checkout, and a system secret store | Copilot CLI runtime, cmux |
| Remote worker | The same tools plus SSH access from the review machine | A service supervisor |
| Existing Copilot feedback target | Copilot CLI signed in, ACP listener, session ID | cmux |

Check the boundaries independently:

```sh
git --version
bun --version
copilot --version
ssh -V
```

`/ask` uses the installed Copilot CLI runtime but does **not** use its stored
login. cmux-localreview passes an explicit daemon-owned GitHub App token with
all other SDK credential sources disabled. On macOS this requires Keychain;
on Linux install a libsecret provider that exposes `secret-tool`.
cmux is optional: without it, snapshots, GitHub PRs, and `/ask` still work,
but cmux provenance and terminal `/btw` routing do not.

## Create and connect the GitHub Apps

Create three GitHub Apps (personal or organization-owned), enable **Device
Flow** on each, and retain their public Client IDs. The CLI prints the exact
registration checklist:

```sh
bun src/localreview-github-app.ts guide
```

Use three different Apps and least privilege:

| Capability | Repository permissions | Used for |
| --- | --- | --- |
| `read` | Metadata read, Contents read, Pull requests read | Resolve, mirror, and locally review PRs |
| `write` | Metadata read, Pull requests read/write | Explicit GitHub review publication only |
| `copilot` | The Copilot user-token permission required by your plan; no repo grants by default | Fresh Copilot SDK `/ask` sessions |

Install each App only on the repositories it needs. Then configure and connect
each one from Queue Home, or on a local/headless host:

```sh
bun src/localreview-github-app.ts configure --capability read --client-id Iv1.…
bun src/localreview-github-app.ts connect --capability read
bun src/localreview-github-app.ts status
```

`connect` opens GitHub’s device-flow page and waits locally. It stores tokens
only under the `cmux-localreview.github-app` system-secret service. It never
uses `gh`, a PAT, environment token, or Copilot CLI credential fallback.
The browser never stores a GitHub token. Its one-time loopback daemon
capability is exchanged for an `HttpOnly; SameSite=Strict` cookie instead of
localStorage, sessionStorage, IndexedDB, or a readable cookie.

The daemon always binds to `127.0.0.1`. Use SSH local forwarding for a remote
daemon or ACP listener; do not make either service public.

## Install Copilot skills in a project

From a checkout of this repository, install project-local skills for the
repository where developers will use them:

```sh
# macOS or Linux: auto-select the platform wrapper
sh scripts/localreview-setup.sh /absolute/path/to/project

# Inspect the planned writes without changing anything
sh scripts/localreview-setup.sh --dry-run --json /absolute/path/to/project

# Also install the skills as personal Copilot CLI skills
sh scripts/localreview-setup.sh --personal /absolute/path/to/project
```

Setup adds or updates only tool-owned files:

```text
.github/skills/localreview-submit/SKILL.md
.github/skills/localreview-feedback/SKILL.md
.github/skills/localreview-reproduce/SKILL.md
.github/copilot-instructions.md  # a bounded managed block only
```

It never overwrites an existing unmanaged skill. Re-run it after upgrading this
repository; it is idempotent. Start a new Copilot CLI conversation or run
`/skills reload` after setup.

| Agent request | Use | Result |
| --- | --- | --- |
| “This branch is ready for review” | `/localreview-submit` | Snapshot + queue item, optionally with ACP binding |
| “Get feedback to the existing agent” | `/localreview-feedback` | Copy-safe feedback or live ACP delivery |
| “Recreate what was reviewed” | `/localreview-reproduce` | Rebuild snapshot and explain what can resume |

The installer does not place credentials, bearer tokens, or terminal output in
project instructions.

## Start Queue Home and submit a local review

Always open the UI through `localreview-open`; it starts the loopback daemon if
needed and places its bearer token in the browser URL fragment.

```sh
# Global home: local and federated queues
bun src/localreview-open.ts --home

# Explicit workspace reviewer, using a chosen base ref
bun src/localreview-open.ts /absolute/path/to/project --base origin/main

# Capture an immutable snapshot from the terminal
bun src/queue-submit.ts /absolute/path/to/project --title 'Review parser change'

# Continuously queue later Git source revisions
bun src/queue-submit.ts /absolute/path/to/project --watch --poll-interval 5000
```

The watcher creates a new item only when the Git source state changes. Snapshot
capture uses a temporary index and does not change the source worktree, index,
or `HEAD`.

## Copilot: `/ask` versus existing-session ACP feedback

`/ask` is a new persistent Copilot SDK conversation. It has its own model
picker, transcript, question sets, and inline code anchors. It is deliberately
separate from formal review feedback unless a reviewer explicitly converts an
answer into a comment.

For an inline question such as `/ask why could this be risky?`, the retained
Copilot session receives the workspace root, selected repository root,
repository-relative/workspace-relative/absolute file paths, diff side, exact
line or range, and selected source text. This makes follow-ups reliable across
files and in multi-repository workspaces. The inline card and full `/ask`
panel are two views of the same saved transcript.

Use **Start fresh** in `/ask` for a clean Copilot context. Older Ask sessions
remain available through **Show previous Ask sessions** but are read-only, so
they cannot accidentally affect new review work. **New Review** also starts a
new Ask round. Older formal comments are hidden by default; **Show previous
comments** exposes them as explicitly outdated, read-only history.

For feedback to reach an existing Copilot CLI agent, run that agent in ACP TCP
mode, retain the session ID returned by its ACP client, then submit all ACP
coordinates together:

```sh
# Agent terminal
copilot --acp --port 4123

# Submission terminal
bun src/queue-submit.ts . --title 'Review feature branch' \
  --agent-kind copilot-cli --agent-id feature-123 \
  --copilot-session-id "$COPILOT_SESSION_ID" \
  --acp-host 127.0.0.1 --acp-port 4123 --acp-session-id "$ACP_SESSION_ID"
```

`--acp-host`, `--acp-port`, and `--acp-session-id` are all required together.
Use **Copy feedback prompt** for a portable prompt. Use **Send through ACP**
only while the original listener/session still exists. The `queue` policy waits
for an in-flight daemon-issued turn; `interrupt` cancels that turn before
delivery. Delivered feedback is recorded so retries do not duplicate it.

## Review a GitHub PR

Connect the **PR read** App and use a canonical PR URL:

```sh
bun src/localreview-github-app.ts status
bun src/queue-submit.ts https://github.com/OWNER/REPOSITORY/pull/NUMBER
```

The daemon resolves the repository and base/head SHAs, creates a managed mirror
and cache worktree, and pins review to that head. Use **Refresh remote PRs**
after a push; a stale head cannot receive an approval or change request. Use
the remote clean-up controls only for cache-owned worktrees and mirrors.

### Ask Copilot about a PR without entering the review queue

Use this when you want a local, question-only inspection: it resolves the PR,
creates an isolated cached worktree, and opens the diff with `/ask` available.
It does **not** create a queue item or snapshot, attach an ACP agent, or make
GitHub review submission controls available in the browser.

```sh
# Connect read + copilot capabilities once, then:
bun src/localreview-open.ts --pr https://github.com/OWNER/REPOSITORY/pull/NUMBER
```

The resulting reviewer URL has no daemon bearer token and is accepted only on
`127.0.0.1`. Open `/ask`, choose a model, and ask a side-chat or inline code
question. The PR read App is required to resolve and clone a private PR; no
GitHub write capability is required or used in this mode.

## Remote daemon and SSH forwarding

Set up the checkout and skills on the remote worker as above. Start the remote
daemon under your normal supervisor:

```sh
# Remote host
sh scripts/localreview-remote-daemon.sh --port 4311 --data-dir /srv/localreview

# Local review machine: print a safe local forward, then run it
sh scripts/localreview-remote-tunnel.sh \
  --ssh-target reviewer@worker.example --remote-port 4311 --local-port 5311
```

The generated command uses `ssh -N -L 127.0.0.1:<local>:127.0.0.1:<remote>`,
`BatchMode=yes`, and `ExitOnForwardFailure=yes`. Add the node in Queue Home
with its label, SSH target, remote port, and remote discovery token. The token
comes from the remote owner-only `daemon.json`; transfer it only through an
approved secret path. Queue Home connects lazily and offers retry/disconnect.

For a remote ACP agent, create a separate SSH local forward to its ACP port and
submit that *local* forwarded port. Public and LAN ACP hosts are rejected.

## Reproduce a saved review

The queue detail view shows the exact command. The CLI destination must be new
or empty:

```sh
bun src/localreview-reproduce-copilot.ts QUEUE_ID /tmp/reproduced-review
```

This verifies saved bundle checksums and materializes the submitted Git state.
It can provide a fresh `copilot --acp --port …` launch command, but cannot
revive a closed Copilot session, disappeared remote host, expired GitHub login,
or an old cmux surface.

## Security and maintenance

- Keep the owner-only discovery file and remote federation tokens out of source
  control, browser URLs, tickets, and shell history.
- Queue records cmux surface metadata, not terminal transcripts; supplied
  metadata is redacted for credential-like fields.
- `/ask` never enters exports, queue feedback, ACP delivery, or GitHub review
  payloads unless a reviewer explicitly converts an answer into a comment.
- Prune completed queue entries and remote cache worktrees through the UI only
  after their audit/reproduction value is no longer needed.

## Agent readiness check

Run the relevant checks before claiming a machine is ready:

```sh
bun test
bun run typecheck
bun run build
bun src/localreview-github-app.ts status
copilot --version
```

Finally, open Queue Home using `bun src/localreview-open.ts --home`, submit a
throwaway local repository, open the explicit workspace reviewer, and confirm
that a diff is visible. See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) if any
check fails.
