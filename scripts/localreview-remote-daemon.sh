#!/usr/bin/env sh
# Run this on the remote host. The daemon always binds only 127.0.0.1.
set -eu
exec "${LOCALREVIEW_BIN:-localreview}" remote daemon "$@"
