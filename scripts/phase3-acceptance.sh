#!/usr/bin/env bash
# Prepare and journal a *manual* clean-profile Phase 3 acceptance session.
#
# This script deliberately never starts an OAuth flow and never calls Copilot.
# Its job is to make the real browser and packaged-Electron checks repeatable:
# it creates a disposable multi-repository workspace, isolated daemon/Electron
# profiles, exact build artifacts, and a durable evidence ledger.  Recording a
# result is intentionally an explicit human action after computer-use testing.
set -euo pipefail

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"

usage() {
  cat <<'EOF'
Usage:
  scripts/phase3-acceptance.sh prepare [--output DIR] [--skip-build]
  scripts/phase3-acceptance.sh launch-web --session DIR
  scripts/phase3-acceptance.sh launch-electron --session DIR
  scripts/phase3-acceptance.sh record --session DIR --surface web|electron \
    --item 1..7 --result pass|fail --note TEXT
  scripts/phase3-acceptance.sh status --session DIR

prepare creates a disposable two-repository review workspace and separate web
and Electron data directories. It builds the daemon, CLI, and packaged desktop
artifact unless --skip-build is supplied. It does not authenticate, contact
GitHub/Copilot, or mark any checklist item as passed.

launch-web starts the isolated daemon and opens its one-time local browser URL.
launch-electron launches the unpacked packaged artifact with a different clean
profile. Run each checklist item through computer-use, then record the observed
result. The ledger is append-only: later retries do not erase prior failures.
EOF
}

die() { printf '%s\n' "$*" >&2; exit 2; }

require_dir() { [ -d "$1" ] || die "session directory does not exist: $1"; }

session_path() {
  local value=$1
  require_dir "$value"
  [ -f "$value/manifest.json" ] || die "not a Phase 3 acceptance session: $value"
  printf '%s\n' "$(cd "$value" && pwd)"
}

write_checklist() {
  local session=$1
  cat >"$session/CHECKLIST.md" <<EOF
# Phase 3 manual acceptance checklist

Session: \`$session\`

This checklist is a record of **observed computer-use behavior**, not an
automated certification. Use the two launch scripts from this session so each
surface starts from a separate clean profile. Record results only after making
the corresponding interaction in the real UI.

| Item | Web | Packaged Electron | Required observation |
| --- | --- | --- | --- |
| 1 | pending | pending | Two nested repositories; staged, unstaged and untracked changes; tree, split/unified/full-file, gates and context expansion. |
| 2 | pending | pending | Create/edit/resolve a normal and expanded/full-file comment; restart; verify persistence and workspace-relative export paths. |
| 3 | pending | pending | Real Copilot \`/ask\` stream, cancel, resume, model switch and persisted picker value; \`/btw\` stays separate. Do not substitute the fixture. |
| 4 | pending | pending | \`localreview submit\`, Queue Home open, complete and requeue a round. |
| 5 | pending | pending | Fresh-profile loopback OAuth; terminal device-flow; secure-store token; logout clears local state. |
| 6 | pending | pending | Cold start, durable review action, quit kills sidecar, relaunch retains state and has a new sidecar PID. |
| 7 | pending | pending | Record idle and post-checklist daemon RSS; idle is below 51200 KB and repeated clean sessions show no growth trend. |

The runner deliberately leaves every cell pending. Use:

~~~sh
scripts/phase3-acceptance.sh record --session "$session" --surface web --item 1 --result pass --note 'what was clicked and observed'
~~~

Do not treat a fully populated ledger as final Phase 3 certification until its
evidence is independently reviewed and two complete clean sessions are recorded
in \`MIGRATION_LOG.md\`.
EOF
}

make_repo() {
  local repo=$1 name=$2
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" config user.email phase3-fixture@example.invalid
  git -C "$repo" config user.name 'Phase 3 fixture'
  printf '# %s\n\nconst baseline = 1;\n' "$name" >"$repo/review.ts"
  printf 'baseline\n' >"$repo/README.md"
  git -C "$repo" add review.ts README.md
  git -C "$repo" commit -qm 'fixture baseline'
  printf '# %s\n\nconst staged = 2;\n' "$name" >"$repo/review.ts"
  git -C "$repo" add review.ts
  printf 'working tree change\n' >>"$repo/README.md"
  printf 'untracked fixture\n' >"$repo/untracked.txt"
}

find_desktop_executable() {
  local base=$1 app
  app=$(find "$base" -type d -name '*.app' -print -quit 2>/dev/null || true)
  if [ -n "$app" ]; then
    find "$app/Contents/MacOS" -type f -perm -111 -print -quit
    return
  fi
  find "$base" -type f -perm -111 \( -name 'CMUX Local Review' -o -name 'cmux-localreview-desktop' -o -name 'cmux-localreview' \) -print -quit
}

prepare() {
  local output='' skip_build=0
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --output) [ "$#" -ge 2 ] || die '--output needs a directory'; output=$2; shift 2 ;;
      --skip-build) skip_build=1; shift ;;
      -h|--help) usage; return ;;
      *) die "unknown prepare argument: $1" ;;
    esac
  done
  if [ -z "$output" ]; then output=$(mktemp -d "${TMPDIR:-/tmp}/cmux-localreview-phase3.XXXXXX"); fi
  if [ -e "$output" ]; then [ -d "$output" ] && [ -z "$(find "$output" -mindepth 1 -maxdepth 1 -print -quit)" ] || die "output must be a new or empty directory: $output"; fi
  mkdir -p "$output"
  output=$(cd "$output" && pwd)

  if [ "$skip_build" -eq 0 ]; then
    command -v bazel >/dev/null || die 'bazel is required to prepare Phase 3 artifacts'
    bazel build //cmd/localreviewd:localreviewd //cmd/localreview:localreview //desktop:app
  fi
  local daemon="$root/bazel-bin/cmd/localreviewd/localreviewd_/localreviewd"
  local cli="$root/bazel-bin/cmd/localreview/localreview_/localreview"
  [ -x "$daemon" ] || die "native daemon is missing; run bazel build //cmd/localreviewd:localreviewd"
  [ -x "$cli" ] || die "native CLI is missing; run bazel build //cmd/localreview:localreview"
  local archive="$root/bazel-bin/desktop/cmux-localreview-desktop.tar.gz"
  local staged_desktop="$root/bazel-bin/desktop/desktop-stage/dist"
  # A completed Bazel target publishes the tarball.  During an explicit
  # --skip-build reuse, Bazel's output tree may retain the unpacked staged app
  # while its symlink forest has been cleaned; it is still the same packaged
  # artifact and is suitable for a manual local session.
  [ -f "$archive" ] || [ -d "$staged_desktop" ] || die "desktop artifact is missing; run bazel build //desktop:app"

  mkdir -p "$output/workspace/alpha" "$output/workspace/nested/beta" "$output/profiles/web" "$output/profiles/electron" "$output/artifacts/electron" "$output/logs"
  make_repo "$output/workspace/alpha" alpha
  make_repo "$output/workspace/nested/beta" beta
  if [ -f "$archive" ]; then
    tar -xzf "$archive" -C "$output/artifacts/electron"
  else
    cp -R "$staged_desktop/." "$output/artifacts/electron/"
  fi
  local electron
  electron=$(find_desktop_executable "$output/artifacts/electron")
  [ -n "$electron" ] && [ -x "$electron" ] || die 'could not locate executable in packaged Electron archive'
  cp "$daemon" "$output/artifacts/localreviewd"
  cp "$cli" "$output/artifacts/localreview"
  chmod 0755 "$output/artifacts/localreviewd" "$output/artifacts/localreview"
  printf '%s\n' "$electron" >"$output/artifacts/electron-executable"

  cat >"$output/manifest.json" <<EOF
{
  "format": 1,
  "createdAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "gitCommit": "$(git rev-parse HEAD)",
  "workspace": "$output/workspace",
  "daemon": "$output/artifacts/localreviewd",
  "cli": "$output/artifacts/localreview",
  "electronExecutable": "$electron",
  "webDataDir": "$output/profiles/web",
  "electronDataDir": "$output/profiles/electron",
  "credentialPolicy": "empty clean profiles; no credential is copied or created by prepare"
}
EOF
  : >"$output/evidence.tsv"
  printf 'timestamp\tsurface\titem\tresult\tnote\n' >"$output/evidence.tsv"
  write_checklist "$output"
  cat >"$output/launch-web.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
export CMUX_LOCALREVIEW_DATA_DIR='$output/profiles/web'
exec '$output/artifacts/localreview' open '$output/workspace'
EOF
  cat >"$output/launch-electron.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
export CMUX_LOCALREVIEW_DATA_DIR='$output/profiles/electron'
exec '$electron'
EOF
  chmod 0755 "$output/launch-web.sh" "$output/launch-electron.sh"
  cat >"$output/README.md" <<EOF
# Disposable Phase 3 session

This directory has no copied credential. It contains two nested modified Git
repositories under \`workspace/\`, isolated web/Electron SQLite profiles, and a
packaged Electron artifact built from commit \`$(git rev-parse --short HEAD)\`.

Run \`./launch-web.sh\` and \`./launch-electron.sh\` independently. For each
surface, drive all seven checks in \`CHECKLIST.md\` using computer-use. Record
every observed result through \`scripts/phase3-acceptance.sh record\`; do not
edit \`evidence.tsv\` by hand. Keep this directory until a reviewer has copied
the evidence into \`MIGRATION_LOG.md\`.
EOF
  printf 'Prepared clean Phase 3 acceptance session: %s\n' "$output"
  printf 'Web:      %s/launch-web.sh\nElectron: %s/launch-electron.sh\nChecklist: %s/CHECKLIST.md\n' "$output" "$output" "$output"
}

launch() {
  local kind=$1; shift
  [ "$#" -eq 2 ] && [ "$1" = '--session' ] || die "launch-$kind requires --session DIR"
  local session
  session=$(session_path "$2")
  exec "$session/launch-$kind.sh"
}

record() {
  local session='' surface='' item='' result='' note=''
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --session|--surface|--item|--result|--note)
        [ "$#" -ge 2 ] || die "$1 needs a value"
        case "$1" in
          --session) session=$2 ;; --surface) surface=$2 ;; --item) item=$2 ;; --result) result=$2 ;; --note) note=$2 ;;
        esac
        shift 2 ;;
      *) die "unknown record argument: $1" ;;
    esac
  done
  session=$(session_path "$session")
  case "$surface" in web|electron) ;; *) die '--surface must be web or electron' ;; esac
  case "$item" in 1|2|3|4|5|6|7) ;; *) die '--item must be 1 through 7' ;; esac
  case "$result" in pass|fail) ;; *) die '--result must be pass or fail' ;; esac
  [ -n "$note" ] || die '--note must describe the observed interaction'
  # A TSV ledger is intentionally log-shaped and preserves retries. Reject tabs
  # and newlines so one observation can never corrupt another observation.
  case "$note" in *$'\t'*|*$'\n'*) die '--note may not contain tabs or newlines' ;; esac
  printf '%s\t%s\t%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$surface" "$item" "$result" "$note" >>"$session/evidence.tsv"
  printf 'Recorded %s item %s: %s\n' "$surface" "$item" "$result"
}

status() {
  [ "$#" -eq 2 ] && [ "$1" = '--session' ] || die 'status requires --session DIR'
  local session
  session=$(session_path "$2")
  printf 'Phase 3 evidence session: %s\n' "$session"
  for surface in web electron; do
    for item in 1 2 3 4 5 6 7; do
      local latest
      latest=$(awk -F '\t' -v s="$surface" -v i="$item" '$2 == s && $3 == i { line=$0 } END { print line }' "$session/evidence.tsv")
      if [ -n "$latest" ]; then
        printf '  %-8s item %s: %s\n' "$surface" "$item" "$(printf '%s' "$latest" | cut -f4)"
      else
        printf '  %-8s item %s: pending\n' "$surface" "$item"
      fi
    done
  done
  printf '%s\n' 'This status is evidence only. It never certifies OAuth, Copilot, or Phase 3 completion.'
}

command=${1:-}
[ -n "$command" ] || { usage; exit 2; }
shift || true
case "$command" in
  prepare) prepare "$@" ;;
  launch-web) launch web "$@" ;;
  launch-electron) launch electron "$@" ;;
  record) record "$@" ;;
  status) status "$@" ;;
  -h|--help|help) usage ;;
  *) die "unknown command: $command" ;;
esac
