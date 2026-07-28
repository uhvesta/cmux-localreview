#!/usr/bin/env sh
# Exercise the native half of Electron's sidecar contract without requiring a
# display server: cold launch, loopback health, idle RSS, persisted data-dir
# reuse after restart, and --parent-pid orphan cleanup.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
daemon=${LOCALREVIEWD_PATH:-"$root/bazel-bin/cmd/localreviewd/localreviewd_/localreviewd"}
max_rss_kb=${LOCALREVIEW_MAX_IDLE_RSS_KB:-51200}

if [ ! -x "$daemon" ]; then
  printf '%s\n' "build //cmd/localreviewd:localreviewd first, or set LOCALREVIEWD_PATH to an executable" >&2
  exit 2
fi
command -v curl >/dev/null || { printf '%s\n' 'curl is required for the loopback health check' >&2; exit 2; }
command -v ps >/dev/null || { printf '%s\n' 'ps is required for the RSS check' >&2; exit 2; }

scratch=$(mktemp -d "${TMPDIR:-/tmp}/localreview-electron-lifecycle.XXXXXX")
data_dir="$scratch/data"
daemon_pid=
parent_pid=

cleanup() {
  [ -z "$parent_pid" ] || kill "$parent_pid" 2>/dev/null || true
  [ -z "$daemon_pid" ] || kill "$daemon_pid" 2>/dev/null || true
  rm -rf "$scratch"
}
trap cleanup EXIT HUP INT TERM

read_field() {
  field=$1
  sed -n "s/.*\"$field\"[[:space:]]*:[[:space:]]*\([^,}]*\).*/\1/p" "$data_dir/daemon.json" | tr -d ' "' | head -n 1
}

wait_for_health() {
  tries=0
  while [ "$tries" -lt 100 ]; do
    if [ -f "$data_dir/daemon.json" ]; then
      port=$(read_field port || true)
      pid=$(read_field pid || true)
      if [ -n "$port" ] && [ -n "$pid" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
        printf '%s %s\n' "$pid" "$port"
        return 0
      fi
    fi
    tries=$((tries + 1))
    sleep 0.1
  done
  printf '%s\n' 'localreviewd did not become healthy within 10 seconds' >&2
  return 1
}

wait_for_exit() {
  watched=$1
  tries=0
  while [ "$tries" -lt 50 ]; do
    if ! kill -0 "$watched" 2>/dev/null; then return 0; fi
    tries=$((tries + 1))
    sleep 0.1
  done
  printf '%s\n' "localreviewd PID $watched did not exit" >&2
  return 1
}

start_service_daemon() {
  "$daemon" --port=0 --data-dir="$data_dir" >"$scratch/daemon.log" 2>&1 &
  daemon_pid=$!
  health=$(wait_for_health)
  health_pid=${health%% *}
  [ "$health_pid" = "$daemon_pid" ] || { printf '%s\n' 'health PID did not match spawned daemon' >&2; return 1; }
}

# Cold start and idle memory target from the migration spec.
start_service_daemon
rss=$(ps -o rss= -p "$daemon_pid" | tr -d '[:space:]')
case "$rss" in ''|*[!0-9]*) printf '%s\n' "could not read RSS for PID $daemon_pid" >&2; exit 1;; esac
[ "$rss" -le "$max_rss_kb" ] || { printf '%s\n' "idle RSS ${rss}KB exceeds ${max_rss_kb}KB target" >&2; exit 1; }
[ -s "$data_dir/daemon.db" ] || { printf '%s\n' 'daemon did not create its durable SQLite database' >&2; exit 1; }
first_pid=$daemon_pid
kill -TERM "$daemon_pid"
wait_for_exit "$daemon_pid"
daemon_pid=

# Reuse the same profile: a new sidecar must come up and retain the durable DB.
start_service_daemon
[ "$daemon_pid" != "$first_pid" ] || { printf '%s\n' 'restart reused an exited PID unexpectedly' >&2; exit 1; }
[ -s "$data_dir/daemon.db" ] || { printf '%s\n' 'restart lost the durable SQLite database' >&2; exit 1; }
kill -TERM "$daemon_pid"
wait_for_exit "$daemon_pid"
daemon_pid=

# Simulate Electron crashing: an independent shell is the supplied parent;
# the sidecar must stop once that parent exits, rather than relying on a UI
# quit handler to clean it up.
pid_file="$scratch/watchdog.pid"
release_file="$scratch/release-parent"
sh -c '
  "$1" --port=0 --data-dir="$2" --parent-pid="$$" >"$3" 2>&1 &
  echo "$!" >"$4"
  while [ ! -e "$5" ]; do sleep 1; done
' sh "$daemon" "$data_dir" "$scratch/watchdog.log" "$pid_file" "$release_file" &
parent_pid=$!
tries=0
while [ ! -s "$pid_file" ] && [ "$tries" -lt 50 ]; do tries=$((tries + 1)); sleep 0.1; done
[ -s "$pid_file" ] || { printf '%s\n' 'watchdog parent did not start sidecar' >&2; exit 1; }
daemon_pid=$(tr -d '[:space:]' < "$pid_file")
health=$(wait_for_health)
[ "${health%% *}" = "$daemon_pid" ] || { printf '%s\n' 'watchdog health PID did not match child' >&2; exit 1; }
kill -TERM "$parent_pid"
wait_for_exit "$parent_pid" || true
parent_pid=
wait_for_exit "$daemon_pid"
daemon_pid=

printf '%s\n' "Electron sidecar lifecycle passed (idle RSS ${rss}KB; limit ${max_rss_kb}KB)"
