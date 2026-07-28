#!/usr/bin/env bash
# Build the React app once and stage the immutable output next to the Go
# package that embeds it. This is a build/release action, never a daemon
# runtime dependency.
set -euo pipefail

repo_root="${BUILD_WORKSPACE_DIRECTORY:-$(cd "$(dirname "$0")/.." && pwd)}"
source_dir="$repo_root/vendor/difit/dist/client"
target_dir="$repo_root/internal/webassets/dist"
archive_file=""

usage() {
  cat <<'EOF'
usage: stage-ui-assets.sh [--output <directory>] [--archive <tar.gz>]

Build the Vite UI once. By default stage it into internal/webassets/dist for
go:embed. --output keeps Bazel/release artifacts outside the source tree;
--archive writes a portable tar.gz of that staged output.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output)
      [[ $# -ge 2 ]] || { echo "--output needs a directory" >&2; exit 2; }
      target_dir="$2"
      shift 2
      ;;
    --archive)
      [[ $# -ge 2 ]] || { echo "--archive needs a path" >&2; exit 2; }
      archive_file="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

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
if [[ -n "$archive_file" ]]; then
  mkdir -p "$(dirname "$archive_file")"
  tar -C "$target_dir" -czf "$archive_file" .
  echo "Wrote Vite UI archive to $archive_file"
fi
echo "Staged UI assets in $target_dir; now run: bazel build //cmd/localreviewd"
