#!/usr/bin/env sh
# Run this on the remote host. The daemon always binds only 127.0.0.1.
set -eu
command -v bun >/dev/null 2>&1 || { echo "Bun is required. Install Bun, then rerun this command." >&2; exit 127; }
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
exec bun "$script_dir/../src/localreview-remote.ts" daemon "$@"
