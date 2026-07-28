#!/usr/bin/env sh
# Build this checkout and install lightweight Bun launchers below a user-owned
# prefix. No sudo, package-manager changes, PATH edits, or source copying.
set -eu

usage() {
  cat <<'EOF'
Usage: sh scripts/localreview-install.sh [options] [project]

Build this checkout and install cmux-localreview CLI launchers to PREFIX/bin.
The launchers keep using this checkout, so leave it in place or reinstall from
the replacement checkout. No sudo, shell profile, Git, or credential changes.

Options:
  --prefix PATH       User-owned install prefix (default: ~/.local)
  --dry-run           Print build/install/setup actions without writing
  --no-deps           Skip `bun install --frozen-lockfile`
  --no-build          Skip the production client build (not recommended)
  --no-project-setup  Do not install Copilot skills in [project]
  -h, --help          Show this help

When [project] is supplied, managed Copilot skills are also installed there and
are configured to call PREFIX/bin/localreview-submit.
EOF
}

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
source_root=$(CDPATH= cd "$script_dir/.." && pwd)
prefix=${HOME}/.local
dry_run=0
build=1
dependencies=1
project_setup=1
project=

while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix) [ "$#" -ge 2 ] || { echo "--prefix requires a path" >&2; exit 2; }; prefix=$2; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    --no-deps) dependencies=0; shift ;;
    --no-build) build=0; shift ;;
    --no-project-setup) project_setup=0; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    -*) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
    *) [ -z "$project" ] || { echo "Only one project path is accepted." >&2; exit 2; }; project=$1; shift ;;
  esac
done
[ "$#" -eq 0 ] || { echo "Unexpected argument: $1" >&2; exit 2; }

case "$(uname -s)" in Darwin|Linux) ;; *) echo "Install is supported on macOS and Linux." >&2; exit 2 ;; esac
command -v bun >/dev/null 2>&1 || { echo "Bun is required. Install Bun, then rerun this command." >&2; exit 127; }

case "$prefix" in
  /*) ;;
  *) prefix=$(cd "$(dirname "$prefix")" 2>/dev/null && pwd)/$(basename "$prefix") ;;
esac
bin_dir=$prefix/bin
stage_dir=$prefix/.cmux-localreview-stage-$$

say() { printf '%s\n' "$*"; }
run() { if [ "$dry_run" -eq 1 ]; then say "dry-run: $*"; else "$@"; fi; }

if [ "$dependencies" -eq 1 ]; then
  if [ "$dry_run" -eq 1 ]; then say "dry-run: (cd $source_root && bun install --frozen-lockfile)"; else (cd "$source_root" && bun install --frozen-lockfile); fi
fi

if [ "$build" -eq 1 ]; then
  if [ "$dry_run" -eq 1 ]; then say "dry-run: (cd $source_root && bun run build)"; else (cd "$source_root" && bun run build); fi
fi

if [ "$dry_run" -eq 1 ]; then
  say "dry-run: create launchers in $bin_dir"
else
  mkdir -p "$stage_dir/bin" "$bin_dir"
  trap 'rm -rf "$stage_dir"' EXIT HUP INT TERM
  for command in \
    cmux-localreview:src/cli.ts \
    global-daemon:src/global-daemon.ts \
    queue-submit:src/queue-submit.ts \
    localreview-submit:src/queue-submit.ts \
    localreview-open:src/localreview-open.ts \
    localreview-demo:src/localreview-demo.ts \
    localreview-setup:src/localreview-setup.ts \
    localreview-reproduce:src/localreview-reproduce.ts \
    localreview-reproduce-copilot:src/localreview-reproduce-copilot.ts \
    localreview-remote:src/localreview-remote.ts \
    localreview-remote-daemon:src/localreview-remote-daemon.ts; do
    name=${command%%:*}
    entry=${command#*:}
    cat >"$stage_dir/bin/$name" <<EOF
#!/usr/bin/env sh
exec bun "$source_root/$entry" "\$@"
EOF
    chmod 700 "$stage_dir/bin/$name"
  done
  # Build has already succeeded. Each rename replaces only an executable we
  # own; an interrupted staging phase leaves existing launchers intact.
  for launcher in "$stage_dir/bin"/*; do mv -f "$launcher" "$bin_dir/$(basename "$launcher")"; done
  rmdir "$stage_dir/bin" "$stage_dir"
  trap - EXIT HUP INT TERM
fi

if [ -n "$project" ] && [ "$project_setup" -eq 1 ]; then
  if [ "$dry_run" -eq 1 ]; then
    say "dry-run: bun $source_root/src/localreview-setup.ts --command $bin_dir/localreview-submit --reproduce-command $bin_dir/localreview-reproduce-copilot $project"
  else
    bun "$source_root/src/localreview-setup.ts" --command "$bin_dir/localreview-submit" --reproduce-command "$bin_dir/localreview-reproduce-copilot" "$project"
  fi
fi

say "Installed cmux-localreview launchers in $bin_dir"
say "Add $bin_dir to PATH yourself if it is not already present. No shell profile was changed."
