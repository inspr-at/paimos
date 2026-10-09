// SPDX-License-Identifier: AGPL-3.0-only
// Evidence categories, not inferred infrastructure root causes. OPS owns the
// later required aggregate and pin; this report changes no gate or exit code.
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
