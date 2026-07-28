#!/usr/bin/env sh
# The native federation transport is not cut over yet. Refuse before loading
# the historical TypeScript tunnel helper so this script cannot start a Bun
# control plane by accident.
set -eu
echo "SSH tunnel setup is not available in the native CLI yet. Use localreview remote status for configured nodes; the legacy Bun tunnel helper is retired." >&2
exit 64
