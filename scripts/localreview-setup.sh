#!/usr/bin/env sh
# Cross-platform native setup entry point. It makes no package-manager or
# credential changes; the installed Go CLI performs only additive,
# cmux-localreview-managed Copilot configuration writes.
set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
case "$(uname -s)" in
  Darwin) exec sh "$script_dir/localreview-setup-macos.sh" "$@" ;;
  Linux) exec sh "$script_dir/localreview-setup-linux.sh" "$@" ;;
  *) echo "cmux-localreview supports macOS and Linux; use localreview-setup directly on this platform." >&2; exit 2 ;;
esac
