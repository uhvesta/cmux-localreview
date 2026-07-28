#!/usr/bin/env bash
# Hermetic acceptance check for the isolated localreviewd-e2e binary.
# It exercises the same loopback bearer/cookie, queue, /ask SSE, cancellation,
# model-setting, /btw, and restart-persistence routes that browser/Electron use.
set -euo pipefail

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$root"

for command in bazel curl git jq; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 2; }
done

bazel build //cmd/localreviewd-e2e:localreviewd-e2e >/dev/null
binary="$root/bazel-bin/cmd/localreviewd-e2e/localreviewd-e2e_/localreviewd-e2e"
data=$(mktemp -d "${TMPDIR:-/tmp}/cmux-localreview-e2e-check.XXXXXX")
repo=$(mktemp -d "${TMPDIR:-/tmp}/cmux-localreview-e2e-repo.XXXXXX")
daemon_pid=""

cleanup() {
  if [[ -n "$daemon_pid" ]] && kill -0 "$daemon_pid" 2>/dev/null; then kill -TERM "$daemon_pid" || true; fi
  rm -rf "$data" "$repo"
}
trap cleanup EXIT

start_daemon() {
  CMUX_LOCALREVIEW_DATA_DIR="$data" "$binary" --port 0 >"$data/daemon.log" 2>&1 &
  daemon_pid=$!
  for _ in $(seq 1 100); do
    [[ -f "$data/daemon.json" ]] && curl -fsS "http://127.0.0.1:$(jq -r .port "$data/daemon.json")/health" >/dev/null 2>&1 && return
    sleep .05
  done
  cat "$data/daemon.log" >&2 || true
  echo "E2E fixture daemon did not become ready" >&2
  exit 1
}

authenticate() {
  local port token
  port=$(jq -r .port "$data/daemon.json")
  token=$(jq -r .token "$data/daemon.json")
  origin="http://127.0.0.1:$port"
  cookie="$data/browser.cookie"
  curl -fsS -c "$cookie" -H "Authorization: Bearer $token" -X POST "$origin/api/browser/session" >/dev/null
}

api_get() { curl -fsS -b "$cookie" "$origin/api/$1"; }
api_post() { curl -fsS -b "$cookie" -H "Origin: $origin" -H 'content-type: application/json' -d "$2" "$origin/api/$1"; }

git -C "$repo" init -q
git -C "$repo" config user.email fixture@example.invalid
git -C "$repo" config user.name fixture
printf 'package fixture\n\nconst before = 1\n' >"$repo/main.go"
git -C "$repo" add main.go && git -C "$repo" commit -qm initial
printf 'package fixture\n\nconst after = 2\n' >"$repo/main.go"

start_daemon
authenticate

queue=$(api_post queue "{\"title\":\"Fixture acceptance\",\"workspacePath\":\"$repo\",\"reviewTopic\":\"fixture-e2e\"}")
item=$(jq -r .item.id <<<"$queue")
[[ "$item" != null && -n "$item" ]]
opened=$(api_post "queue/$item/open" '{}')
repo_id=$(jq -r '.repos[0].id' <<<"$opened")
[[ "$repo_id" != null && -n "$repo_id" ]]

# This is the connected SDK-shaped response used by the picker, not its offline
# fallback. It proves the E2E binary enters the intended transport path.
models=$(api_get ask/models)
jq -e '.state == "ready" and .source == "sdk" and (.models | length == 3)' <<<"$models" >/dev/null

# Follow the inline React form's precise create → subscribe → POST sequence.
# Reopening the same location must reuse the persisted shared conversation,
# and its GET must not create or replay an additional SDK prompt.
inline=$(api_post ask/inline-conversations "{\"context\":{\"repoId\":\"$repo_id\",\"filePath\":\"main.go\",\"workspacePath\":\"main.go\",\"side\":\"current\",\"startLine\":3,\"endLine\":3,\"selectedCode\":\"const after = 2\"}}")
inline_id=$(jq -r .conversation.id <<<"$inline")
[[ "$inline_id" != null && -n "$inline_id" ]]
curl -sSN --max-time 5 -b "$cookie" "$origin/api/ask/conversations/$inline_id/events" >"$data/inline-stream.txt" &
inline_stream_pid=$!
sleep .1
api_post "ask/conversations/$inline_id/messages" "{\"body\":\"Explain this selected line.\",\"location\":{\"repoId\":\"$repo_id\",\"filePath\":\"main.go\",\"workspacePath\":\"main.go\",\"side\":\"current\",\"startLine\":3,\"endLine\":3,\"selectedCode\":\"const after = 2\"}}" >/dev/null
wait "$inline_stream_pid" || true
grep -q 'event: delta' "$data/inline-stream.txt"
inline_transcript=$(api_get "ask/conversations/$inline_id")
jq -e '.messages | length == 2 and .[0].location.filePath == "main.go" and .[0].location.startLine == 3 and .[1].pending == false' <<<"$inline_transcript" >/dev/null
inline_reopen=$(api_post ask/inline-conversations "{\"context\":{\"repoId\":\"$repo_id\",\"filePath\":\"main.go\",\"workspacePath\":\"main.go\",\"side\":\"current\",\"startLine\":3,\"endLine\":3,\"selectedCode\":\"const after = 2\"}}")
jq -e --arg id "$inline_id" '.reused == true and .conversation.id == $id' <<<"$inline_reopen" >/dev/null
inline_after_reopen=$(api_get "ask/conversations/$inline_id")
jq -e '.messages | length == 2' <<<"$inline_after_reopen" >/dev/null

conversation=$(api_post ask/conversations '{"model":"gpt-5"}')
conversation_id=$(jq -r .conversation.id <<<"$conversation")
api_post "ask/conversations/$conversation_id/settings" '{"model":"claude-sonnet-4.6","reasoningEffort":"high","contextTier":"long_context"}' >/dev/null

# Subscribe before posting exactly as the React panel does, then prove model
# selection and the terminal event travel through the durable SSE transport.
curl -sSN --max-time 5 -b "$cookie" "$origin/api/ask/conversations/$conversation_id/events" >"$data/stream.txt" &
stream_pid=$!
sleep .1
api_post "ask/conversations/$conversation_id/messages" '{"body":"Stream this deterministic fixture answer."}' >/dev/null
wait "$stream_pid" || true
grep -q 'event: delta' "$data/stream.txt"
grep -q 'claude-sonnet-4.6' "$data/stream.txt"
grep -q 'event: done' "$data/stream.txt"
transcript=$(api_get "ask/conversations/$conversation_id")
jq -e '.messages | length == 2 and .[1].pending == false and (.[1].body | contains("claude-sonnet-4.6"))' <<<"$transcript" >/dev/null

# Cancellation uses a different turn and must leave a settled durable answer
# with an explicit terminal marker when a partial stream was already visible.
cancel_conversation=$(api_post ask/conversations '{"model":"gpt-5-mini"}')
cancel_id=$(jq -r .conversation.id <<<"$cancel_conversation")
api_post "ask/conversations/$cancel_id/messages" '{"body":"Cancel this visibly streaming fixture response before it finishes."}' >/dev/null
sleep .12
api_post "ask/conversations/$cancel_id/cancel" '{}' >/dev/null
sleep .1
cancelled=$(api_get "ask/conversations/$cancel_id")
jq -e '.messages | length == 2 and .[1].pending == false and (.[1].body | contains("_Response cancelled before it completed._"))' <<<"$cancelled" >/dev/null

# /btw owns a deliberately separate durable SDK thread but uses the same
# runtime contract; this catches accidental removal of that native path.
btw=$(api_post btw/ask "{\"transport\":\"copilot\",\"repoId\":\"$repo_id\",\"filePath\":\"main.go\",\"startLine\":3,\"endLine\":3,\"codeContent\":\"const after = 2\",\"question\":\"Why is this deterministic?\",\"model\":\"gpt-5-mini\"}")
jq -e '.target == "copilot-sdk"' <<<"$btw" >/dev/null
sleep 1
threads=$(api_get btw/threads)
jq -e '.threads | length == 1 and .[0].questions[0].answer.pending == false and (. [0].questions[0].answer.body | contains("Fixture Copilot"))' <<<"$threads" >/dev/null

# Restarting resets only live fixture SDK sessions. It must retain the saved
# transcript and a read must never replay a prompt.
kill -TERM "$daemon_pid"
wait "$daemon_pid" || true
daemon_pid=""
start_daemon
authenticate
after_restart=$(api_get "ask/conversations/$conversation_id")
jq -e '.messages | length == 2 and .[1].pending == false and (.[1].body | contains("claude-sonnet-4.6"))' <<<"$after_restart" >/dev/null
inline_after_restart=$(api_get "ask/conversations/$inline_id")
jq -e '.messages | length == 2 and .[0].location.selectedCode == "const after = 2" and .[1].pending == false' <<<"$inline_after_restart" >/dev/null

echo "E2E Copilot fixture acceptance passed"
