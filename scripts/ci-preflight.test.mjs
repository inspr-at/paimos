// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { preflight, boundCheckout, goPackages, testEnvironment } from './ci-preflight-local.mjs';
import { browserPreflight } from './ci-preflight-browser.mjs';
import { combine, browserGroups, validLocal } from './ci-preflight-result.mjs';
import { preflightMetrics } from './ci-preflight-report.mjs';
import { measurementCauses, measurementErrorCause, measurementCofailures } from './test-tiers/measurement-causes.mjs';

const sha = 'b'.repeat(40), base = 'a'.repeat(40);
const greenLocal = () => ({ schema: 1, kind: 'local', sha, base_sha: base, runner_class: 'build-mac',
  checks: ['static', 'go-strict', 'go-packages', 'web-unit', 'web-strict'].map(id => ({ id, status: 'passed' })), status: 'passed' });

test('preflight binds each command to the SHA and never turns failure, skip or tree changes green', async () => {
  // Risk: stale or partially executed local results authorize a new PR head.
  let binds = 0;
  const commands = [];
  const options = { sha, base, root: '/unused', runner: 'build-mac.local', expectedRunner: 'build-mac' };
  const dependencies = { bind: () => { binds++; }, verifyBase: () => {}, diff: () => ['internal/delivery/shipping.go'],
    execute: (bin, args) => { commands.push([bin, args]); return true; } };
  const green = await preflight(options, dependencies);
  assert.equal(validLocal(green, sha), true);
  assert.equal(validLocal(green, base), false);
  assert.equal(binds, 11);
  assert.deepEqual(commands[2], ['go', ['test', '-count=1', '-timeout', '20m', './internal/delivery']]);
  assert(commands[1][1].includes('--strict'));
  assert(commands[4][1].includes('--strict'));
  const red = await preflight(options, { ...dependencies, execute: () => false });
  assert.equal(red.status, 'failed');
  assert(red.checks.every(row => row.status === 'failed'));
  let count = 0;
  const changed = await preflight(options, { ...dependencies, bind: () => { if (++count === 3) throw new Error('moved'); } });
  assert.equal(changed.status, 'failed');
  assert.equal(changed.checks[0].status, 'failed');
  assert(changed.checks.slice(1).every(row => row.status === 'not_run'));
  for (const runner of ['build-mac', 'build-linux']) {
    const receipt = await preflight({ ...options, runner, expectedRunner: runner }, dependencies);
    assert.equal(receipt.runner_class, runner);
    assert.equal(validLocal(receipt, sha), true);
  }
  for (const expectedRunner of ['', 'build mac', 'r'.repeat(129)]) {
    await assert.rejects(preflight({ ...options, expectedRunner }, dependencies), /requires_configured_runner/);
    assert.equal(validLocal({ ...greenLocal(), runner_class: expectedRunner }, sha), false);
  }
  await assert.rejects(preflight({ ...options, runner: 'daily-driver' }, dependencies), /requires_configured_runner/);
  assert.deepEqual(goPackages(['go.mod']), ['./...']);
  assert.deepEqual(goPackages(['web/src/App.vue']), []);
  assert.throws(() => testEnvironment({}), /test_database_required/);
  assert.throws(() => testEnvironment({ AEON_TEST_DATABASE_URL: 'postgres://test@remote.invalid/production' }), /isolated_test_database_required/);
  const isolated = 'postgres://aeon:aeon@127.0.0.1:55433/aeon_preflight_bbbbbbbbbb';
  const environment = testEnvironment({ AEON_TEST_DATABASE_URL: isolated, UNRELATED_AUTH: 'fixture' });
  assert.equal(environment.AEON_TEST_DATABASE_URL, isolated);
  assert.equal(environment.UNRELATED_AUTH, undefined);
  const malformed = greenLocal(); malformed.checks.pop();
  assert.equal(validLocal(malformed, sha), false);
  const extra = greenLocal(); extra.payload = 'raw private data';
  assert.equal(validLocal(extra, sha), false);
});

test('preflight red rate stays separate from CI and measurement causes never invent root causes', () => {
  const run = { id: 42, run_attempt: 1, path: '.github/workflows/ci-preflight.yml', event: 'workflow_dispatch',
    head_branch: 'main', display_title: `preflight:${sha}`, head_sha: base, status: 'completed', conclusion: 'failure' };
  const receipt = combine(greenLocal(), browserGroups.map(group => ({ schema: 1, kind: 'browser', sha, group,
    run_id: 43, run_attempt: 1, runner_class: 'hosted', status: 'passed' })), { sha, run: 43, attempt: 1 });
  const runs = [run, { ...run, id: 43, conclusion: 'success' }, { path: '.github/workflows/ci.yml', conclusion: 'success' }];
  assert.deepEqual(preflightMetrics(runs, [receipt]),
    { metric: 'preflight_red_rate', unit: 'percent', attempts: 2, red: 1, pending: 0, missing_receipt: 0, invalid_receipt: 0, value: 50,
      scope: 'completed preflight attempts; green requires the exact SHA/attempt receipt; CI first attempts remain separate' });
  // All work jobs can skip after a non-hosted route while Actions labels the
  // workflow successful. No artifact must remain red, including in this tile.
  assert.equal(preflightMetrics(runs).red, 2);
  assert.equal(preflightMetrics(runs).missing_receipt, 1);
  assert.equal(preflightMetrics(runs, [{ ...receipt, sha: base }]).invalid_receipt, 1);
  assert.equal(preflightMetrics([]).value, null);
  assert.throws(() => preflightMetrics([run, run]), /duplicate_attempt/);
  assert.deepEqual(measurementCauses({ upstreamFailures: ['setup'], skippedJobs: ['browser'], missingEvidence: ['browser'], excludedEvidence: ['stale'] }),
    ['upstream_setup_or_planner', 'required_tier_jobs_skipped', 'missing_case_artifact', 'artifact_run_attempt_or_sha_mismatch']);
  assert.equal(measurementErrorCause(new Error('Invalid job timestamps: web')), 'invalid_runner_timestamps');
  assert.equal(measurementErrorCause(new Error('unrecognized')), 'unclassified_measurement_error');
  const record = { event: 'pull_request', created_at: '2026-10-05T01:00:00Z', a1_completed: '2026-10-05T02:00:00Z',
    a1_conclusion: 'failure', failed_jobs: ['tier-measurements'] };
  const causes = measurementCofailures([record, { ...record, failed_jobs: ['tier-measurements', 'web-setup'] },
    { ...record, failed_jobs: ['tier-measurements', 'web-shard (1)'] }, { ...record, a1_conclusion: 'cancelled' }],
    '2026-10-05T00:00:00Z', '2026-10-09T04:00:00Z');
  assert.deepEqual(causes.groups.pull_request, { tier_red: 3, setup_or_planner_cofailure: 1,
    test_execution_cofailure: 1, no_setup_or_tier_execution_red_in_inventory: 1 });
});

test('bound checkout rejects a real dirty tree and a different commit', () => {
  const root = mkdtempSync(join(tmpdir(), 'preflight-binding-'));
  const git = (...args) => {
    const result = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr); return result.stdout.trim();
  };
  git('init', '-q');
  writeFileSync(join(root, 'fixture'), 'clean'); git('add', 'fixture');
  git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture');
  const head = git('rev-parse', 'HEAD'); boundCheckout(root, head);
  assert.throws(() => boundCheckout(root, sha), /sha_or_tree_changed/);
  writeFileSync(join(root, 'fixture'), 'dirty');
  assert.throws(() => boundCheckout(root, head), /sha_or_tree_changed/);
});

test('browser receipts require hosted execution and all full-layout groups for the current attempt', () => {
  const output = mkdtempSync(join(tmpdir(), 'preflight-browser-'));
  const options = { sha, group: 'browser-1', run: 42, attempt: 2, root: '/unused', output,
    environment: 'github-hosted', runnerClass: 'hosted' };
  let commands = 0;
  assert.equal(browserPreflight(options, () => ++commands === 1 ? { status: 0, stdout: sha + '\n' } : { status: 1 }), 1);
  assert.equal(JSON.parse(readFileSync(join(output, 'browser-1.json'))).status, 'failed');
  assert.throws(() => browserPreflight({ ...options, environment: 'self-hosted' }), /identity_invalid/);
  const rows = browserGroups.map(group => ({ schema: 1, kind: 'browser', sha, group, run_id: 42, run_attempt: 2, runner_class: 'hosted', status: 'passed' }));
  assert.equal(combine(greenLocal(), rows, { sha, run: 42, attempt: 2 }).status, 'passed');
  for (const bad of [rows.slice(1), [rows[0], ...rows.slice(0, 11)], rows.map(row => ({ ...row, sha: base })), rows.map(row => ({ ...row, run_attempt: 1 }))]) {
    assert.throws(() => combine(greenLocal(), bad, { sha, run: 42, attempt: 2 }), /evidence_invalid/);
  }
});

test('preflight workflow uses the router, hosted guard, read-only permissions and isolated exact-SHA evidence', () => {
  const source = readFileSync(new URL('../.github/workflows/ci-preflight.yml', import.meta.url), 'utf8');
  assert.match(source, /workflow_dispatch:/);
  assert.match(source, /uses: \.\/\.github\/workflows\/test-runner-route\.yml/);
  assert.equal((source.match(/runs-on: \$\{\{ fromJSON\(needs.preflight-route.outputs.runs_on\) \}\}/g) ?? []).length, 3);
  assert(!source.includes('ubuntu-latest'));
  assert(!source.includes('secrets.'));
  assert(!/^\s+[a-z-]+: (?:write|write-all)\s*$/m.test(source));
  assert.match(source, /runner_class == 'hosted'/);
  assert.match(source, /outputs.run_attempt == github.run_attempt/);
  assert.match(source, /name: preflight-\$\{\{ matrix.group \}\}/);
  assert.match(source, /group: preflight-\$\{\{ github.repository \}\}-\$\{\{ inputs.sha \}\}/);
  assert.match(source, /ref: \$\{\{ inputs.sha \}\}/);
  assert.match(source, /ref: \$\{\{ github.sha \}\}/);
  assert.match(source, /path: candidate/);
  assert.match(source, /path: result\/preflight.json/);
  assert(source.includes('browser-12'));
  assert.equal((source.match(/run: npm ci /g) ?? []).length, 1);
  assert.match(source, /run: npm run build/);
  assert(!source.includes('continue-on-error'));
});
