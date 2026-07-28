#!/usr/bin/env bash
set -euo pipefail

# Phase 0 determinism gate. Both frozen-server capture programs must produce
# byte-identical normalized contracts on consecutive disposable runs.
work_dir=$(mktemp -d)
cleanup() { rm -rf "$work_dir"; }
trap cleanup EXIT

bun scripts/capture-parity-fixtures.ts >/dev/null
cp testdata/parity/ts-final/http.json "$work_dir/http.json"
bun scripts/capture-parity-fixtures.ts >/dev/null
cmp -s testdata/parity/ts-final/http.json "$work_dir/http.json"

bun scripts/capture-remote-parity-fixtures.ts >/dev/null
cp testdata/parity/ts-final/remote-pr.json "$work_dir/remote-pr.json"
bun scripts/capture-remote-parity-fixtures.ts >/dev/null
cmp -s testdata/parity/ts-final/remote-pr.json "$work_dir/remote-pr.json"

echo "parity fixtures are repeatable"
