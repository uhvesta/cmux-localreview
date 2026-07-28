#!/usr/bin/env bash
# Verify that the reviewed Vite renderer can be installed and built in a clean
# directory. This deliberately does not start the frozen TypeScript server or
# import a Go daemon: it is the dependency gate that must pass before Phase 4
# prunes root server/CLI packages.
set -euo pipefail

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/cmux-localreview-ui-install.XXXXXX")
cleanup() { rm -rf "$scratch"; }
trap cleanup EXIT INT TERM

mkdir -p "$scratch/vendor"
cp "$root/package.json" "$root/bun.lock" "$scratch/"
cp -R "$root/vendor/difit" "$scratch/vendor/difit"

(
  cd "$scratch"
  # Do not run package lifecycle hooks in this audit. The renderer needs only
  # its declared dependency graph and Vite compilation.
  bun install --frozen-lockfile --ignore-scripts
  cd vendor/difit
  bun ../../node_modules/vite/bin/vite.js --config vite.config.ts build
)

test -f "$scratch/vendor/difit/dist/client/index.html"

# A browser bundle must not carry retired server, ACP, or JS Copilot SDK
# module names. This catches an accidental renderer import before a package
# removal appears to succeed only because a developer has stale node_modules.
if rg -n '@github/copilot-sdk|agent-client-protocol|claude-code-acp|src/server' "$scratch/vendor/difit/dist/client"; then
  printf '%s\n' 'UI clean-install verification: retired server dependency leaked into renderer output' >&2
  exit 1
fi

printf '%s\n' 'UI clean-install verification passed (fresh lockfile install + Vite renderer build).'
