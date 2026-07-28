#!/usr/bin/env sh
# Build and structurally verify every v1 native release archive.  This does
# not execute a foreign-architecture binary; the platform-specific installer
# smoke remains CI's responsibility.  It does prove that the tag-release
# payload contains both native binaries for all promised platforms and that
# checksums describe exactly those payloads.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"

bazel build //release:archives //release:checksums

output=${LOCALREVIEW_RELEASE_OUTPUT_DIR:-"$root/bazel-bin/release"}
checksums="$output/checksums.txt"
[ -f "$checksums" ] || { echo "missing release checksums: $checksums" >&2; exit 1; }

digest() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

for platform in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
  archive="$output/localreview_${platform}.tar.gz"
  root_name="localreview_${platform}"
  [ -f "$archive" ] || { echo "missing release archive: $archive" >&2; exit 1; }

  entries=$(tar -tzf "$archive")
  printf '%s\n' "$entries" | grep -Fx "$root_name/bin/localreview" >/dev/null || {
    echo "$archive is missing localreview" >&2; exit 1;
  }
  printf '%s\n' "$entries" | grep -Fx "$root_name/bin/localreviewd" >/dev/null || {
    echo "$archive is missing localreviewd" >&2; exit 1;
  }

  expected=$(awk -v name="$(basename "$archive")" '$2 == name { print $1 }' "$checksums")
  actual=$(digest "$archive")
  [ -n "$expected" ] && [ "$actual" = "$expected" ] || {
    echo "checksum mismatch for $(basename "$archive")" >&2; exit 1;
  }
done

# A checksum manifest with stale or unexpected native archives would make a
# release ambiguous. Its four lines are the complete v1 binary contract.
[ "$(wc -l < "$checksums" | tr -d ' ')" = 4 ] || {
  echo "checksums.txt must contain exactly four native archives" >&2; exit 1;
}

echo "all four native release archives are structurally valid"
