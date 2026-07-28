#!/usr/bin/env sh
set -eu
[ "$(uname -s)" = "Darwin" ] || { echo "This wrapper is for macOS. Use scripts/localreview-setup-linux.sh on Linux." >&2; exit 2; }
command -v bun >/dev/null 2>&1 || { echo "Bun is required. Install Bun, then rerun this command." >&2; exit 127; }
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
if [ "${1:-}" = "--install" ]; then
  shift
  exec sh "$script_dir/localreview-install.sh" "$@"
fi
exec bun "$script_dir/../src/localreview-setup.ts" "$@"
