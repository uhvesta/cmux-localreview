#!/usr/bin/env bash
# Repeat the hermetic Phase 3 route checklist and make its daemon-memory
# evidence machine-readable.  This intentionally needs neither GitHub OAuth
# nor a Copilot token: localreviewd-e2e exercises the same Go HTTP/SSE/SQLite
# paths with a deterministic streaming SDK fixture.
set -euo pipefail

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"

rounds=${LOCALREVIEW_PHASE3_ROUNDS:-2}
max_rss_kb=${LOCALREVIEW_MAX_RSS_KB:-51200}
# RSS is sampled by ps and varies slightly because of allocator/page timing.
# A 4 MiB allowance rejects a monotonic leak while avoiding flaky sub-page
# comparisons on macOS and Linux.
max_trend_kb=${LOCALREVIEW_MAX_RSS_TREND_KB:-4096}

case "$rounds" in
  ''|*[!0-9]*|0) echo 'LOCALREVIEW_PHASE3_ROUNDS must be a positive integer' >&2; exit 2 ;;
esac
case "$max_rss_kb" in ''|*[!0-9]*) echo 'LOCALREVIEW_MAX_RSS_KB must be an integer' >&2; exit 2 ;; esac
case "$max_trend_kb" in ''|*[!0-9]*) echo 'LOCALREVIEW_MAX_RSS_TREND_KB must be an integer' >&2; exit 2 ;; esac

scratch=$(mktemp -d "${TMPDIR:-/tmp}/cmux-localreview-phase3-trend.XXXXXX")
trap 'rm -rf "$scratch"' EXIT

first_after=""
last_after=""
for round in $(seq 1 "$rounds"); do
  log="$scratch/round-$round.log"
  if ! LOCALREVIEW_E2E_RSS_REPORT=1 LOCALREVIEW_MAX_RSS_KB="$max_rss_kb" \
    bash scripts/verify-e2e-copilot-fixture.sh >"$log" 2>&1; then
    cat "$log" >&2
    exit 1
  fi

  idle=$(sed -n 's/^RSS_IDLE_KB=//p' "$log" | tail -n 1)
  after=$(sed -n 's/^RSS_AFTER_CHECKLIST_KB=//p' "$log" | tail -n 1)
  restarted=$(sed -n 's/^RSS_AFTER_RESTART_KB=//p' "$log" | tail -n 1)
  for sample in "$idle" "$after" "$restarted"; do
    case "$sample" in ''|*[!0-9]*) echo "round $round did not produce valid RSS samples" >&2; cat "$log" >&2; exit 1 ;; esac
  done
  printf 'Phase 3 fixture round %s: idle=%sKB after-checklist=%sKB after-restart=%sKB\n' \
    "$round" "$idle" "$after" "$restarted"

  if [[ -z "$first_after" ]]; then first_after=$after; fi
  last_after=$after
done

delta=$((last_after - first_after))
if (( delta > max_trend_kb )); then
  echo "RSS trend grew ${delta}KB across ${rounds} full checklists (limit ${max_trend_kb}KB)" >&2
  exit 1
fi

printf 'Phase 3 fixture memory trend passed: %s rounds, after-checklist delta=%sKB (limit %sKB)\n' \
  "$rounds" "$delta" "$max_trend_kb"
