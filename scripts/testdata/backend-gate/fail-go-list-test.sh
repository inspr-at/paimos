#!/usr/bin/env bash
set -euo pipefail

if [[ " $* " == *' list '* && " $* " == *' -test '* ]]; then
  echo 'backend-gate fixture: deliberate go list -test failure' >&2
  exit 43
fi

exec "${REAL_GO_COMMAND:?REAL_GO_COMMAND is required}" "$@"
