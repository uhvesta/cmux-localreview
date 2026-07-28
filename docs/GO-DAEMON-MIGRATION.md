# Go daemon migration

`localreviewd` is the replacement control plane for cmux-localreview. The
target runtime architecture is deliberately split:

- **Go** owns the loopback daemon, CLI, SQLite state, Git snapshots and
  worktrees, GitHub App device flow, system secrets, Copilot-SDK-backed
  `/ask` and `/btw`, SSH federation, and static asset serving.
- **The web app remains TypeScript/React/Vite.** It is a normal independently
  buildable frontend; production does not need Node or Bun to serve it.

The migration must preserve existing on-disk state and browser/API behavior.
It is not a schema reset and it must not run the legacy and Go daemons against
the same SQLite database at the same time.

## Current native cutover state

The repository now contains a Bazel-built `//cmd/localreviewd` Go binary. It
already proves the non-negotiable runtime boundary:

1. bind to `127.0.0.1` only;
2. write the same owner-only `daemon.json` discovery shape;
3. exchange a daemon bearer capability for an `HttpOnly; SameSite=Strict`
   browser cookie; and
4. serve the built React app as static files without a Node/Bun web server;
   and
5. own the SQLite v19 database plus the authenticated Queue Home lifecycle
   routes (`GET`/`POST /api/queue`, open, requeue, complete, and remove).

Build it with:

```sh
bazel build //cmd/localreviewd:localreviewd
bazel test //...
```

The companion CLI is `//cmd/localreview:localreview`. Its migrated commands
are deliberately small but real:

```sh
bazel run //cmd/localreview -- daemon --port 57992
bazel run //cmd/localreview -- open
bazel run //cmd/localreview -- open /path/to/workspace
bazel run //cmd/localreview -- queue-submit --title "Parser" --topic parser /path/to/workspace
```

`queue-submit` uses the owner-only discovery capability and the daemon HTTP
API; it does not open SQLite directly. It captures a multi-repository,
immutable Git snapshot before enqueueing. `open` puts the daemon capability
only in a URL fragment, which the browser immediately exchanges for an
HttpOnly cookie. Use `--no-open` to print that URL for Firefox, Chrome, or a
remote desktop session without launching a browser.

For source-only development, `go run ./cmd/localreview ...` and `go test ./...`
work with the checked-in Go vendor tree. Release and user installation should
use Bazel instead, so the generated UI and the paired native binaries are built
together.

The Go daemon is the active local control plane. The remaining TypeScript
server is frozen exclusively as a Phase-0 parity oracle and fixture harness;
it is not a supported runtime. `package.json` now routes every historical npm
bin to a small migration error before any `src/` module can load. The error
names the native `localreview` replacement. Phase 4 deletes both that temporary
guard and `src/` after the full cutover checklist is proven.

The frontend has an explicit Bazel build artifact:

```sh
bazel build //ui:dist
# Inspect the portable Vite bundle archive:
tar -tzf bazel-bin/ui/cmux-localreview-ui.tar.gz | head
```

`//ui:dist` is intentionally host-local until a pinned `rules_js` toolchain is
introduced. It uses `bun.lock`, produces an archive only, and never becomes a
daemon runtime requirement. `scripts/stage-ui-assets.sh` remains the explicit
step that stages the same bundle into `internal/webassets/dist` for `go:embed`.

## Copilot SDK parity

Fresh `/ask` is a hard requirement, not a reason to keep a Node daemon. The
official [GitHub Copilot Go SDK](https://github.com/github/copilot-sdk/tree/main/go)
supports explicit `GitHubToken`, `UseLoggedInUser=false`, model discovery,
reasoning effort, context tier, streaming, session resume, and permission
handlers. The Go implementation must use those controls with the same
dedicated Copilot GitHub App token boundary as the current implementation.

It must not silently fall back to `gh`, an existing Copilot CLI login, or
environment tokens. It must reject write/shell/network permission requests for
fresh `/ask` sessions.

## Required cutover gates

Before a route is switched, test the legacy and Go daemons independently using
copied fixture databases and repositories. Normalize generated IDs, paths, and
timestamps, then compare:

- HTTP status, headers, JSON bodies, and error bodies for every `/api` route;
- SSE event order for `/ask` and `/btw` (`started`, `delta`, `done`, `error`);
- websocket invalidation events;
- SQLite v18 schema/data, WAL behavior, upgrade backup, and rollback
  readability;
- browser capability/CSRF behavior;
- immutable multi-repository snapshot materialization and diffs;
- SDK feedback queue/interrupt/no-duplicate delivery and permission rejection;
- stale GitHub PR protection, explicit publication, and remote mirrors;
- lazy SSH federation connection/cache/disconnect;
- Queue Home → exact queue item → browser diff, including reopen with zero
  Copilot prompt replay.

The release matrix also includes macOS arm64/amd64 and Linux amd64/arm64
Bazel builds, install smoke tests, and a browser test against the production
static artifact.

## Data and rollback

The Go daemon uses the same data directory:

```text
${CMUX_LOCALREVIEW_DATA_DIR:-~/.local/share/cmux-localreview}
```

It must retain the existing `daemon.db`, artifacts, snapshots, and discovery
file. The Go daemon now runs the complete append-only v1→v19 schema history
transactionally, verified against a v1 fixture. Before Go becomes the default,
cutover still needs an owner-only backup and a rollback test proving that the
prior daemon can reopen that backup.

Federation node credentials are included in this security migration: move them
from SQLite into the OS secret provider, keyed by node ID, before the Go
daemon becomes the default.

## Web development and release packaging

Frontend development remains normal:

```sh
bun run build
```

Release/CI builds use Bazel to build the Go binary and a pinned frontend bundle
as an input asset. Generated `dist` files are not committed. The release
binary either embeds the bundle or receives it as a verified runfile; it never
starts Node/Bun to serve the UI.

## Open product requirements carried into Go

The migration includes—not merely documents—the following user-facing work:

- create and resume multiple local Copilot SDK conversations without manually
  copying connection or session IDs;
- explicit feedback target selection or confirmed multi-target broadcast, with
  per-target delivery/deduplication history;
- remote node actions (open, feedback, delivery, requeue, decision) rather
  than read-only queue aggregation;
- structural `/ask` versus formal-review channel separation, enforced in
  storage/export/delivery/GitHub paths rather than a `/ask` text-prefix
  heuristic; and
- clear active/history filters so completed items cannot look actionable.
