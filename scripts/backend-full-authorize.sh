#!/usr/bin/env bash
set -euo pipefail

event=${GITHUB_EVENT_NAME:?GITHUB_EVENT_NAME is required}
label=${BACKEND_FULL_LABEL:-}

case "$event" in
  schedule)
    [[ "${GITHUB_REF:-}" == 'refs/heads/main' ]] || {
      echo 'backend-full-authorize: schedule must execute main' >&2
      exit 1
    }
    ;;
  workflow_dispatch)
    ;;
  pull_request)
    [[ "$label" == 'backend-full-evidence' ]] || {
      echo "backend-full-authorize: refusing PR label [$label]" >&2
      exit 1
    }
    ;;
  *)
    echo "backend-full-authorize: refusing event [$event]" >&2
    exit 1
    ;;
esac

echo "backend-full-authorize: accepted $event${label:+ / $label}"
