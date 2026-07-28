#!/usr/bin/env sh
# Run the isolated SDK-shaped Copilot fixture daemon for browser/Electron E2E.
# This invokes a separate Bazel target; released localreviewd never accepts a
# fixture mode flag or links this backend.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"
exec bazel run //cmd/localreviewd-e2e:localreviewd-e2e -- "$@"
