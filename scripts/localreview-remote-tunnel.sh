#!/usr/bin/env sh
# Run this locally to print a safe copy/paste SSH local-forward command.
set -eu
command -v bun >/dev/null 2>&1 || { echo "Bun is required. Install Bun, then rerun this command." >&2; exit 127; }
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
exec bun "$script_dir/../src/localreview-remote.ts" tunnel "$@"
