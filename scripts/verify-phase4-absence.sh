#!/usr/bin/env bash
# Phase-4 deletion gate for the retired TypeScript control plane.
#
# Default mode is intentionally strict and is expected to FAIL before Phase 4:
# it proves the post-deletion source/runtime boundary. `--predelete` is the
# honest transition check for the current tree: it verifies the frozen runtime
# inventory remains explicit and that the archived corpus has native-only
# consumers, without pretending `src/` has already been removed.
set -euo pipefail

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"

usage() {
  printf '%s\n' 'usage: scripts/verify-phase4-absence.sh [--predelete]'
}

fail() {
  printf '%s\n' "phase-4 absence gate: $1" >&2
  exit 1
}

mode=strict
case "${1:-}" in
  '') ;;
  --predelete) mode=predelete ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 64 ;;
esac

legacy_paths=(
  src
  scripts/capture-parity-fixtures.ts
  scripts/capture-remote-parity-fixtures.ts
  scripts/verify-parity-fixtures.sh
  scripts/legacy-bin.mjs
  scripts/legacy-bin.test.mjs
  vendor/difit/src/cli
  vendor/difit/src/server
  vendor/difit/tsconfig.cli.json
  tsconfig.json
)

require_archived_corpus() {
  for path in \
    testdata/parity/ts-final/http.json \
    testdata/parity/ts-final/remote-pr.json \
    internal/daemon/parity_corpus_test.go \
    internal/daemon/parity_matrix_test.go \
    internal/daemon/remote_parity_test.go \
    scripts/verify-frozen-parity-corpus.sh \
    scripts/verify-go-parity-matrix.sh; do
    test -e "$path" || fail "archived native parity artifact is missing: $path"
  done
}

require_renderer_isolation() {
  # The renderer is intentionally retained after Phase 4.  These four files
  # are server-side cmux-hub modules, so a renderer import would turn the
  # planned delete into a late, user-visible regression.  Check it before the
  # deletion rather than discovering it while pruning dependencies.
  if rg -n 'vendor/cmux-hub/(cmux|logger|review-watcher|review)|cmux-hub/(cmux|logger|review-watcher|review)' \
    vendor/difit/src/client --glob '*.{ts,tsx,js,jsx}'; then
    fail 'retained renderer imports a server-only cmux-hub TypeScript module'
  fi

  # Vite's root is vendor/difit/src/client. The adjacent upstream CLI and
  # Express server are frozen runtime baggage, not a renderer dependency.
  # This import check makes their Phase-4 deletion a verified operation rather
  # than an assumption based on directory names.
  if rg -n '(from[[:space:]]+|import[[:space:]]*\()[[:punct:]]*(@/|\.\.?/)(cli|server)/' \
    vendor/difit/src/client --glob '*.{ts,tsx,js,jsx}'; then
    fail 'retained renderer imports a vendored CLI or server module'
  fi
}

if [[ "$mode" == predelete ]]; then
  for path in "${legacy_paths[@]}"; do
    test -e "$path" || fail "pre-deletion inventory changed unexpectedly: missing $path"
  done
  for path in vendor/cmux-hub/cmux.ts vendor/cmux-hub/logger.ts vendor/cmux-hub/review-watcher.ts vendor/cmux-hub/review.ts; do
    test -f "$path" || fail "pre-deletion server-only vendor inventory changed unexpectedly: missing $path"
  done
  require_archived_corpus
  require_renderer_isolation

  # The Go delivery boundary must already be free of the old runtime. This is
  # deliberately separate from the strict tree-absence assertion below.
  bash scripts/verify-native-runtime-boundary.sh

  # These consume the archived oracle with Go/Bazel only.  Calling them here
  # makes --predelete a real deletion-readiness check, not merely a directory
  # existence check.  Neither command starts Bun/Node or imports src/.
  bash scripts/verify-frozen-parity-corpus.sh
  bash scripts/verify-go-parity-matrix.sh

  printf '%s\n' 'Phase-4 pre-deletion inventory is explicit (legacy runtime is still present):'
  printf '  src files: %s\n' "$(find src -type f | wc -l | tr -d ' ')"
  printf '%s\n' '  retired capture/runtime paths:'
  printf '    %s\n' "${legacy_paths[@]}"
  printf '%s\n' '  server-only vendored TypeScript modules:'
  find vendor/cmux-hub -maxdepth 1 -type f -name '*.ts' -print | sort | sed 's#^#    #'
  printf '%s\n' 'Pre-deletion check passed; this is not a Phase-4 completion claim.'
  exit 0
fi

for path in "${legacy_paths[@]}"; do
  test ! -e "$path" || fail "retired runtime path remains: $path"
done

if compgen -G 'vendor/cmux-hub/*.ts' >/dev/null; then
  printf '%s\n' 'phase-4 absence gate: server-only vendor TypeScript remains:' >&2
  find vendor/cmux-hub -maxdepth 1 -type f -name '*.ts' -print >&2
  exit 1
fi

# The only surviving application TypeScript after Phase 4 is the Vite
# renderer. These source groups implement a second, vendored Node CLI/server
# and must not remain merely because they sit inside the UI checkout.
for path in vendor/difit/src/cli vendor/difit/src/server vendor/difit/tsconfig.cli.json tsconfig.json; do
  test ! -e "$path" || fail "retired vendored/root TypeScript runtime remains: $path"
done

if rg -n 'tsconfig\.cli|src/(cli|server)' vendor/difit/tsconfig.json package.json; then
  fail 'retained UI build config still references a retired CLI/server tree'
fi

# Preserve the archived JSON provenance and its Go integrity test. Everything
# else in the shipped source/runtime surface must be clear of frozen-server,
# ACP, and JS Copilot SDK references. Historical prose is deliberately outside
# the check, as are the immutable JSON captures themselves.
scan_roots=(cmd internal scripts desktop release .github package.json bun.lock)
scan_excludes=(
  # These gates intentionally contain the legacy names they diagnose. They
  # are verifier source, not a shipped dependency edge.
  --glob '!scripts/verify-phase4-absence.sh'
  --glob '!scripts/verify-native-runtime-boundary.sh'
  --glob '!scripts/verify-ui-clean-install.sh'
  --glob '!internal/daemon/parity_corpus_test.go'
  --glob '!testdata/**'
  --glob '!docs/**'
  --glob '!MIGRATION_LOG.md'
  --glob '!NATIVE_MIGRATION_SPEC.md'
  --glob '!AGENT_LOOP.md'
  --glob '!SPEC.md'
  --glob '!HANDOFF.md'
  --glob '!vendor/**'
  --glob '!node_modules/**'
)
pattern='src/server|bun[[:space:]]+src/|capture-parity-fixtures|capture-remote-parity-fixtures|legacy-bin\.mjs|@zed-industries/(agent-client-protocol|claude-code-acp)|@github/copilot-sdk|claude-code-acp'
if rg -n -i "$pattern" "${scan_roots[@]}" "${scan_excludes[@]}"; then
  fail 'frozen TypeScript, ACP, or JS Copilot SDK reference remains in a runtime/build surface'
fi

# These packages are server/CLI/ACP dependencies of the retired root runtime.
# React/Vite build dependencies are intentionally not named here.
# `prism-svelte` deliberately survives: the retained React renderer lazy-loads
# it for .svelte files (see vendor/difit/src/client/utils/languageLoader.ts).
# Treating it as a server dependency made the strict gate reject a valid UI.
if rg -n '"(@github/copilot-sdk|@zed-industries/agent-client-protocol|@zed-industries/claude-code-acp|@parcel/watcher|commander|express|open|simple-git|ws|@types/bun|@types/express|@types/ws)"[[:space:]]*:' package.json; then
  fail 'retired root package dependency remains'
fi

if rg -n '"(cmux-localreview|global-daemon|queue-submit|localreview-submit|localreview-reproduce|localreview-open|localreview-demo|localreview-setup|localreview-github-app|localreview-remote|localreview-remote-daemon)"[[:space:]]*:[[:space:]]*"\./scripts/' package.json; then
  fail 'obsolete npm/bin compatibility entry remains'
fi

# The root manifest remains the renderer build/test entry point.  Assert the
# replacement shape explicitly so a Phase-4 patch cannot delete the old CLI
# and silently leave a server command (or no UI typecheck) behind.  jq is a
# required host tool for the release installer checks and keeps this resilient
# to package.json formatting changes.
command -v jq >/dev/null || fail 'jq is required to inspect the retained UI package scripts'
if ! jq -e '
  (.scripts["vendor-difit-build"] // "") | test("(^|[[:space:]])vite[[:space:]]+build([[:space:]]|$)")
' package.json >/dev/null; then
  fail 'retained UI build script must invoke Vite directly'
fi
if ! jq -e '
  (.scripts.typecheck // "") | test("vendor/difit/tsconfig\\.json")
' package.json >/dev/null; then
  fail 'retained UI typecheck script must check vendor/difit/tsconfig.json'
fi
if jq -e '
  (.scripts // {}) | has("vendor-difit-server") or
  ((.test // "") | test("legacy-bin|bun[[:space:]]+test"))
' package.json >/dev/null; then
  fail 'retired server or legacy test script remains'
fi

require_archived_corpus
require_renderer_isolation
bash scripts/verify-native-runtime-boundary.sh
printf '%s\n' 'Phase-4 strict absence gate passed.'
