#!/usr/bin/env bash
set -euo pipefail

CHECK_ONLY=0
DISPATCH=0
case "${1:-}" in
  --check) CHECK_ONLY=1; shift ;;
  --dispatch) DISPATCH=1; shift ;;
esac
# Hosted jobs only inspect existing evidence; never hold a runner while waiting.
if [[ "${GITHUB_ACTIONS:-false}" == true && "$CHECK_ONLY" -ne 1 ]]; then
  echo 'wait-backend-full: hosted callers must use --check' >&2
  exit 2
fi

[[ $# -eq 1 && "$1" =~ ^[0-9a-f]{40}$ ]] || {
  echo "usage: $0 [--check|--dispatch] <exact-40-character-head-sha>" >&2
  exit 2
}

HEAD_SHA=$1
GH_COMMAND=${GH_COMMAND:-gh}
# The serial and measured race jobs each have a 90-minute budget; they run in
# parallel.
# Allow ten additional minutes for queueing, setup, and the fail-closed
# aggregator; exact hosted measurements must remain comfortably inside this.
BACKEND_FULL_TIMEOUT_SECONDS=${BACKEND_FULL_TIMEOUT_SECONDS:-6000}
BACKEND_FULL_POLL_SECONDS=${BACKEND_FULL_POLL_SECONDS:-15}
REPOSITORY=${GITHUB_REPOSITORY:-inspr-at/paimos}
start=$(date +%s)
dispatched=0

while true; do
  runs=$("$GH_COMMAND" run list \
    --repo "$REPOSITORY" \
    --workflow backend-full.yml \
    --commit "$HEAD_SHA" \
    --limit 20 \
    --json databaseId,headSha,status,conclusion,url,event,headBranch,displayTitle) || {
      echo 'wait-backend-full: could not query exhaustive backend evidence' >&2
      exit 1
    }
  jq -e 'type == "array"' >/dev/null <<<"$runs" || {
    echo 'wait-backend-full: GitHub returned malformed run evidence' >&2
    exit 1
  }

  # Dispatch metadata headSha describes the workflow ref, which may be newer
  # than its pinned checkout. The trusted main workflow binds target_sha into
  # displayTitle and checks out that exact commit in every execution job.
  dispatched_runs=$("$GH_COMMAND" run list --repo "$REPOSITORY" \
    --workflow backend-full.yml --event workflow_dispatch --branch main \
    --limit 100 --json databaseId,headSha,status,conclusion,url,event,headBranch,displayTitle) || {
      echo 'wait-backend-full: could not query dispatched backend evidence' >&2
      exit 1
    }
  jq -e 'type == "array"' >/dev/null <<<"$dispatched_runs" || {
    echo 'wait-backend-full: GitHub returned malformed dispatch evidence' >&2
    exit 1
  }
  runs=$(jq -sc --arg head "$HEAD_SHA" '
    add | unique_by(.databaseId) | map(select(
      (.event == "workflow_dispatch" and .headBranch == "main" and
       .displayTitle == ("backend-full " + $head)) or
      ((.event == "push" or .event == "schedule") and
       .headBranch == "main" and .headSha == $head) or
      (.event == "pull_request" and .headSha == $head and
       .displayTitle == ("backend-full " + $head))
    ))
  ' <<<"$runs
$dispatched_runs")
  successful_run_ids=$(jq -r '
    .[] | select(.status == "completed" and .conclusion == "success") |
    .databaseId | select(type == "number" and . > 0 and floor == .)
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
      [.jobs[] | select(.status == "completed" and .conclusion == "success") | .name] as $green |
      ["backend-full-authorize", "backend-full-serial", "backend-full",
       "backend-full-race (core)", "backend-full-race (handlers)",
       "backend-full-race (runtime)"] | all(.[]; . as $name | $green | index($name) != null)
    ' >/dev/null <<<"$jobs"; then
      echo "Backend assurance is green for exact head $HEAD_SHA (full serial/platform and all race groups executed)."
      exit 0
    fi
  done <<<"$successful_run_ids"

  if (( CHECK_ONLY )); then
    echo "wait-backend-full: no completed full execution for exact head $HEAD_SHA" >&2
    exit 1
  fi
  exact_count=$(jq 'length' <<<"$runs")
  active_count=$(jq '[.[] | select(.status != "completed")] | length' <<<"$runs")
  if (( exact_count > 0 && active_count == 0 )); then
    echo "wait-backend-full: exhaustive backend assurance completed without success for exact head $HEAD_SHA" >&2
    exit 1
  fi
  if (( DISPATCH && ! dispatched && exact_count == 0 )); then
    "$GH_COMMAND" workflow run backend-full.yml --repo "$REPOSITORY" \
      --ref main -f "target_sha=$HEAD_SHA" || {
        echo 'wait-backend-full: could not dispatch exact-code backend execution' >&2
        exit 1
      }
    dispatched=1
    echo "Dispatched exhaustive backend assurance for exact head $HEAD_SHA."
  fi

  now=$(date +%s)
  if (( now - start >= BACKEND_FULL_TIMEOUT_SECONDS )); then
    echo "wait-backend-full: timed out waiting for exhaustive backend assurance on exact head $HEAD_SHA" >&2
    exit 1
  fi
  echo "Waiting for exhaustive backend assurance on exact head $HEAD_SHA..."
  sleep "$BACKEND_FULL_POLL_SECONDS"
done
