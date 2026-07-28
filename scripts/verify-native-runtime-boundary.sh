#!/usr/bin/env sh
# Verify the release runtime boundary independently of the frozen TS oracle.
# The React/Electron build layers may use JS, but no shipped Go daemon/CLI code
# may invoke Bun/Node, import the retired src tree, or depend on an ACP adapter.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"

fail() {
  printf '%s\n' "native runtime boundary violation: $1" >&2
  exit 1
}

# Core Go source and Bazel declarations are the authoritative shipped runtime
# surface. A match is always an error here; documentation and the frozen
# capture harness are deliberately outside this check until Phase 4 removes
# them.
if rg -n -i '(exec\.(Command|CommandContext)\([^)]*"(node|bun)"|exec\.LookPath\("(node|bun)"|"[^"[:space:]]*(src/server|global-daemon\.ts|claude-code-acp|gemini[^"[:space:]]*acp)[^"]*")' \
  cmd internal --glob '*.go' --glob 'BUILD.bazel' --glob '*.bzl'; then
  fail 'Go daemon/CLI references a retired JS or ACP runtime'
fi

# The UI build is intentionally a separate Bun/Vite *build-time* concern.
# The native executable process itself must not need Node/Bun after the
# embedded archive exists. Confirm that Bazel can resolve both release
# binaries, then invoke the host-native outputs with a deliberately tiny PATH
# that contains neither runtime. This catches a future exec.LookPath edge that
# source scanning alone would miss.
bazel query 'kind(go_binary, //cmd/localreview/... union //cmd/localreviewd/...)' >/dev/null || fail 'Bazel cannot resolve released native binaries'

native_output() {
  bazel cquery --output=files "$1" | tail -n 1
}

cli=$(native_output //cmd/localreview:localreview)
daemon=$(native_output //cmd/localreviewd:localreviewd)
test -x "$cli" || fail 'native localreview output is missing; build it before boundary verification'
test -x "$daemon" || fail 'native localreviewd output is missing; build it before boundary verification'
runtime_path=/usr/bin:/bin
env PATH="$runtime_path" "$cli" --help >/dev/null || fail 'localreview requires an external runtime to print help'
env PATH="$runtime_path" "$daemon" --help >/dev/null || fail 'localreviewd requires an external runtime to print help'

# The deterministic E2E fixture is intentionally a *different* binary. It
# must never enter release archive inputs or Electron's packaged sidecar.
if rg -n 'localreviewd-e2e|e2ecopilot' release desktop .github/workflows/release.yml --glob 'BUILD.bazel' --glob '*.yml' --glob '*.yaml'; then
  fail 'E2E Copilot fixture leaked into a release payload'
fi

printf '%s\n' 'native Go runtime boundary is clean'
