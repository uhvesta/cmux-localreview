#!/usr/bin/env bash
set -euo pipefail

# Phase 0 determinism gate. Capture runs must be byte-identical *and* match
# the checked-in frozen corpus. Never write into testdata from this verifier:
# changing the oracle requires an intentional fixture update and review.
work_dir=$(mktemp -d)
cleanup() { rm -rf "$work_dir"; }
trap cleanup EXIT

bun scripts/capture-parity-fixtures.ts --output "$work_dir/http-one.json" >/dev/null
bun scripts/capture-parity-fixtures.ts --output "$work_dir/http-two.json" >/dev/null
cmp -s "$work_dir/http-one.json" "$work_dir/http-two.json"
cmp -s testdata/parity/ts-final/http.json "$work_dir/http-one.json"

bun scripts/capture-remote-parity-fixtures.ts --output "$work_dir/remote-one.json" >/dev/null
bun scripts/capture-remote-parity-fixtures.ts --output "$work_dir/remote-two.json" >/dev/null
cmp -s "$work_dir/remote-one.json" "$work_dir/remote-two.json"
cmp -s testdata/parity/ts-final/remote-pr.json "$work_dir/remote-one.json"

echo "parity fixtures are repeatable"
