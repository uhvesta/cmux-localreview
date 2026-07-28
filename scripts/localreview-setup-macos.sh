#!/usr/bin/env sh
set -eu
[ "$(uname -s)" = "Darwin" ] || { echo "This wrapper is for macOS. Use scripts/localreview-setup-linux.sh on Linux." >&2; exit 2; }
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
if [ "${1:-}" = "--install" ]; then
  shift
  exec sh "$script_dir/localreview-install.sh" "$@"
fi
exec "${LOCALREVIEW_BIN:-localreview}" setup "$@"
