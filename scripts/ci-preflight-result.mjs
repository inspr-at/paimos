// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync, statSync } from 'node:fs';

export const localChecks = ['static', 'go-strict', 'go-packages', 'web-unit', 'web-strict'];
// WP1.4 may narrow only with a complete, base-tree-bound impact map. Until
// that planner is available, preflight always uses CI's full 12-shard layout.
export const browserGroups = Array.from({ length: 12 }, (_, i) => `browser-${i + 1}`);
export const shaPattern = /^[a-f0-9]{40}$/;
export const runnerLabelPattern = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/;
export const maxResultBytes = 32 * 1024;
export function readResult(path) {
  if (statSync(path).size > maxResultBytes) throw new Error('preflight_result_oversized');
  return JSON.parse(readFileSync(path, 'utf8'));
}
function exact(value, fields) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).sort().join(',') === fields.sort().join(',');
}
function checks(rows, names) {
  return Array.isArray(rows) && rows.length === names.length && rows.every((row, i) =>
    exact(row, ['id', 'status']) && row.id === names[i] && ['passed', 'failed', 'not_run'].includes(row.status));
}
export function validLocal(result, sha) {
  return exact(result, ['schema', 'kind', 'sha', 'base_sha', 'runner_class', 'checks', 'status']) &&
    result.schema === 1 && result.kind === 'local' && shaPattern.test(sha ?? '') && result.sha === sha &&
    shaPattern.test(result.base_sha ?? '') && typeof result.runner_class === 'string' && runnerLabelPattern.test(result.runner_class) && checks(result.checks, localChecks) &&
    result.status === (result.checks.every(row => row.status === 'passed') ? 'passed' : 'failed');
}
export function validBrowser(result, sha, run, attempt, group) {
  return exact(result, ['schema', 'kind', 'sha', 'run_id', 'run_attempt', 'runner_class', 'group', 'status']) &&
    result.schema === 1 && result.kind === 'browser' && result.sha === sha && shaPattern.test(sha ?? '') &&
    result.run_id === run && result.run_attempt === attempt && result.runner_class === 'hosted' &&
    browserGroups.includes(group) && result.group === group && ['passed', 'failed'].includes(result.status);
}
export function combine(local, browsers, { sha, run, attempt }) {
  if (!validLocal(local, sha) || !Number.isSafeInteger(run) || run < 1 || !Number.isSafeInteger(attempt) || attempt < 1 ||
      !Array.isArray(browsers) || browsers.length !== browserGroups.length ||
      browsers.some((result, i) => !validBrowser(result, sha, run, attempt, browserGroups[i]))) {
    throw new Error('preflight_evidence_invalid');
  }
  return { schema: 1, sha, run_id: run, run_attempt: attempt, local,
    browsers, status: local.status === 'passed' && browsers.every(row => row.status === 'passed') ? 'passed' : 'failed' };
}
