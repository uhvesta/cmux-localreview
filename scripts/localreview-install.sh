#!/usr/bin/env sh
# Build this checkout's native artifacts and install only owner-controlled
# launchers. Bun is used here solely to build the React assets; no installed
# launcher invokes Bun, Node, or src/.
set -eu

usage() {
  cat <<'EOF'
Usage: sh scripts/localreview-install.sh [options] [project]

Build this checkout and install native localreview/localreviewd binaries to
PREFIX/bin. Historical command names become tiny native aliases so existing
skills fail over without a Bun/TypeScript daemon.

Options:
  --prefix PATH       User-owned install prefix (default: ~/.local)
  --dry-run           Print actions without writing
  --no-ui-stage       Skip the optional UI staging preflight
  --no-project-setup  Do not install Copilot skills in [project]
  -h, --help          Show this help
EOF
}

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
source_root=$(CDPATH= cd "$script_dir/.." && pwd)
prefix=${HOME}/.local
dry_run=0
ui_stage=1
project_setup=1
project=

while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix) [ "$#" -ge 2 ] || { echo "--prefix requires a path" >&2; exit 2; }; prefix=$2; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    # Bazel owns the authoritative embedded UI archive.  This only avoids the
    # redundant, early staging preflight; it must not imply that a stale
    # checked-in bundle can be reused or that the native daemon needs a JS
    # runtime after installation.
    --no-ui-stage|--no-ui-build) ui_stage=0; shift ;;
    --no-project-setup) project_setup=0; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    -*) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
    *) [ -z "$project" ] || { echo "Only one project path is accepted." >&2; exit 2; }; project=$1; shift ;;
  esac
done
[ "$#" -eq 0 ] || { echo "Unexpected argument: $1" >&2; exit 2; }

case "$(uname -s)" in Darwin|Linux) ;; *) echo "Install is supported on macOS and Linux." >&2; exit 2 ;; esac
command -v bazel >/dev/null 2>&1 || { echo "Bazel (or Bazelisk) is required to build native localreview binaries." >&2; exit 127; }

case "$prefix" in
  /*) ;;
  *) prefix=$(cd "$(dirname "$prefix")" 2>/dev/null && pwd)/$(basename "$prefix") ;;
esac
bin_dir=$prefix/bin

say() { printf '%s\n' "$*"; }
run() { if [ "$dry_run" -eq 1 ]; then say "dry-run: $*"; else "$@"; fi; }

# The current Bazel UI action intentionally uses Bun to produce the Vite
# archive at *build time*.  The installed Go binaries never invoke it.  Keep
# this check unconditional so a fresh install fails clearly rather than later
# inside Bazel; --no-ui-stage only skips a redundant diagnostic preflight.
command -v bun >/dev/null 2>&1 || { echo "Bun is required to build the embedded React UI from source; installed native binaries do not require it." >&2; exit 127; }

if [ "$ui_stage" -eq 1 ]; then
  if [ "$dry_run" -eq 1 ]; then
    say "dry-run: (cd $source_root && bash scripts/stage-ui-assets.sh)"
  else
    (cd "$source_root" && bash scripts/stage-ui-assets.sh)
  fi
fi

if [ "$dry_run" -eq 1 ]; then
  say "dry-run: (cd $source_root && bazel build //cmd/localreview:localreview //cmd/localreviewd:localreviewd)"
  say "dry-run: install native binaries in $bin_dir"
else
  (cd "$source_root" && bazel build //cmd/localreview:localreview //cmd/localreviewd:localreviewd)
  cli_output=$(cd "$source_root" && bazel cquery --output=files //cmd/localreview:localreview | tail -n 1)
  daemon_output=$(cd "$source_root" && bazel cquery --output=files //cmd/localreviewd:localreviewd | tail -n 1)
  case "$cli_output" in /*) cli_binary=$cli_output ;; *) cli_binary=$source_root/$cli_output ;; esac
  case "$daemon_output" in /*) daemon_binary=$daemon_output ;; *) daemon_binary=$source_root/$daemon_output ;; esac
  [ -x "$cli_binary" ] && [ -x "$daemon_binary" ] || { echo "Bazel did not produce executable localreview binaries." >&2; exit 1; }
  mkdir -p "$bin_dir"
  install -m 0755 "$cli_binary" "$bin_dir/localreview"
  install -m 0755 "$daemon_binary" "$bin_dir/localreviewd"

  # Keep historical shell/skill names working while ensuring they enter the
  # same Go binary. Each wrapper is created in the user-owned prefix only.
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
    cat >"$bin_dir/$name" <<EOF
#!/usr/bin/env sh
exec "\$(dirname "\$0")/localreview" $native_args "\$@"
EOF
    chmod 0755 "$bin_dir/$name"
  done
fi

if [ -n "$project" ] && [ "$project_setup" -eq 1 ]; then
  if [ "$dry_run" -eq 1 ]; then
    say "dry-run: $bin_dir/localreview setup $project"
  else
    "$bin_dir/localreview" setup "$project"
  fi
fi

say "Installed native cmux-localreview binaries in $bin_dir"
say "Add $bin_dir to PATH yourself if it is not already present. No shell profile or credentials were changed."
