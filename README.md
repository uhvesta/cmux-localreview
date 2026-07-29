# cmux-localreview

`cmux-localreview` is a local multi-repository review UI with persistent comments, `/btw`, and a global local-review queue.

## Guides

- [Agent guide](docs/AGENT-GUIDE.md) — operating model, safe agent workflows, handoffs, and review boundaries.
- [FAQ](docs/FAQ.md) — concise setup, recovery, Copilot/ACP, GitHub, and remote-daemon answers.
- [Setup](docs/SETUP.md) — macOS/Linux configuration, Copilot skills, remote nodes, and security boundaries.
- [CLI playbook](docs/CLI-PLAYBOOK.md) — copyable human/agent commands for setup, queueing, reproduction, and recovery.
- [Demo and source install](docs/DEMO.md) — disposable browser demo plus a user-prefix CLI installation flow.
- [Troubleshooting](docs/TROUBLESHOOTING.md) — actionable recovery for auth, diff, Copilot, GitHub, cmux, and SSH states.

## Common workflow

```sh
# First-time local review: install managed skills, start the daemon, capture a
# snapshot, queue it, and open the exact review item.
bun src/localreview-start.ts /path/to/workspace --setup-copilot --submit \
  --title "Review parser change"

# Start/open an explicit workspace reviewer (starts the daemon when needed)
bun src/localreview-open.ts /path/to/workspace

# Open the daemon-wide Queue Home at / (including lazy federated queue summaries)
bun src/localreview-open.ts --home

# Capture an immutable local snapshot and enqueue it
bun src/queue-submit.ts /path/to/workspace --title "Review parser change"

# Keep monitoring a submitted worktree; only a changed Git source state makes
# a new immutable queue item (the prior queued item is marked superseded).
bun src/queue-submit.ts /path/to/workspace --watch --poll-interval 5000

# A GitHub PR URL is accepted too; it is mirrored under ~/.cache/cmux-localreview
bun src/queue-submit.ts https://github.com/org/repo/pull/123

# Reconstruct a retained snapshot elsewhere
bun src/localreview-reproduce.ts /path/to/manifest.json /tmp/reproduced-review

# Reconstruct a queued snapshot and print/start a Copilot ACP setup
bun src/localreview-reproduce-copilot.ts <queue-id> /tmp/reproduced-review

# Install Copilot CLI skills and project instructions in a repository.
# This is idempotent and never replaces an unmanaged user file.
bun src/localreview-setup.ts /path/to/workspace

# Optional: install the same skills for all local Copilot CLI projects too.
bun src/localreview-setup.ts /path/to/workspace --personal
```

The daemon only binds `127.0.0.1`. Its discovery file is
`~/.local/share/cmux-localreview/daemon.json`, is written atomically with mode
`0600`, and holds the bearer token used by queue/workspace/agent control APIs.
`localreview-open` spends that token locally to mint a separate, one-time,
60-second browser bootstrap code. The bootstrap code is placed in a URL
fragment, immediately exchanged for an `HttpOnly; SameSite=Strict` loopback
cookie, and scrubbed from the address bar; the discovery token never enters a
browser URL.

## Copilot CLI setup and skills

Run `localreview-setup <workspace>` once in each repository you want to submit
from. It installs these project-local Copilot CLI skills under
`.github/skills/` and appends one bounded, managed section to
`.github/copilot-instructions.md`:

| Skill | Use |
| --- | --- |
| `/localreview-submit` | Capture and submit the current Git state, cmux provenance, and optional live Copilot ACP binding. |
| `/localreview-feedback` | Keep formal feedback separate from `/ask`; copy it or deliver it through the retained ACP session. |
| `/localreview-reproduce` | Materialize a saved snapshot and start a fresh Copilot ACP setup when a prior session is no longer live. |

## Persistent inline Copilot `/ask`

Inline `/ask` sends Copilot the workspace root, selected repository root,
repository-relative/workspace-relative/absolute file paths, diff side, exact
line or range, and highlighted source text. Every inline question in the
active review round uses one retained Copilot SDK session. The inline card and
**Open full /ask chat** show the same saved conversation, including questions
asked on other files.

Use **Start fresh** in `/ask` for a clean context while retaining the old one
under **Show previous Ask sessions**. Starting **New Review** creates a new
formal and Ask round; previous Ask sessions and review comments are hidden by
default but remain available as read-only, explicitly outdated history.

The setup command defaults to this checkout's `bun src/queue-submit.ts` entry
so it works immediately without a global package install. Set
`--command localreview-submit` after installing the package's executable (for
example with `bun link`), or give any explicit command with `--command`. Use
`--reproduce-command` for the corresponding reproduction executable.
`--dry-run` reports each proposed file operation, `--json` emits the same
result for automation, and `--personal` also copies skills to
`~/.copilot/skills`. Existing user-owned skill files are always preserved;
the installer automatically refreshes only the clearly marked, tool-owned
skill files. After installing while Copilot CLI is already open, run
`/skills reload` (or start a new session), then invoke `/localreview-submit`.

The bundled source assets live in `copilot/skills/` and
`copilot/copilot-instructions.md` for review and customization. They follow
GitHub Copilot CLI's project-skill convention; no credentials, daemon bearer
tokens, or terminal transcripts are written to instruction files.

## One-command macOS/Linux setup

The portable entry point is `localreview-setup` (or
`bun src/localreview-setup.ts`). It installs or updates the project Copilot
skills every time it runs, while preserving any file it did not create. The
shell wrappers only check for Bun and delegate to that same entry point; they
do not install packages, change shell profiles, alter Git configuration, or
overwrite Copilot configuration.

```sh
# macOS or Linux: detect the OS and configure this repository.
sh scripts/localreview-setup.sh /path/to/workspace

# Explicit wrappers when provisioning a known operating system.
sh scripts/localreview-setup-macos.sh /path/to/workspace
sh scripts/localreview-setup-linux.sh /path/to/workspace

# Audit without writing; CI/provisioners can use structured output.
sh scripts/localreview-setup.sh --dry-run --json /path/to/workspace
```

## Remote daemon and SSH forwarding

Install the checkout and run the same setup command on each remote Linux or
macOS worker. Start its daemon on a fixed **loopback-only** port; the discovery
file and bearer token remain owner-only on the remote host.

```sh
# On the remote host (keep this process supervised by your normal service tool)
sh scripts/localreview-remote-daemon.sh --port 4311 --data-dir /srv/localreview

# On the local review machine: print a safe SSH local forward, then run it.
sh scripts/localreview-remote-tunnel.sh \
  --ssh-target reviewer@worker.example --remote-port 4311 --local-port 5311

# Equivalent commands and machine-readable environment help
bun src/localreview-remote.ts env --data-dir /srv/localreview --shell
bun src/localreview-remote.ts tunnel --ssh-target reviewer@worker.example \
  --remote-port 4311 --local-port 5311 --json
```

The generated SSH command uses `-L 127.0.0.1:<local>:127.0.0.1:<remote>`,
`BatchMode=yes`, and `ExitOnForwardFailure=yes`. It never opens the remote
daemon to the network. Add the remote daemon through Queue Home's federation
controls with the remote discovery token, then retry/disconnect from that UI
as needed. Remote setup intentionally does not copy a token over SSH or write
service-manager configuration; use your existing secure host provisioning and
supervisor for those actions.

Run the standalone reviewer with `bun src/cli.ts [workspace]`; run the daemon
explicitly with `bun src/global-daemon.ts`. Build the client with `bun run build`.

Snapshots use a temporary Git index, leaving the source repository's HEAD,
index, and working tree untouched. Each retained manifest includes SHA-256
checks for its self-contained Git bundles.

## GitHub PR reviews

### Authenticate with your existing GitHub CLI login

On a local review machine, Queue Home uses the credential already held by an
authenticated `gh` CLI. Run this once if needed:

```sh
gh auth login --hostname github.com
localreview-github-app status
```

The daemon asks `gh` for a credential only while a GitHub request is running.
It keeps that value in process memory only: not in the browser, URL, daemon
database, discovery file, environment, project files, or logs. It is used for
PR reads, explicit GitHub publication, and fresh Copilot SDK `/ask` sessions.

Dedicated GitHub Apps remain an optional least-privilege override when a
shared machine or organization policy calls for separate **PR read**, **PR
publish**, or **Copilot /ask** identities. Create the App registrations once
(device flow must be enabled), then connect an override in Queue Home or run:

```sh
bun src/localreview-github-app.ts guide
bun src/localreview-github-app.ts configure --capability read --client-id Iv1.…
bun src/localreview-github-app.ts connect --capability read
```

If an optional App is connected, its issued access and refresh tokens stay only
in macOS Keychain or Linux libsecret, under the daemon's service name. The
browser never receives a GitHub token. Its local daemon capability is immediately
exchanged for an `HttpOnly; SameSite=Strict` loopback session cookie; it is not
written to web storage. The public App client IDs are the only values written
to daemon configuration. See [the operator setup guide](docs/SETUP.md)
for optional App permissions and headless setup.

The daemon resolves the PR's repository plus base/head SHAs before cloning,
then checks out that exact head in a managed cache worktree. Re-submitting the
same PR URL refreshes the review stream; when its head changes, a new immutable
review round linked to the prior item returns to the queue. `POST
/api/queue/:id/refresh` performs the same refresh explicitly, while `POST
/api/queue/:id/cleanup` removes the managed worktree (pass `{ "removeMirror":
true }` only when its reusable mirror should be discarded too).

Publishing is through GitHub’s Reviews API using the separate publish App and
is never implied by a local approve/request-changes lifecycle action; the UI
requires an explicitly labelled, confirmed publish action. A
stale or invalid inline anchor is rejected rather than silently publishing
against a newer head or changing transport.

## Submission context and automatic queueing

`queue-submit` records the submitting terminal's `CMUX_SURFACE_ID` and
`CMUX_WORKSPACE_ID`, then performs a short best-effort `surface.list` lookup.
It stores only surface metadata (IDs/title/focus), never terminal transcripts;
credential-looking fields in supplied metadata are redacted. The provenance is
retained both on the queue item and in its snapshot manifest, and travels in an
exported review package.

Use `--agent-id`, `--agent-provider`, and `--feedback-target` to bind a
submission to its originating agent. For integrations, authenticated daemon
endpoints `POST /api/queue/hook` and `POST /api/queue/watch` provide
event-driven and polling-based automatic submission. Both compute a Git source
fingerprint themselves, making duplicate hook delivery idempotent. A changed
source produces a new immutable snapshot and requeues it; an unopened queued
revision is completed as superseded rather than silently overwritten.

## Existing Copilot CLI ACP feedback loop

Start the branch's Copilot CLI in ACP TCP mode and retain the `sessionId`
returned by its initial `connection.newSession({ cwd })`. The
`/localreview-submit` skill (or its equivalent wrapper) submits the exact
working directory/snapshot plus that live session binding:

```sh
queue-submit . --title "Review feature branch" \
  --agent-kind copilot-cli --agent-id "feature-123" \
  --acp-host 127.0.0.1 --acp-port 4123 --acp-session-id "$ACP_SESSION_ID"
```

For a remote machine, create an SSH *local* forward first and submit its local
port (for example `ssh -N -L 4123:127.0.0.1:4123 worker`). The daemon accepts
only loopback ACP hosts, so it cannot be used as an arbitrary TCP proxy.

Add comments with `POST /api/queue/:id/feedback`, then either set
`{ "deliver": true }` on that request or call
`POST /api/queue/:id/deliver-feedback`. Undelivered comments are batched into
one ACP `session/prompt` call for the same session. Use
`{ "policy": "interrupt" }` only when the current delivered feedback should
cancel an in-flight daemon-issued turn; the default `queue` policy waits for
it. No cmux keystrokes are used for this path.

## `/localreview-submit` workflow

`localreview-submit` and `queue-submit` are equivalent installed commands. A
`/localreview-submit` skill can invoke either from the branch being reviewed.
It takes the snapshot before it sends the queue request, so the
queue holds the submitted Git state rather than a pointer to a mutable working
tree. Queue Home materializes that snapshot into a daemon-owned review
directory and compares it with its captured parent/base before rendering the
diff. It captures `cwd`, branch/base/head through the snapshot, cmux
surface/workspace provenance when available, and redacted originating-agent
metadata.

For an existing Copilot CLI ACP session, pass all three ACP fields together.
The binding is then retained by the queue item, review package, auto-queue
watcher, and reproduction guidance:

```sh
queue-submit . --title "Parser review" --base origin/main \
  --agent-id parser-agent --agent-provider copilot --agent-kind copilot-cli \
  --copilot-session-id "$COPILOT_SESSION_ID" \
  --acp-host 127.0.0.1 --acp-port 4123 --acp-session-id "$ACP_SESSION_ID"
```

`--watch` preserves that origin binding when a later Git revision is queued.
Do not place bearer tokens or terminal transcripts in submit metadata; the
daemon redacts credential-looking values and never stores terminal contents.

## Operations and recovery

Queue Home is the cross-workspace landing page at `/`; `/queue` remains a
compatible alias. Opening a workspace reviewer at `/review` is deliberate;
its actual absolute path is retained on every queue item. If Queue Home was
opened directly rather than through `localreview-open`, it offers a local-only
token recovery field instead of silently failing with a bare 401.
Control APIs require the loopback daemon bearer token in the owner-only
discovery file. The CLI converts it into a one-time browser bootstrap code;
the browser never receives the discovery token.

| Need | Control/API | Recovery |
| --- | --- | --- |
| Inspect one review | `GET /api/queue/:id` | Includes comments, ACP delivery timestamps, and decision history. |
| Reproduce | `GET /api/queue/:id/reproduce` or `localreview-reproduce-copilot <id> <empty-dir>` | The endpoint prints safe command templates; the CLI materializes only into a new/empty directory. |
| Requeue | `POST /api/queue/:id/requeue` | Returns the same immutable snapshot to the end of the queue. |
| Remove without review | `DELETE /api/queue/:id` | Hides the item from the active queue and retains its audit record. Resubmit its path + topic or PR URL for a new round. |
| Remote cache state | `GET /api/queue/:id/remote-status` | Refresh for a new head; cleanup removes only cache-owned paths; submit the URL again to re-add. |
| Agent routing | `GET /api/agents`, `POST /api/agents/:id/heartbeat`, `POST /api/agents/:id/reconnect` | Register workspace, cmux `surfaceId`, and live ACP/session data; terminal `/btw` rejects missing, stale, or disconnected targets. |
| Federation | `GET /api/federation/nodes/:id/status`, `POST .../connect`, `POST .../disconnect` | Verify remote loopback daemon/token/port and SSH batch authentication, then retry. |

Copy/paste and ACP delivery are separate controls. Fetch
`GET /api/queue/:id/feedback/prompt?includeDelivered=true` for a portable
prompt, or POST to `/deliver-feedback` with `{ "policy": "queue" }` to send
the undelivered batch to the exact ACP session. ACP states are `idle`, `busy`,
`connecting`, `error`, and `unavailable`; errors leave feedback undelivered so
the user can safely retry or paste it without duplicate delivery.

Federated nodes are lazy SSH forwards: the local daemon starts
`ssh -N -L 127.0.0.1:<local>:127.0.0.1:<remote>` only when a node is opened or
aggregated. `GET /api/federation/queue` returns a node error alongside its
empty item list rather than hiding a disconnected remote. Saved remote tokens
are never returned by node or status APIs.

`/ask` is a separate, read-only Copilot SDK conversation channel. It can keep
question-set and inline-code context in its own persistent transcript, but it
is never mixed into queue feedback, exported prompts, ACP delivery, or GitHub
review payloads unless the reviewer explicitly turns an answer into a review
comment.

## Validation fixtures

Run `bun test` for the hermetic validation suite. It includes a scripted ACP
agent, a dirty two-repository snapshot/reproduction fixture, a local bare-Git
PR mirror fixture, and a fake `ssh -L` loopback tunnel that verifies
federation authentication, lazy connect, cache, disconnect, and reconnect.
The fixtures do not need Copilot, GitHub credentials, a running SSH daemon, or
cmux. Set `CMUX_LOCALREVIEW_DATA_DIR` to redirect daemon state and artifacts
when running an isolated smoke test.

Browser-driven UI smoke testing requires a browser-control runtime supplied by
the host environment. It is intentionally not faked by this repository; when
that runtime is unavailable, build/typecheck plus API and fixture tests still
run, while manual browser click-through must be performed in an environment
that provides the browser controller.

## Opt-in live GitHub and Copilot validation

The hermetic suite deliberately never spends Copilot requests or posts to
GitHub. Run this short, authenticated smoke flow against a disposable PR that
you own when validating the real integrations. It performs an actual GitHub
PR read and an actual Copilot response; the final review-publication step is
explicitly opt-in because it creates a real GitHub review.

```sh
# Set up dedicated Apps once; the guide specifies the least-privilege
# permissions. Configure/connect read and copilot for local PR review + /ask.
bun src/localreview-github-app.ts guide
bun src/localreview-github-app.ts configure --capability read --client-id Iv1.…
bun src/localreview-github-app.ts connect --capability read
bun src/localreview-github-app.ts configure --capability copilot --client-id Iv1.…
bun src/localreview-github-app.ts connect --capability copilot

# Resolve, mirror, snapshot, and queue a real PR through the read App. This
# does not publish a review yet.
export PR_URL='https://github.com/OWNER/REPOSITORY/pull/NUMBER'
bun src/queue-submit.ts "$PR_URL" --title 'Authenticated remote smoke' --json
bun src/localreview-open.ts --home
```

In Queue Home, open that remote item and verify its **Remote cache** state,
head/base SHAs, and source files. To exercise an actual GitHub review, connect
the separate **PR publish** App, add a non-destructive summary or inline
comment, and choose **Request changes** or **Approve** only on the disposable
PR. Confirm the result on that PR’s GitHub page.

For an existing Copilot CLI agent, run it as `copilot --acp --port 4123`, keep
the session ID returned by ACP, submit the three ACP values with
`queue-submit`, add one test comment, and choose **Send through ACP**. The
queue item must report `idle` with a delivery timestamp before this is counted
as a live ACP pass. Never put the daemon bearer token, `gh` token, or Copilot
credential in queue metadata, shell history shared with others, or a review
comment.
