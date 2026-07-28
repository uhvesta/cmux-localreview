#!/usr/bin/env bash
set -euo pipefail

# Phase-4/release parity gate. This validates the immutable Phase-0 capture
# with Go and Bazel only. It deliberately never invokes Bun, Node, src/, or a
# capture script: the TypeScript oracle is archived in testdata/ and release
# verification must remain valid after that runtime is deleted.
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

corpus=(
  testdata/parity/ts-final/http.json
  testdata/parity/ts-final/remote-pr.json
)

for path in "${corpus[@]}"; do
  test -f "$path" || { echo "missing frozen parity corpus: $path" >&2; exit 1; }
done

# A dirty corpus is not a frozen oracle. This does not require Git for an
# extracted release archive, but catches accidental local recaptures when Git
# metadata is available.
if command -v git >/dev/null 2>&1 && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  if ! git diff --quiet -- "${corpus[@]}" || ! git diff --cached --quiet -- "${corpus[@]}"; then
    echo "frozen parity corpus is modified; review and commit an explicit oracle update before verifying a release" >&2
    exit 1
  fi
fi

go test ./internal/daemon -run '^TestFrozenParityCorpusIntegrity$' -count=1
bazel test //internal/daemon:daemon_test --test_filter='^TestFrozenParityCorpusIntegrity$'
echo "frozen parity corpus is immutable and release-verifiable without Bun"
