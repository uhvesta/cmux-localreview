#!/usr/bin/env sh
# Configure a native daemon-to-daemon federation node. The local Go daemon
# creates the ephemeral SSH loopback forward itself; this helper deliberately
# never invokes Bun or leaves an unmanaged `ssh -L` process behind.
set -eu

if [ "$#" -ne 5 ]; then
  echo "usage: $0 <id> <label> <user@host> <remote-port> <token-file>" >&2
  echo "The token file must be mode 0600 and is read through stdin, never argv." >&2
  exit 64
fi

node_id=$1
node_label=$2
ssh_target=$3
remote_port=$4
token_file=$5

if [ ! -r "$token_file" ]; then
  echo "cannot read token file: $token_file" >&2
  exit 66
fi

cat "$token_file" | localreview federation add \
  --id "$node_id" --label "$node_label" --ssh "$ssh_target" \
  --port "$remote_port" --token-stdin
