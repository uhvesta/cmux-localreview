#!/usr/bin/env sh
set -eu
[ "$(uname -s)" = "Linux" ] || { echo "This wrapper is for Linux. Use scripts/localreview-setup-macos.sh on macOS." >&2; exit 2; }
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
if [ "${1:-}" = "--install" ]; then
  shift
  exec sh "$script_dir/localreview-install.sh" "$@"
fi
exec "${LOCALREVIEW_BIN:-localreview}" setup "$@"
