// SPDX-License-Identifier: AGPL-3.0-only
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { readFileSync, statSync } from 'node:fs';
import { shaPattern } from './ci-preflight-result.mjs';

// Completed preflight workflow attempts only. A failed setup without a receipt
// remains red. This series cannot enter first_attempt_green (CI PR runs only).
export function preflightMetrics(runs) {
  if (!Array.isArray(runs) || runs.length > 10000) throw new Error('preflight_run_inventory_invalid');
  const seen = new Set();
  let attempts = 0, red = 0, pending = 0;
  for (const run of runs) {
    if (run.path !== '.github/workflows/ci-preflight.yml') continue;
    if (run.event !== 'workflow_dispatch' || run.head_branch !== 'main' || !/^preflight:[a-f0-9]{40}$/.test(run.display_title ?? '') ||
        !Number.isSafeInteger(run.id) || run.id < 1 || !Number.isSafeInteger(run.run_attempt) || run.run_attempt < 1 || !shaPattern.test(run.head_sha ?? '')) throw new Error('preflight_run_identity_invalid');
    const key = `${run.id}/${run.run_attempt}`;
    if (seen.has(key)) throw new Error('preflight_duplicate_attempt');
    seen.add(key);
    if (run.status !== 'completed') { pending++; continue; }
    if (!['success','failure','cancelled','timed_out','startup_failure','action_required','neutral','skipped','stale'].includes(run.conclusion)) throw new Error('preflight_conclusion_invalid');
    attempts++; if (run.conclusion !== 'success') red++;
  }
  return { metric: 'preflight_red_rate', unit: 'percent', attempts, red, pending,
    value: attempts ? 100 * red / attempts : null, scope: 'completed preflight workflow attempts; CI first attempts remain separate' };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const file = resolve(process.argv[2]);
    if (statSync(file).size > 4 * 1024 * 1024) throw new Error('preflight_inventory_oversized');
    const inventory = JSON.parse(readFileSync(file, 'utf8'));
    if (!Array.isArray(inventory.workflow_runs) || inventory.total_count !== inventory.workflow_runs.length) throw new Error('preflight_inventory_incomplete');
    console.log(JSON.stringify(preflightMetrics(inventory.workflow_runs)));
  }
  catch { console.error('preflight_metrics_unavailable'); process.exitCode = 1; }
}
