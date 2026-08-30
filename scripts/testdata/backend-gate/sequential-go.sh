#!/usr/bin/env bash
set -euo pipefail

state=${FAKE_GO_STATE:?FAKE_GO_STATE is required}
if [[ " $* " == *' test '* && " $* " == *' -list '* ]]; then
  printf '%s\n' \
    TestConcurrentOne \
    TestConcurrentTwo \
    TestConcurrentThree \
    TestConcurrentFour \
    TestConcurrentFive \
    'ok  fixture  0.001s'
  exit 0
fi
if [[ " $* " == *' test '* && " $* " == *' -race '* ]]; then
  if ! mkdir "$state/active" 2>/dev/null; then
    echo 'backend-gate fixture: overlapping handler race process' >&2
    touch "$state/overlap"
    exit 45
  fi
  sleep 0.05
  rmdir "$state/active"
  printf 'run\n' >> "$state/runs"
  exit 0
fi

echo "backend-gate sequential fixture received unexpected command: $*" >&2
exit 46
