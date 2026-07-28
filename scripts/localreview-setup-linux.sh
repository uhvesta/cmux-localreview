#!/usr/bin/env sh
set -eu
[ "$(uname -s)" = "Linux" ] || { echo "This wrapper is for Linux. Use scripts/localreview-setup-macos.sh on macOS." >&2; exit 2; }
command -v bun >/dev/null 2>&1 || { echo "Bun is required. Install Bun, then rerun this command." >&2; exit 127; }
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
exec bun "$script_dir/../src/localreview-setup.ts" "$@"
