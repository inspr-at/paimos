#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# LEAD only. The worker never pushes, dispatches, publishes or attaches assets.
# Run from the worktree root after the coordinator publishes the reviewed ref.
set -euo pipefail

case "${1:-}" in
  dispatch)
    capture_ref=${2:?usage: lead-commands.sh dispatch COORDINATOR_PUBLISHED_REF}
    expected_sha=$(git rev-parse HEAD)
    published_sha=$(gh api "repos/inspr-at/paimos/commits/$capture_ref" --jq .sha)
    if [ "$expected_sha" != "$published_sha" ]; then
      echo 'Published ref does not match this checkout; stop before dispatch.' >&2
      exit 1
    fi
    gh workflow run ci.yml --repo inspr-at/paimos --ref "$capture_ref" -f visual_capture=true
    echo 'Record the dispatched run ID. Both captures render this same SHA.'
    ;;
  download)
    capture_run=${2:?usage: lead-commands.sh download CI_RUN_ID}
    [[ "$capture_run" =~ ^[1-9][0-9]*$ ]] || exit 1
    capture_sha=$(gh run view "$capture_run" --repo inspr-at/paimos --json headSha --jq .headSha)
    capture_attempt=$(gh api "repos/inspr-at/paimos/actions/runs/$capture_run" --jq .run_attempt)
    # Download into the original relative layout used by the generated plan.
    # Refuse an existing plan so an earlier artifact cannot impersonate this run.
    if [ -e web/test-results/container-pin-plan ] || [ -e web/test-results/container-run-1 ]; then
      echo 'Use a clean artifact destination/worktree; never overwrite an earlier pin plan.' >&2
      exit 1
    fi
    gh run download "$capture_run" --repo inspr-at/paimos \
      --name "visual-linux-candidate-${capture_sha}-${capture_attempt}" --dir web/test-results
    cat web/test-results/container-identity/identity-proof.md
    # A difference leaves no usable plan and fails this verification. LEAD
    # resolves instability first and decides any review of changed regions.
    node scripts/test-prune/visual-pin.mjs verify-plan web/test-results/container-pin-plan
    echo 'After Tick receipt for the exact container archive: fill tick-approval.json, then run container-pin-plan/lead-commands.sh.'
    ;;
  *)
    echo 'Usage: lead-commands.sh dispatch COORDINATOR_PUBLISHED_REF | download CI_RUN_ID' >&2
    exit 1
    ;;
esac
