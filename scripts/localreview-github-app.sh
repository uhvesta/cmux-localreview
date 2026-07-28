#!/usr/bin/env sh
# Cross-platform entry point for the dedicated GitHub App setup helper.
set -eu

case "$(uname -s)" in
  Darwin|Linux) ;;
  *) echo "GitHub App setup is supported on macOS and Linux." >&2; exit 2 ;;
esac
command -v bun >/dev/null 2>&1 || { echo "Bun is required. Install Bun, then rerun this command." >&2; exit 127; }
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
exec bun "$script_dir/../src/localreview-github-app.ts" "$@"
