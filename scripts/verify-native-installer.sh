#!/usr/bin/env sh
# Exercise the released-archive installer without network access. This checks
# archive layout, SHA-256 verification, both native binaries, and every
# compatibility launcher that keeps installed Copilot skills working.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "native installer smoke supports macOS and Linux only" >&2; exit 2 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64) arch=amd64 ;;
  *) echo "unsupported CPU architecture for native installer smoke: $(uname -m)" >&2; exit 2 ;;
esac
platform="${os}_${arch}"
archive=${1:-$root/bazel-bin/release/localreview_${platform}.tar.gz}
checksums=${2:-}
[ -f "$archive" ] || {
  echo "release archive not found: $archive" >&2
  echo "build it first with: bazel build //release:${platform}_archive" >&2
  exit 2
}
archive_name=$(basename "$archive")
expected_name="localreview_${platform}.tar.gz"
[ "$archive_name" = "$expected_name" ] || {
  echo "release archive $archive_name cannot run on this ${platform} host" >&2
  exit 2
}
[ -z "$checksums" ] || [ -f "$checksums" ] || {
  echo "release checksum manifest not found: $checksums" >&2
  exit 2
}

stage=$(mktemp -d "${TMPDIR:-/tmp}/localreview-release-fixture.XXXXXX")
prefix=$(mktemp -d "${TMPDIR:-/tmp}/localreview-release-prefix.XXXXXX")
data=$(mktemp -d "${TMPDIR:-/tmp}/localreview-release-data.XXXXXX")
project=$(mktemp -d "${TMPDIR:-/tmp}/localreview-release-project.XXXXXX")
cleanup() { rm -rf "$stage" "$prefix" "$data" "$project"; }
trap cleanup EXIT INT TERM

mkdir -p "$stage/download/vtest"
cp "$archive" "$stage/download/vtest/$archive_name"
if [ -n "$checksums" ]; then
  # A tag-release smoke must exercise the exact manifest uploaded with the
  # archive, not a test-only checksum regenerated from its local copy.
  cp "$checksums" "$stage/download/vtest/checksums.txt"
else
  (
    cd "$stage/download/vtest"
    if command -v shasum >/dev/null 2>&1; then
      shasum -a 256 "$archive_name" > checksums.txt
    else
      sha256sum "$archive_name" > checksums.txt
    fi
  )
fi

# install.sh intentionally uses curl by name. Put a static, auditable test
# replacement first on PATH so the production script follows its normal
# download + checksum branch without network access.
fake_bin="$stage/bin"
mkdir -p "$fake_bin"
ln -s "$root/scripts/testdata/fake-release-curl.sh" "$fake_bin/curl"
PATH="$fake_bin:$PATH" \
  LOCALREVIEW_TEST_RELEASE_DIR="$stage/download/vtest" \
  LOCALREVIEW_VERSION=vtest \
  LOCALREVIEW_RELEASE_BASE_URL=https://release-fixture.invalid \
  LOCALREVIEW_INSTALL_PREFIX="$prefix" \
  sh "$root/install.sh" --setup "$project"

for executable in \
  localreview localreviewd cmux-localreview global-daemon queue-submit \
  localreview-submit localreview-open localreview-demo localreview-setup \
  localreview-github-app localreview-reproduce localreview-reproduce-copilot \
  localreview-remote localreview-remote-daemon; do
  [ -x "$prefix/$executable" ] || { echo "missing installed launcher: $executable" >&2; exit 1; }
done

# An alias must resolve through the release-local binary, not PATH or a legacy
# runtime. --help exits nonzero under flag, so only assert its native usage.
CMUX_LOCALREVIEW_DATA_DIR="$data" "$prefix/queue-submit" --help 2>&1 | grep -q 'Usage of queue-submit:'
CMUX_LOCALREVIEW_DATA_DIR="$data" "$prefix/localreview-reproduce-copilot" --help 2>&1 | grep -q 'Usage of reproduce:'

# `--setup` must run the installed release-local CLI, not merely copy helper
# names. It creates only its managed Copilot skills/instructions in the chosen
# workspace; OAuth setup remains an explicit, separate operator action.
grep -q 'cmux-localreview-managed' "$project/.github/skills/localreview-submit/SKILL.md"
grep -q 'cmux-localreview-managed' "$project/.github/copilot-instructions.md"
CMUX_LOCALREVIEW_DATA_DIR="$data" "$prefix/localreview-github-app" guide | grep -q 'OAuth Apps'

CMUX_LOCALREVIEW_DATA_DIR="$data" "$prefix/localreview" daemon run --port 0 >"$data/daemon.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 100); do
  [ -f "$data/daemon.json" ] && break
  sleep 0.05
done
[ -f "$data/daemon.json" ] || { cat "$data/daemon.log" >&2; exit 1; }
CMUX_LOCALREVIEW_DATA_DIR="$data" "$prefix/localreview" daemon status | grep -q '"running":true'
CMUX_LOCALREVIEW_DATA_DIR="$data" "$prefix/localreview" daemon stop | grep -q 'Stopped localreviewd PID'
wait "$daemon_pid" || true

echo "native release installer verification passed"
