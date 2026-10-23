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
    node --input-type=module -e 'import { resolve } from "node:path"; if (!resolve(process.argv[1]).startsWith(resolve("web/test-results") + "/")) throw new Error("Plan root must stay under web/test-results");' "$plan_root"
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
    echo 'Diagnostic probe only: six parallel runner machines, baseline vs disabled Skia runtime opts plus determinism flags, six repeats each. Read visual-raster-probe-matrix-SHA-ATTEMPT/matrix-report.json and .md; per-runner PNGs, CPU flags and logs remain separate artifacts. No pin plan is produced.'
    ;;
  dispatch-candidate)
    capture_ref=${2:?usage: lead-commands.sh dispatch-candidate COORDINATOR_PUBLISHED_REF PREPARED_SETTING}
    setting=${3:?prepared renderer setting required}
    SETTING="$setting" node --input-type=module -e 'import { readFileSync } from "node:fs"; import { preparedRenderer } from "./scripts/test-prune/visual-renderer.mjs"; const renderer = preparedRenderer(JSON.parse(readFileSync("scripts/test-prune/visual-manifest.json")), process.env.SETTING); if (!renderer.rasterSetting) throw new Error("Choose an explicit prepared renderer"); if (renderer.machine) throw new Error("agm2 scheduling is prepared only; OPS and coordinator must review the runner boundary before enabling that workflow input");'
    expected_sha=$(git rev-parse HEAD)
    published_sha=$(gh api "repos/inspr-at/paimos/commits/$capture_ref" --jq .sha)
    if [ "$expected_sha" != "$published_sha" ]; then
      echo 'Published ref does not match this checkout; stop before dispatch.' >&2
      exit 1
    fi
    gh workflow run ci.yml --repo inspr-at/paimos --ref "$capture_ref" -f visual_capture=true -f "visual_renderer=$setting"
    echo 'Two runner jobs each capture all 72 candidates twice. Download visual-linux-candidate-SHA-ATTEMPT-runner-1 and -runner-2 into separate fresh directories. Prove all 60 selected PNGs across machines and review diffs vs set 3; no current reference is changed.'
    ;;
  candidate-proof)
    setting=${2:?usage: lead-commands.sh candidate-proof PREPARED_SETTING FIRST_CAPTURE SECOND_CAPTURE VERIFIED_SET3_DIR NEW_REVIEW_DIR}
    first_capture=${3:?first runner container-run-1 required}
    second_capture=${4:?second runner container-run-1 required}
    set3=${5:?verified set-3 assets directory required}
    output=${6:?fresh review directory required}
    node scripts/test-prune/visual-identity.mjs candidate "$setting" "$first_capture" "$second_capture" "$set3" "$output"
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
    echo 'Usage: lead-commands.sh prepare CAPTURE_ARTIFACT_DIR REVIEWED_RUNNER_DIR NEW_PLAN_ROOT | dispatch COORDINATOR_PUBLISHED_REF | download CI_RUN_ID | probe COORDINATOR_PUBLISHED_REF CHECKPOINT_IDS | dispatch-candidate COORDINATOR_PUBLISHED_REF PREPARED_SETTING | candidate-proof PREPARED_SETTING FIRST_CAPTURE SECOND_CAPTURE VERIFIED_SET3_DIR NEW_REVIEW_DIR' >&2
    exit 1
    ;;
esac
