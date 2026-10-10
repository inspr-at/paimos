// SPDX-License-Identifier: AGPL-3.0-only
// Run this reviewed controller from the workflow checkout, outside candidate.
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { shaPattern, browserGroups } from './ci-preflight-result.mjs';

export function browserPreflight({ sha, group, run, attempt, root, output, environment, runnerClass }, execute = spawnSync) {
  if (!shaPattern.test(sha ?? '') || !browserGroups.includes(group) || !Number.isSafeInteger(run) || run < 1 ||
      !Number.isSafeInteger(attempt) || attempt < 1 || environment !== 'github-hosted' || runnerClass !== 'hosted') throw new Error('preflight_browser_identity_invalid');
  const index = browserGroups.indexOf(group) + 1;
  const identity = execute('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8', timeout: 30000, maxBuffer: 1024 });
  let passed = false;
  if (!identity.error && identity.status === 0 && identity.stdout.trim() === sha) {
    const result = execute('node', ['scripts/test-tiers/cli.mjs', 'run', 'web', '--full', '--event', 'workflow_dispatch',
      '--shard', `${index}/${browserGroups.length}`, '--job', group], { cwd: root, stdio: 'inherit', timeout: 30 * 60 * 1000,
      env: { ...process.env, PW_RETRIES: '0', CI_LANE: 'full', AEON_TEST_TIER_MODE: 'full', AEON_TEST_TIER_LAYOUT: 'full' } });
    passed = !result.error && !result.signal && result.status === 0;
  }
  const receipt = { schema: 1, kind: 'browser', sha, run_id: run, run_attempt: attempt, runner_class: 'hosted', group, status: passed ? 'passed' : 'failed' };
  mkdirSync(output, { recursive: true });
  writeFileSync(resolve(output, `${group}.json`), JSON.stringify(receipt) + '\n');
  return passed ? 0 : 1;
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try { process.exitCode = browserPreflight({ sha: process.env.PREFLIGHT_SHA, group: process.env.PREFLIGHT_GROUP,
    run: Number(process.env.GITHUB_RUN_ID), attempt: Number(process.env.GITHUB_RUN_ATTEMPT), root: resolve('candidate'),
    output: resolve('receipts'), environment: process.env.RUNNER_ENVIRONMENT, runnerClass: process.env.PREFLIGHT_RUNNER_CLASS });
  } catch { console.error('preflight_browser_unavailable'); process.exitCode = 1; }
}
