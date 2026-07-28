#!/usr/bin/env sh
# Exercise the released-archive installer without network access. This checks
# archive layout, SHA-256 verification, both native binaries, and every
# compatibility launcher that keeps installed Copilot skills working.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
archive=${1:-$root/bazel-bin/release/localreview_darwin_arm64.tar.gz}
[ -f "$archive" ] || {
  echo "release archive not found: $archive" >&2
  echo "build it first with: bazel build //release:darwin_arm64_archive" >&2
  exit 2
}

stage=$(mktemp -d "${TMPDIR:-/tmp}/localreview-release-fixture.XXXXXX")
prefix=$(mktemp -d "${TMPDIR:-/tmp}/localreview-release-prefix.XXXXXX")
data=$(mktemp -d "${TMPDIR:-/tmp}/localreview-release-data.XXXXXX")
cleanup() { rm -rf "$stage" "$prefix" "$data"; }
trap cleanup EXIT INT TERM

mkdir -p "$stage/download/vtest"
cp "$archive" "$stage/download/vtest/localreview_darwin_arm64.tar.gz"
(
  cd "$stage/download/vtest"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 localreview_darwin_arm64.tar.gz > checksums.txt
  else
    sha256sum localreview_darwin_arm64.tar.gz > checksums.txt
  fi
)

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
  sh "$root/install.sh"

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
