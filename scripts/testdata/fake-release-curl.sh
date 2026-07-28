#!/usr/bin/env sh
# Test-only curl replacement for verify-native-installer.sh. It accepts the
# subset of curl's download interface used by install.sh and copies a fixture
# release asset instead of reaching the network.
set -eu

output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      output=$2
      shift 2
      ;;
    -*)
      shift
      ;;
    *)
      url=$1
      shift
      ;;
  esac
done

[ -n "$output" ] || { echo "fake curl requires -o" >&2; exit 2; }
[ -n "$url" ] || { echo "fake curl requires a URL" >&2; exit 2; }
[ -n "${LOCALREVIEW_TEST_RELEASE_DIR:-}" ] || { echo "LOCALREVIEW_TEST_RELEASE_DIR is required" >&2; exit 2; }

asset=${url##*/}
case "$asset" in
  localreview_*.tar.gz|checksums.txt) ;;
  *) echo "unexpected release asset URL: $url" >&2; exit 2 ;;
esac
cp "$LOCALREVIEW_TEST_RELEASE_DIR/$asset" "$output"
