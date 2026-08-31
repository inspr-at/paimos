#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 1 && "$1" =~ ^[0-9a-f]{40}$ ]] || {
  echo "usage: $0 <exact-40-character-head-sha>" >&2
  exit 2
}

HEAD_SHA=$1
GH_COMMAND=${GH_COMMAND:-gh}
# The serial job has a 40-minute budget and the measured race job has a
# 70-minute budget; they run in parallel.
# Allow ten additional minutes for queueing, setup, and the fail-closed
# aggregator; exact hosted measurements must remain comfortably inside this.
BACKEND_FULL_TIMEOUT_SECONDS=${BACKEND_FULL_TIMEOUT_SECONDS:-4800}
BACKEND_FULL_POLL_SECONDS=${BACKEND_FULL_POLL_SECONDS:-15}
REPOSITORY=${GITHUB_REPOSITORY:-inspr-at/paimos}
start=$(date +%s)

while true; do
  runs=$("$GH_COMMAND" run list \
    --repo "$REPOSITORY" \
    --workflow backend-full.yml \
    --commit "$HEAD_SHA" \
    --limit 20 \
    --json databaseId,headSha,status,conclusion,url) || {
      echo 'wait-backend-full: could not query exhaustive backend evidence' >&2
      exit 1
    }
  jq -e 'type == "array"' >/dev/null <<<"$runs" || {
    echo 'wait-backend-full: GitHub returned malformed run evidence' >&2
    exit 1
  }

  successful_run_ids=$(jq -r --arg head "$HEAD_SHA" '
    .[] |
    select(.headSha == $head and .status == "completed" and .conclusion == "success") |
    .databaseId
  ' <<<"$runs")
  while IFS= read -r run_id; do
    [[ -n "$run_id" ]] || continue
    if ! jobs=$("$GH_COMMAND" run view "$run_id" --repo "$REPOSITORY" --json jobs); then
      echo "wait-backend-full: could not inspect exhaustive backend run $run_id" >&2
      exit 1
    fi
    jq -e '.jobs | type == "array"' >/dev/null <<<"$jobs" || {
      echo "wait-backend-full: GitHub returned malformed job evidence for run $run_id" >&2
      exit 1
    }
    if jq -e '
      any(.jobs[]; .name == "backend-full" and .status == "completed" and .conclusion == "success")
    ' >/dev/null <<<"$jobs"; then
      echo "Exhaustive backend assurance is green for exact head $HEAD_SHA."
      exit 0
    fi
  done <<<"$successful_run_ids"

  exact_count=$(jq --arg head "$HEAD_SHA" '[.[] | select(.headSha == $head)] | length' <<<"$runs")
  active_count=$(jq --arg head "$HEAD_SHA" \
    '[.[] | select(.headSha == $head and .status != "completed")] | length' <<<"$runs")
  if (( exact_count > 0 && active_count == 0 )); then
    echo "wait-backend-full: exhaustive backend assurance completed without success for exact head $HEAD_SHA" >&2
    exit 1
  fi

  now=$(date +%s)
  if (( now - start >= BACKEND_FULL_TIMEOUT_SECONDS )); then
    echo "wait-backend-full: timed out waiting for exhaustive backend assurance on exact head $HEAD_SHA" >&2
    exit 1
  fi
  echo "Waiting for exhaustive backend assurance on exact head $HEAD_SHA..."
  sleep "$BACKEND_FULL_POLL_SECONDS"
done
