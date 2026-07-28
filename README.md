# cmux-localreview

`cmux-localreview` is a loopback-only review daemon and browser UI for durable
local snapshots and GitHub pull requests. Queue Home is the starting point;
opening a reviewer is always an explicit action on a queue item.

The native runtime is Go: `localreviewd` owns state and the web UI, while
`localreview` is the operator CLI. The retired `bun src/*.ts` commands are not
an operational interface.

## Start here

Install a released build on macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/uhvesta/cmux-localreview/main/install.sh | sh
localreview daemon run --port 0
localreview open
```

For a source checkout, build/install native binaries and project Copilot
skills in one command:

```sh
sh scripts/localreview-install.sh /absolute/path/to/project
localreview daemon run --port 0
localreview submit --title 'Review parser change' --topic parser-boundaries \
  /absolute/path/to/project
localreview open
```

The installer supports macOS and Linux, writes only to a user-controlled
prefix (default `~/.local/bin`), and can be previewed with `--dry-run`. It
does not modify shell profiles or copy credentials. See
[Setup](docs/SETUP.md) for prerequisites and remote-machine instructions.

## What is safe by design

- The daemon binds only `127.0.0.1`. Its owner-only discovery file holds a
  bearer capability; `localreview open` passes it in a URL fragment, then the
  UI exchanges it for an HttpOnly same-origin cookie. Do not share the URL.
- Each local submission captures an immutable Git snapshot without modifying
  the source worktree, index, or `HEAD`. Re-submit the same path and topic to
  create a new linked review round.
- `/ask` is a distinct, persisted Copilot SDK conversation. Opening or
  refreshing a review reads history only; it never resends a question. Formal
  feedback is delivered only through the explicit **Send through Copilot**
  action.
- Authentication uses cmux-localreview’s dedicated OAuth credentials in the
  OS secret store—not `gh`, PATs, environment variables, or a Copilot CLI
  login. Read, write, and Copilot are separate capabilities.

## Guides

- [CLI workflows](docs/CLI-WORKFLOWS.md) — authoritative commands and a
  copy/paste local review loop.
- [Setup](docs/SETUP.md) — macOS/Linux installation, skills, OAuth, and
  remote-worker roles.
- [Agent guide](docs/AGENT-GUIDE.md) — concise review protocol and safe
  handoff template.
- [FAQ](docs/FAQ.md) — common operator questions and boundaries.
- [Troubleshooting](docs/TROUBLESHOOTING.md) — recoverable error states.
- [Copilot sessions](docs/COPILOT-ACP-SESSIONS.md) — what native SDK sessions
  support today, and the intentionally unsupported ACP transport.
- [Demo](docs/DEMO.md) — credential-free browser smoke test.
- [CLI workflows](docs/CLI-WORKFLOWS.md#deterministic-ask-and-btw-acceptance-fixture-source-checkouts-only)
  also documents the isolated, non-release Copilot streaming fixture used for
  repeatable browser and Electron acceptance testing.
- [Go migration](docs/GO-DAEMON-MIGRATION.md) and
  [cutover audit](docs/CUTOVER-AUDIT.md) — engineering migration evidence.

## Native command surface

```text
localreview daemon run|status|stop
localreview open [--no-open] [--queue-item ID | --pr PR-URL] [workspace]
localreview submit|queue-submit [--title TITLE] [--topic TOPIC] WORKSPACE
localreview reproduce [--copilot] [--json] MANIFEST-OR-QUEUE-ID DESTINATION
localreview setup [--personal] [--dry-run] [WORKSPACE]
localreview auth login|status|logout
localreview github-app guide|configure|connect|status|disconnect
localreview remote daemon|submit|status
localreview federation add|list|connect|disconnect|queue|workspaces
```

Run `localreview <command> --help` for argument validation. The Go CLI is the
source of truth when a guide and an older screenshot disagree.
