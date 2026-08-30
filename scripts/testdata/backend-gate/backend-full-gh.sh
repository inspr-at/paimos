#!/usr/bin/env bash
set -euo pipefail

[[ "$*" == *'run list'* && "$*" == *'--workflow backend-full.yml'* &&
  "$*" == *"--commit ${FAKE_HEAD_SHA:?}"* ]] || exit 47

case "${FAKE_BACKEND_FULL_MODE:?}" in
  success)
    printf '[{"databaseId":1,"headSha":"%s","status":"completed","conclusion":"success","url":"https://example.test/1"}]\n' "$FAKE_HEAD_SHA"
    ;;
  failed)
    printf '[{"databaseId":2,"headSha":"%s","status":"completed","conclusion":"failure","url":"https://example.test/2"}]\n' "$FAKE_HEAD_SHA"
    ;;
  wrong-head)
    printf '[{"databaseId":3,"headSha":"0000000000000000000000000000000000000000","status":"completed","conclusion":"success","url":"https://example.test/3"},{"databaseId":4,"headSha":"%s","status":"completed","conclusion":"failure","url":"https://example.test/4"}]\n' "$FAKE_HEAD_SHA"
    ;;
  *) exit 48 ;;
esac
