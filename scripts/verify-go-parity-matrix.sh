#!/usr/bin/env bash
set -euo pipefail

# Consume both frozen TypeScript captures from the Go daemon tests. First
# validate the immutable captures without executing the frozen TypeScript
# runtime. verify-parity-fixtures.sh remains a pre-Phase-4 capture-
# determinism tool, not a release dependency.
bash scripts/verify-frozen-parity-corpus.sh

pattern='^(TestFrozenTypeScriptParityMatrix|TestFrozenRemotePullRequestLifecycleParity)$'
go test ./internal/daemon -run "$pattern" -count=1
bazel test //internal/daemon:daemon_test --test_filter="$pattern"

echo "Go parity matrix passed"
