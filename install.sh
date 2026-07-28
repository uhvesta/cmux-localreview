#!/usr/bin/env sh
# Install a checksummed GitHub Release archive. No package manager, Node, Bun,
# or source checkout is required at runtime.
set -eu

repo="${LOCALREVIEW_REPOSITORY:-uhvesta/cmux-localreview}"
version="${LOCALREVIEW_VERSION:-latest}"
prefix="${LOCALREVIEW_INSTALL_PREFIX:-$HOME/.local/bin}"
release_base="${LOCALREVIEW_RELEASE_BASE_URL:-https://github.com/$repo/releases}"
setup_workspace=
setup_personal=0

usage() {
  cat <<'EOF'
Usage: sh install.sh [options]

Install the SHA-256-checksummed native release for this macOS or Linux host.

Options:
  --prefix PATH       Install launchers in PATH (default: ~/.local/bin)
  --setup WORKSPACE   Also install managed Copilot project skills in WORKSPACE
  --personal-skills   With --setup, also install managed skills in ~/.copilot
  -h, --help          Show this help

The installer never writes OAuth credentials. Configure the dedicated public
GitHub OAuth client IDs afterwards with `localreview github-app configure`.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix) [ "$#" -ge 2 ] || { echo "--prefix requires a path" >&2; exit 2; }; prefix=$2; shift 2 ;;
    --setup) [ "$#" -ge 2 ] || { echo "--setup requires a workspace path" >&2; exit 2; }; setup_workspace=$2; shift 2 ;;
    --personal-skills) setup_personal=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done
[ "$setup_personal" -eq 0 ] || [ -n "$setup_workspace" ] || { echo "--personal-skills requires --setup WORKSPACE" >&2; exit 2; }

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
  version=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$release_base/latest" | sed 's#.*/tag/##')
fi
[ -n "$version" ] || { echo "Could not resolve a GitHub Release version." >&2; exit 1; }

archive="localreview_${os}_${arch}.tar.gz"
base="$release_base/download/$version"
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

# Keep the migration-era command names useful for people upgrading from a
# release archive as well as from a source checkout.  The aliases are tiny
# launchers in the same owner-controlled prefix; none of them invoke Node,
# Bun, or a retired TypeScript entry point.  This deliberately mirrors the
# source installer so a documented skill behaves the same whichever supported
# installation path the operator chose.
for command in \
  cmux-localreview:open \
  global-daemon:'daemon run' \
  queue-submit:submit \
  localreview-submit:submit \
  localreview-open:open \
  localreview-demo:open \
  localreview-setup:setup \
  localreview-github-app:github-app \
  localreview-reproduce:reproduce \
  localreview-reproduce-copilot:'reproduce --copilot' \
  localreview-remote:remote \
  localreview-remote-daemon:'remote daemon'; do
  name=${command%%:*}
  native_args=${command#*:}
  cat >"$prefix/$name" <<EOF
#!/usr/bin/env sh
exec "\$(dirname "\$0")/localreview" $native_args "\$@"
EOF
  chmod 0755 "$prefix/$name"
done

if [ -n "$setup_workspace" ]; then
  if [ "$setup_personal" -eq 1 ]; then
    "$prefix/localreview" setup --personal "$setup_workspace"
  else
    "$prefix/localreview" setup "$setup_workspace"
  fi
fi

echo "Installed localreview, localreviewd, and compatible native aliases in $prefix"
case ":$PATH:" in *":$prefix:"*) ;; *) echo "Add $prefix to PATH to use localreview." ;; esac
