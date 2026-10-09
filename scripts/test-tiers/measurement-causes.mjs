// SPDX-License-Identifier: AGPL-3.0-only
// Evidence categories, not inferred infrastructure root causes. OPS owns the
// later required aggregate and pin; this report changes no gate or exit code.
import { readFileSync, statSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
export function measurementCauses(report) {
  const causes = [];
  if (report.upstreamFailures?.length) causes.push('upstream_setup_or_planner');
  if (report.skippedJobs?.length) causes.push('required_tier_jobs_skipped');
  if (report.missingEvidence?.length) causes.push('missing_case_artifact');
  if (report.excludedEvidence?.length) causes.push('artifact_run_attempt_or_sha_mismatch');
  return causes;
}
export function measurementErrorCause(error) {
  const message = String(error?.message ?? '');
  if (message.startsWith('Invalid job timestamps:')) return 'invalid_runner_timestamps';
  if (/Invalid (?:or duplicate measurement|case count|flake ledger)|Invalid flake ledger/.test(message)) return 'invalid_case_evidence';
  if (/Reused execution must|Invalid reused source run/.test(message)) return 'reuse_evidence_conflict';
  if (/Missing Actions run identity|Job inventory exceeds/.test(message)) return 'invalid_or_incomplete_job_inventory';
  if (/^gh api /.test(message)) return 'actions_api_read_failed';
  if (error instanceof SyntaxError) return 'malformed_artifact_json';
  return 'unclassified_measurement_error';
}

// Saved Arion attempt-1 records contain failed job names, not artifact/log
// reasons. Classify what that inventory proves; do not infer a root cause.
export function measurementCofailures(records, start, end) {
  if (!Array.isArray(records) || records.length > 20000 || !Number.isFinite(Date.parse(start)) ||
      !Number.isFinite(Date.parse(end)) || start >= end) throw new Error('measurement_inventory_invalid');
  const groups = Object.fromEntries(['pull_request', 'merge_group'].map(event => [event, {
    tier_red: 0, setup_or_planner_cofailure: 0, test_execution_cofailure: 0, no_setup_or_tier_execution_red_in_inventory: 0,
  }]));
  const excluded = new Set(['cancelled', 'neutral', 'action_required', 'stale', 'skipped']);
  for (const row of records) {
    if (!Object.hasOwn(groups, row.event) || row.created_at < start || row.created_at >= end ||
        !row.a1_completed || row.a1_completed >= end || excluded.has(row.a1_conclusion)) continue;
    if (!Array.isArray(row.failed_jobs) || row.failed_jobs.length > 1000 || row.failed_jobs.some(name => typeof name !== 'string' || name.length > 1000)) throw new Error('measurement_job_inventory_invalid');
    if (!row.failed_jobs.some(name => /^tier-measurements(?:$| \(| \/)/.test(name))) continue;
    const families = new Set(row.failed_jobs.map(name => name.split(' (')[0]));
    const group = groups[row.event]; group.tier_red++;
    if (['web-setup', 'tier-plan'].some(name => families.has(name))) group.setup_or_planner_cofailure++;
    else if (['go-test', 'go-timing', 'web-unit', 'web-shard'].some(name => families.has(name))) group.test_execution_cofailure++;
    else group.no_setup_or_tier_execution_red_in_inventory++;
  }
  return { schema: 1, window: [start, end], groups, scope: 'attempt-1 failed-job cofailures; causal artifact/log reasons remain unclassified' };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [file, window = 'W2'] = process.argv.slice(2);
    if (statSync(file).size > 8 * 1024 * 1024) throw new Error('measurement_inventory_oversized');
    const inventory = JSON.parse(readFileSync(file, 'utf8'));
    console.log(JSON.stringify(measurementCofailures(inventory.runs, ...inventory.windows[window])));
  } catch { console.error('measurement_cause_inventory_unavailable'); process.exitCode = 1; }
}
