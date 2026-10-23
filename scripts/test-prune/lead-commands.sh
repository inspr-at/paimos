#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# LEAD only. The worker never pushes, dispatches, publishes or attaches assets.
# Run from the worktree root after the coordinator publishes the reviewed ref.
set -euo pipefail

case "${1:-}" in
  prepare)
    capture_data=${2:?usage: lead-commands.sh prepare CAPTURE_ARTIFACT_DIR REVIEWED_RUNNER_DIR NEW_PLAN_ROOT}
    reviewed_data=${3:?reviewed runner artifact directory required}
    plan_root=${4:?fresh plan root required}
    if [ -e "$plan_root" ]; then
      echo 'Plan root must be fresh; never overwrite earlier proof or receipts.' >&2
      exit 1
    fi
    mkdir "$plan_root"
    node scripts/test-prune/visual-identity.mjs "$capture_data/container-run-1" "$capture_data/container-run-2" "$reviewed_data" "$plan_root/identity"
    node scripts/test-prune/visual-pin.mjs prepare "$capture_data/container-run-1" "$plan_root/pin-plan" "$plan_root/identity/identity-proof.json"
    node scripts/test-prune/visual-pin.mjs verify-plan "$plan_root/pin-plan"
    echo "Copy $plan_root/pin-plan/tick-approval.template.json to $plan_root/pin-plan/tick-approval.json and fill Tick's actual exact-archive receipt, retaining all 60 checkpoint IDs."
    echo "Then run: bash $plan_root/pin-plan/lead-commands.sh publish"
    echo "After recording attachment IDs: bash $plan_root/pin-plan/lead-commands.sh activate ARCHIVE_ATTACHMENT_UUID MANIFEST_ATTACHMENT_UUID"
    ;;
  probe)
    probe_ref=${2:?usage: lead-commands.sh probe COORDINATOR_PUBLISHED_REF CHECKPOINT_IDS}
    checkpoint_ids=${3:?comma-separated checkpoint IDs required}
    VISUAL_PROBE_CHECKPOINTS="$checkpoint_ids" node --input-type=module -e 'import { readFileSync } from "node:fs"; import { probeCheckpoints } from "./scripts/test-prune/visual-probe.mjs"; probeCheckpoints(process.env.VISUAL_PROBE_CHECKPOINTS, JSON.parse(readFileSync("scripts/test-prune/visual-manifest.json")));'
    expected_sha=$(git rev-parse HEAD)
    published_sha=$(gh api "repos/inspr-at/paimos/commits/$probe_ref" --jq .sha)
    if [ "$expected_sha" != "$published_sha" ]; then
      echo 'Published ref does not match this checkout; stop before dispatch.' >&2
      exit 1
    fi
    gh workflow run ci.yml --repo inspr-at/paimos --ref "$probe_ref" -f "visual_probe=$checkpoint_ids"
    echo 'Diagnostic probe only: five settings, six independent repeats each. Download visual-raster-probe-SHA-ATTEMPT; no pin plan is produced.'
    ;;
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
    # All 60 non-deferred PNGs must repeat byte-for-byte and match reviewed
    # runner pixels. The other 20 checkpoints stay unpinned/report-only.
    node scripts/test-prune/visual-pin.mjs verify-plan web/test-results/container-pin-plan
    echo 'The plan selects exactly 60 checkpoints; keep its explicit checkpoint list in the Tick receipt.'
    echo 'Copy tick-approval.template.json to tick-approval.json and fill the actual receipt for these exact archive bytes.'
    echo 'Then run: bash web/test-results/container-pin-plan/lead-commands.sh publish'
    echo 'After recording attachment IDs: bash web/test-results/container-pin-plan/lead-commands.sh activate ARCHIVE_ATTACHMENT_UUID MANIFEST_ATTACHMENT_UUID'
    ;;
  *)
    echo 'Usage: lead-commands.sh prepare CAPTURE_ARTIFACT_DIR REVIEWED_RUNNER_DIR NEW_PLAN_ROOT | dispatch COORDINATOR_PUBLISHED_REF | download CI_RUN_ID | probe COORDINATOR_PUBLISHED_REF CHECKPOINT_IDS' >&2
    exit 1
    ;;
esac
