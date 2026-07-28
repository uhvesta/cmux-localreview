#!/usr/bin/env sh
# Install a checksummed GitHub Release archive. No package manager, Node, Bun,
# or source checkout is required at runtime.
set -eu

repo="${LOCALREVIEW_REPOSITORY:-uhvesta/cmux-localreview}"
version="${LOCALREVIEW_VERSION:-latest}"
prefix="${LOCALREVIEW_INSTALL_PREFIX:-$HOME/.local/bin}"

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) echo "cmux-localreview supports macOS and Linux." >&2; exit 2 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64) arch="amd64" ;;
  *) echo "Unsupported CPU architecture: $(uname -m)" >&2; exit 2 ;;
esac

if [ "$version" = "latest" ]; then
  version=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest" | sed 's#.*/tag/##')
fi
[ -n "$version" ] || { echo "Could not resolve a GitHub Release version." >&2; exit 1; }

archive="localreview_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/localreview-install.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

curl -fL --retry 3 -o "$tmp/$archive" "$base/$archive"
curl -fL --retry 3 -o "$tmp/checksums.txt" "$base/checksums.txt"
expected=$(awk -v target="$archive" '$2 == target || $2 == "*" target { print $1; exit }' "$tmp/checksums.txt")
[ -n "$expected" ] || { echo "Release checksums do not contain $archive." >&2; exit 1; }
if command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
else
  actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || { echo "Checksum verification failed for $archive." >&2; exit 1; }

mkdir -p "$prefix"
tar -xzf "$tmp/$archive" -C "$tmp"
archive_root="$tmp/localreview_${os}_${arch}/bin"
[ -x "$archive_root/localreview" ] && [ -x "$archive_root/localreviewd" ] || { echo "Release archive is malformed." >&2; exit 1; }
install -m 0755 "$archive_root/localreview" "$prefix/localreview"
install -m 0755 "$archive_root/localreviewd" "$prefix/localreviewd"
echo "Installed localreview and localreviewd in $prefix"
case ":$PATH:" in *":$prefix:"*) ;; *) echo "Add $prefix to PATH to use localreview." ;; esac
