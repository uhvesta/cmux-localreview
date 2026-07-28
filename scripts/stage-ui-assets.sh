#!/usr/bin/env bash
# Build the React app once and stage the immutable output next to the Go
# package that embeds it. This is a build/release action, never a daemon
# runtime dependency.
set -euo pipefail

repo_root="${BUILD_WORKSPACE_DIRECTORY:-$(cd "$(dirname "$0")/.." && pwd)}"
source_dir="$repo_root/vendor/difit/dist/client"
target_dir="$repo_root/internal/webassets/dist"

cd "$repo_root"
bun run vendor-difit-build

if [[ ! -f "$source_dir/index.html" ]]; then
  echo "Vite did not produce $source_dir/index.html" >&2
  exit 1
fi
mkdir -p "$target_dir"
# Both macOS and Linux ship rsync. --delete is restricted to the validated
# repository-owned staging directory above.
rsync -a --delete "$source_dir/" "$target_dir/"
echo "Staged UI assets in $target_dir; now run: bazel build //cmd/localreviewd"
