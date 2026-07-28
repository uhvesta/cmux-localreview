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

# The two release binaries must be buildable without Node/Bun on PATH. This
# check is intentionally structural; the UI build remains a separate
# build-time concern and release archives embed its already-built assets.
bazel query 'kind(go_binary, //cmd/...)' >/dev/null || fail 'Bazel cannot resolve native binaries'

printf '%s\n' 'native Go runtime boundary is clean'
