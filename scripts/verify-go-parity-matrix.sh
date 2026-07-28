#!/usr/bin/env bash
set -euo pipefail

# Consume the frozen TypeScript HTTP corpus from the Go daemon test.  This is
# intentionally separate from verify-parity-fixtures.sh: that script proves
# the TS oracle is repeatable; this one proves selected native routes still
# satisfy that oracle and every remaining row has an explicit exception.
go test ./internal/daemon -run '^TestFrozenTypeScriptParityMatrix$' -count=1
bazel test //internal/daemon:daemon_test --test_filter='^TestFrozenTypeScriptParityMatrix$'

echo "Go parity matrix passed"
