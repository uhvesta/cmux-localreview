# Agent loop prompt — implement NATIVE_MIGRATION_SPEC.md end-to-end

You are an autonomous engineering agent working in this repository (`cmux-localreview`). Your goal across iterations is to **fully implement `NATIVE_MIGRATION_SPEC.md` and prove it works**. `SPEC.md` is the product behavior contract; `NATIVE_MIGRATION_SPEC.md` (the "migration spec") is the stack/architecture contract and execution plan. Where they conflict, the migration spec wins on stack, `SPEC.md` wins on behavior.

Each iteration, do exactly one meaningful increment, leave the tree green, and record where you stopped. Do not ask the user questions; every decision you need is in the two specs. If you hit something genuinely undecidable, pick the option that best serves "pure Go core, minimal JS, parity with the frozen TS server", and log the decision in `MIGRATION_LOG.md`.

## Every iteration

1. **Re-derive state, don't trust memory.** Read `MIGRATION_LOG.md` (create it if missing — it is the running journal: one dated entry per iteration with what was done, decisions made, and what's next). Check `git log --oneline -15` and `git status`. Run `go build ./... && go test ./...` first; if the tree is broken, fixing it IS this iteration's task.
2. **Pick the next increment** — the first unfinished item in this strict order:
   - **Phase 0**: fixture corpus. Script capture of request/response fixtures from the frozen TS server (`bun` still runs it) for every route in the migration spec's inventory, against a generated fixture workspace (≥2 nested repos, staged/unstaged/untracked mix). Store under `testdata/parity/`. This gates everything — do not port past what has fixtures.
   - **Phase 1**: port to parity in the spec's order (blob/expansion → full-file projection → comments remainder → sessions/export/send-to-cmux → wsHub + watching → Copilot ask/btw + model selection → GitHub auth + secret store → remote PR/federation → CLI subcommands). Each port lands with Go unit tests AND its parity fixtures passing against the Go daemon. Validate the Copilot SDK token→session→stream path as early as it becomes reachable.
   - **Phase 2**: cutover — `localreview` CLI subcommands complete, UI embedded via `go:embed`, Bazel targets (`bazel build //...`, `bazel test //...` green), Electron shell in `desktop/` spawning the sidecar, release archive rules.
   - **Phase 3**: validation loop (below).
   - **Phase 4**: delete `src/`, prune package.json, update docs.
3. **Implement it.** Small, reviewable commits with the repo's existing commit style. Preserve env vars, data-dir layout, DB schemas, and WebSocket message shapes exactly — the React UI must keep working unmodified except where the migration spec says otherwise (API base, model picker).
4. **Validate the increment.** Minimum bar every iteration: `go test ./...` and the relevant parity fixtures green, plus `bun test` for any still-live TS (until Phase 4). For increments that change user-visible behavior, ALSO do a live check: start `localreviewd` against the fixture workspace and drive the real UI with the Claude-in-Chrome computer-use tools (navigate, click, read the page) to confirm the feature works in the browser — a screenshot-verified pass, not just HTTP assertions.
5. **Journal and commit.** Append the `MIGRATION_LOG.md` entry (done / decided / next), commit everything. Never end an iteration with uncommitted work or a red tree.

## Phase 3 — full validation loop (when Phases 0–2 are complete)

Execute the checklist in the migration spec's "Phase 3" section literally, using computer-use tools against both the local web UI and the packaged Electron app: multi-repo review, comment lifecycle + restart persistence, ask/btw streaming with mid-conversation model switch, queue submit/open/complete, loopback OAuth in-browser + device flow in-terminal, Electron cold start/quit/relaunch, and the memory check (daemon idle RSS < 50 MB, no growth across repeated runs — measure with `ps -o rss=`). File each failure as a minimal repro in `MIGRATION_LOG.md`, fix it, and re-run the failed item. Phase 3 is done only when the ENTIRE checklist passes twice consecutively from a clean data dir.

## Exit condition

Stop the loop (declare done) only when ALL are true: Phase 4 deletion is merged; `bazel test //...` and `go test ./...` are green; the Phase 3 checklist has its two consecutive clean passes recorded in `MIGRATION_LOG.md`; a tagged release build produces the platform archives + Electron artifacts; and `README.md` documents the new install/run story. Until then, always end your iteration by scheduling the next one.

## Guardrails

- Never copy code from `../cmux` (GPL). `vendor/difit` and `vendor/cmux-hub` are MIT and already vendored.
- No new Node runtime dependencies in the daemon or CLI. Node is build-time (Vite) and Electron-shell only.
- Tokens go to the secret store, never SQLite or logs. Keep the github.com token restriction and path-traversal guards.
- Don't refactor the React UI beyond what the spec requires.
- If an iteration's increment turns out too big, land a coherent subset green rather than a broken whole.
