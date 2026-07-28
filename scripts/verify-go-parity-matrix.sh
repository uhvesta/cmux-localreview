#!/usr/bin/env bash
set -euo pipefail

# Consume the frozen TypeScript HTTP corpus from the Go daemon test. First
# validate the immutable capture without executing the frozen TypeScript
# runtime. verify-parity-fixtures.sh remains a pre-Phase-4 capture-
# determinism tool, not a release dependency.
bash scripts/verify-frozen-parity-corpus.sh

go test ./internal/daemon -run '^TestFrozenTypeScriptParityMatrix$' -count=1
bazel test //internal/daemon:daemon_test --test_filter='^TestFrozenTypeScriptParityMatrix$'

echo "Go parity matrix passed"
