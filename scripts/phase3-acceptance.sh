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
  scripts/phase3-acceptance.sh report --session DIR
  scripts/phase3-acceptance.sh verify-two --session DIR --session DIR

prepare creates a disposable two-repository review workspace and separate web
and Electron data directories. It builds the daemon, CLI, and packaged desktop
artifact unless --skip-build is supplied. It does not authenticate, contact
GitHub/Copilot, or mark any checklist item as passed.

launch-web starts the isolated daemon and opens its one-time local browser URL.
launch-electron launches the unpacked packaged artifact with a different clean
profile. Run each checklist item through computer-use, then record the observed
result. The ledger is append-only: later retries do not erase prior failures.

report validates the ledger shape and prints an auditable structural summary of
one session. verify-two does the same for two distinct sessions and returns
success only when every latest web/Electron observation is a pass. Neither
command certifies that OAuth or Copilot was real: a reviewer must inspect the
notes and record both clean passes in MIGRATION_LOG.md before Phase 3 can pass.
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

validate_ledger() {
  local session=$1 ledger="$1/evidence.tsv"
  [ -f "$ledger" ] || die "missing evidence ledger: $ledger"
  # Keep this intentionally small and portable: a record command writes exactly
  # five tab-separated columns and rejects control characters in the note.
  # Validation catches hand-edited/corrupted rows before a report can be used as
  # review evidence.
  awk -F '\t' '
    NR == 1 {
      if ($0 != "timestamp\tsurface\titem\tresult\tnote") {
        printf "invalid evidence header at line 1\n" > "/dev/stderr"; exit 1
      }
      next
    }
    NF != 5 || $1 == "" || ($2 != "web" && $2 != "electron") ||
      ($3 !~ /^[1-7]$/) || ($4 != "pass" && $4 != "fail") || $5 == "" {
      printf "invalid evidence row at line %d\n", NR > "/dev/stderr"; exit 1
    }
  ' "$ledger" || die "evidence ledger is malformed; do not use it as Phase 3 evidence"
}

manifest_value() {
  local session=$1 key=$2
  # The manifest is written by prepare with one scalar per line. This is not a
  # general JSON parser; failing closed is preferable to treating a hand-edited
  # manifest as proof of a clean profile.
  sed -n "s/^  \"$key\": \"\(.*\)\",\{0,1\}\$/\1/p" "$session/manifest.json" | head -n 1
}

latest_result() {
  local session=$1 surface=$2 item=$3
  awk -F '\t' -v s="$surface" -v i="$item" '$2 == s && $3 == i { result=$4 } END { print result }' "$session/evidence.tsv"
}

session_is_structurally_complete() {
  local session=$1 surface item result
  validate_ledger "$session"
  for surface in web electron; do
    for item in 1 2 3 4 5 6 7; do
      result=$(latest_result "$session" "$surface" "$item")
      [ "$result" = pass ] || return 1
    done
  done
}

prior_failure_count() {
  local session=$1
  awk -F '\t' 'NR > 1 && $4 == "fail" { count++ } END { print count + 0 }' "$session/evidence.tsv"
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

Run \`scripts/phase3-acceptance.sh report --session "$session"\` before
copying evidence into the migration log. Do not treat a fully populated ledger
as final Phase 3 certification until its notes are independently reviewed and
two complete, distinct clean sessions pass \`verify-two\` and are recorded in
\`MIGRATION_LOG.md\`.
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
      local result
      result=$(latest_result "$session" "$surface" "$item")
      if [ -n "$result" ]; then
        printf '  %-8s item %s: %s\n' "$surface" "$item" "$result"
      else
        printf '  %-8s item %s: pending\n' "$surface" "$item"
      fi
    done
  done
  printf '  observations: %s; prior failed attempts: %s\n' "$(awk 'END { print (NR > 0 ? NR - 1 : 0) }' "$session/evidence.tsv")" "$(prior_failure_count "$session")"
  printf '%s\n' 'This status is evidence only. It never certifies OAuth, Copilot, or Phase 3 completion.'
}

report() {
  [ "$#" -eq 2 ] && [ "$1" = '--session' ] || die 'report requires --session DIR'
  local session commit workspace web_profile electron_profile failures
  session=$(session_path "$2")
  validate_ledger "$session"
  commit=$(manifest_value "$session" gitCommit)
  workspace=$(manifest_value "$session" workspace)
  web_profile=$(manifest_value "$session" webDataDir)
  electron_profile=$(manifest_value "$session" electronDataDir)
  [ -n "$commit" ] && [ -n "$workspace" ] && [ -n "$web_profile" ] && [ -n "$electron_profile" ] || die 'manifest lacks clean-session identity; prepare a new session'
  failures=$(prior_failure_count "$session")
  printf 'Phase 3 structural evidence report\n'
  printf '  session: %s\n  commit: %s\n  workspace: %s\n  web profile: %s\n  Electron profile: %s\n' "$session" "$commit" "$workspace" "$web_profile" "$electron_profile"
  status --session "$session"
  if session_is_structurally_complete "$session"; then
    printf '  structural result: complete (all 14 latest observations are pass; retries retained: %s)\n' "$failures"
  else
    printf '  structural result: incomplete (every latest observation must be pass)\n'
  fi
  printf '%s\n' 'This is not a Phase 3 certification. Inspect notes, verify real OAuth/Copilot behavior, and copy both clean-session reports into MIGRATION_LOG.md.'
}

verify_two() {
  local first='' second=''
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --session)
        [ "$#" -ge 2 ] || die '--session needs a directory'
        if [ -z "$first" ]; then first=$(session_path "$2"); elif [ -z "$second" ]; then second=$(session_path "$2"); else die 'verify-two accepts exactly two --session values'; fi
        shift 2 ;;
      *) die "unknown verify-two argument: $1" ;;
    esac
  done
  [ -n "$first" ] && [ -n "$second" ] || die 'verify-two requires two --session values'
  [ "$first" != "$second" ] || die 'verify-two requires two distinct clean sessions'
  local first_workspace second_workspace first_web second_web first_electron second_electron
  first_workspace=$(manifest_value "$first" workspace); second_workspace=$(manifest_value "$second" workspace)
  first_web=$(manifest_value "$first" webDataDir); second_web=$(manifest_value "$second" webDataDir)
  first_electron=$(manifest_value "$first" electronDataDir); second_electron=$(manifest_value "$second" electronDataDir)
  [ -n "$first_workspace" ] && [ -n "$second_workspace" ] && [ -n "$first_web" ] && [ -n "$second_web" ] && [ -n "$first_electron" ] && [ -n "$second_electron" ] || die 'one session has no clean-profile identity; prepare it again'
  [ "$first_workspace" != "$second_workspace" ] && [ "$first_web" != "$second_web" ] && [ "$first_electron" != "$second_electron" ] || die 'sessions reuse a workspace or profile and are not two clean passes'
  report --session "$first"
  report --session "$second"
  if ! session_is_structurally_complete "$first" || ! session_is_structurally_complete "$second"; then
    die 'two-session structural check is incomplete; it is not Phase 3 evidence yet'
  fi
  printf '%s\n' 'Two distinct sessions are structurally complete. This is still not Phase 3 certification: independently review notes, authenticated behavior, and MIGRATION_LOG.md before proceeding to Phase 4.'
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
  report) report "$@" ;;
  verify-two) verify_two "$@" ;;
  -h|--help|help) usage ;;
  *) die "unknown command: $command" ;;
esac
