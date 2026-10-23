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
    # All 64 non-deferred PNGs must repeat byte-for-byte. The other 16
    # checkpoints stay awaiting/report-only. Only the declared account-use
    # runner difference may reach a plan, with an explicit Tick re-look gate.
    node scripts/test-prune/visual-pin.mjs verify-plan web/test-results/container-pin-plan
    echo 'The plan selects exactly 64 checkpoints; keep its explicit checkpoint list in the Tick receipt.'
    echo 'Copy tick-approval.template.json to tick-approval.json and fill the actual receipt for these exact archive bytes.'
    echo 'If reviewRequired lists the account-use checkpoint, obtain its Tick re-look and record it in reReviewedCheckpoints.'
    echo 'Then run: bash web/test-results/container-pin-plan/lead-commands.sh publish'
    echo 'After recording attachment IDs: bash web/test-results/container-pin-plan/lead-commands.sh activate ARCHIVE_ATTACHMENT_UUID MANIFEST_ATTACHMENT_UUID'
    ;;
  *)
    echo 'Usage: lead-commands.sh dispatch COORDINATOR_PUBLISHED_REF | download CI_RUN_ID' >&2
    exit 1
    ;;
esac
