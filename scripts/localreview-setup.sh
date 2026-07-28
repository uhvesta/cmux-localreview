#!/usr/bin/env sh
# Cross-platform entry point. It makes no package-manager changes; Bun must
# already be installed and the TypeScript setup command performs only additive
# Copilot configuration writes.
set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
case "$(uname -s)" in
  Darwin) exec sh "$script_dir/localreview-setup-macos.sh" "$@" ;;
  Linux) exec sh "$script_dir/localreview-setup-linux.sh" "$@" ;;
  *) echo "cmux-localreview supports macOS and Linux; use localreview-setup directly on this platform." >&2; exit 2 ;;
esac
