#!/usr/bin/env sh
# Verify an unpacked Electron artifact without pretending that a display-free
# host exercised renderer interactions. It proves the packaged main process,
# bundled native sidecar, and sidecar lifecycle boundary are intact.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
artifact=${1:-}

if [ -z "$artifact" ]; then
  artifact=$(find "$root/desktop/dist" -type d \( -name '*.app' -o -name '*unpacked' \) -print -quit 2>/dev/null || true)
fi
if [ -n "$artifact" ] && [ ! -d "$artifact" ] && [ -d "$root/desktop/$artifact" ]; then
  # `npm --prefix desktop run verify:package -- dist/...` is a natural
  # invocation, while this script itself runs from the repository root.
  artifact="$root/desktop/$artifact"
fi
[ -n "$artifact" ] && [ -d "$artifact" ] || {
  printf '%s\n' 'supply an unpacked .app/*-unpacked directory, or package Electron first' >&2
  exit 2
}
artifact=$(CDPATH= cd "$artifact" && pwd)

if [ -d "$artifact/Contents/Resources" ]; then
  resources="$artifact/Contents/Resources"
elif [ -d "$artifact/resources" ]; then
  resources="$artifact/resources"
else
  printf '%s\n' "could not find Electron resources beneath $artifact" >&2
  exit 1
fi

sidecar="$resources/localreviewd"
asar_file="$resources/app.asar"
[ -x "$sidecar" ] || { printf '%s\n' "packaged sidecar is missing or not executable: $sidecar" >&2; exit 1; }
[ -f "$asar_file" ] || { printf '%s\n' "packaged renderer archive is missing: $asar_file" >&2; exit 1; }

asar_bin="$root/desktop/node_modules/.bin/asar"
[ -x "$asar_bin" ] || { printf '%s\n' 'npm --prefix desktop install is required to inspect app.asar' >&2; exit 2; }
command -v node >/dev/null || { printf '%s\n' 'node is required to syntax-check packaged main.mjs' >&2; exit 2; }

entries=$("$asar_bin" list "$asar_file")
printf '%s\n' "$entries" | grep -Fx '/main.mjs' >/dev/null || { printf '%s\n' 'packaged app.asar lacks main.mjs' >&2; exit 1; }
printf '%s\n' "$entries" | grep -Fx '/package.json' >/dev/null || { printf '%s\n' 'packaged app.asar lacks package.json' >&2; exit 1; }
if printf '%s\n' "$entries" | grep -E '/(node_modules|src|\.env)(/|$)' >/dev/null; then
  printf '%s\n' 'packaged Electron renderer contains an unexpected development/runtime tree' >&2
  exit 1
fi

scratch=$(mktemp -d "${TMPDIR:-/tmp}/localreview-electron-package.XXXXXX")
cleanup() { rm -rf "$scratch"; }
trap cleanup EXIT HUP INT TERM
(
  cd "$scratch"
  "$asar_bin" extract-file "$asar_file" main.mjs
  node --check main.mjs
  "$asar_bin" extract-file "$asar_file" package.json
  grep -Eq '"main"[[:space:]]*:[[:space:]]*"main\.mjs"' package.json
)

# Exercise the *packaged* Go binary rather than the checkout build: health,
# restart/persistence storage, parent-watchdog shutdown, and idle RSS.
LOCALREVIEWD_PATH="$sidecar" sh "$root/scripts/verify-electron-sidecar-lifecycle.sh"
printf '%s\n' "packaged Electron boundary passed: $artifact"
