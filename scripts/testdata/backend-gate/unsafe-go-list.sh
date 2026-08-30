#!/usr/bin/env bash
set -euo pipefail

if [[ " $* " == *' test '* && " $* " == *' -list '* ]]; then
  printf '%s\n' 'TestConcurrentSafe' 'TestConcurrentÜnsafe' 'ok  fixture  0.001s'
  exit 0
fi

echo "backend-gate unsafe-list fixture received unexpected command: $*" >&2
exit 44
