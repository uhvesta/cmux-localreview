#!/usr/bin/env sh
# Cross-platform entry point for the native dedicated GitHub App helper.
set -eu

case "$(uname -s)" in
  Darwin|Linux) ;;
  *) echo "GitHub App setup is supported on macOS and Linux." >&2; exit 2 ;;
esac
exec "${LOCALREVIEW_BIN:-localreview}" github-app "$@"
