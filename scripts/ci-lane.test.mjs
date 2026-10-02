// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { currentLane, planLane, playwrightArgs } from './ci-lane.mjs';

const base = 'a'.repeat(40), head = 'b'.repeat(40);
const event = { action: 'checks_requested', merge_group: { base_ref: 'refs/heads/main', base_sha: base, head_sha: head } };
const command = ['npx', 'playwright', 'test', '-c', 'playwright.ui.config.ts', '--workers=2'];
test('PR/main/manual always use full coverage; queue is opt-in and bound to the immutable base', () => {
  for (const name of ['push', 'pull_request', 'workflow_dispatch']) assert.equal(planLane(name, event, 'on').lane, 'full');
  assert.equal(planLane('merge_group', event, 'off').lane, 'full');
  assert.equal(planLane('merge_group', event, '').lane, 'full');
  assert.deepEqual(planLane('merge_group', event, 'on'), { lane: 'impacted', base, head, reason: 'queue fallback after tree reuse' });
  for (const bad of [{}, { ...event, action: 'destroyed' }, { ...event, merge_group: { ...event.merge_group, base_sha: 'main' } },
    { ...event, merge_group: { ...event.merge_group, base_ref: 'refs/heads/other' } }]) assert.throws(() => planLane('merge_group', bad, 'on'));
});
test('only spec-only queue changes get only-changed; shared inputs widen to the full suite', () => {
  const plan = planLane('merge_group', event, 'on');
  assert.deepEqual(playwrightArgs(command, plan, ['web/tests/theme.spec.ts']).args,
    [...command, `--only-changed=${base}`, '--pass-with-no-tests']);
  for (const files of [[], ['web/src/App.vue'], ['web/tests/helpers/stable.ts'], ['api/openapi.yaml'], ['internal/auth/keys.go'],
    ['web/tests/theme.spec.ts', '.github/workflows/ci.yml'], ['deleted input']]) assert.deepEqual(playwrightArgs(command, plan, files).args, command);
  assert.deepEqual(playwrightArgs(command, { lane: 'full' }, ['web/tests/theme.spec.ts']).args, command);
  assert.throws(() => playwrightArgs([...command, '--only-changed=main'], plan, []));
});
test('npm safety wrapper receives the same immutable selection without losing its arguments', () => {
  const plan = planLane('merge_group', event, 'on');
  assert.deepEqual(playwrightArgs(['npm', 'test', '--', 'tests/theme.spec.ts'], plan, ['web/tests/theme.spec.ts']).args,
    ['npm', 'test', '--', 'tests/theme.spec.ts', `--only-changed=${base}`, '--pass-with-no-tests']);
  assert.deepEqual(playwrightArgs(['npm', 'run', 'e2e'], plan, ['web/tests/theme.spec.ts']).args,
    ['npm', 'run', 'e2e', '--', `--only-changed=${base}`, '--pass-with-no-tests']);
});
test('actual launcher propagates browser failure and records per-tree diagnostic metrics', () => {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-ci-lane-'));
  execFileSync('git', ['init', '-q', dir]);
  writeFileSync(join(dir, 'file'), 'fixture');
  execFileSync('git', ['-C', dir, 'add', 'file']);
  execFileSync('git', ['-C', dir, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture']);
  writeFileSync(join(dir, 'npx'), '#!/bin/sh\nexit 19\n', { mode: 0o700 });
  const script = resolve('scripts/ci-lane.mjs');
  const env = { ...process.env, PATH: `${dir}:${process.env.PATH}`, GITHUB_EVENT_NAME: 'push', CI_IMPACTED_TESTS: 'on',
    GITHUB_STEP_SUMMARY: join(dir, 'summary'), GITHUB_OUTPUT: join(dir, 'output') };
  assert.equal(spawnSync(process.execPath, [script, 'playwright', '--', ...command], { cwd: dir, env }).status, 19);
  assert.equal(spawnSync(process.execPath, [script, 'record'], { cwd: dir, env }).status, 0);
  const record = JSON.parse(readFileSync(join(dir, 'ci-lane.json')));
  assert.equal(record.lane, 'full'); assert.match(record.sha, /^[a-f0-9]{40}$/); assert.match(record.tree, /^[a-f0-9]{40}$/);
});
test('queue checkout mismatch and missing ancestry are errors, not successful empty selections', () => {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-ci-lane-event-'));
  const eventPath = join(dir, 'event.json'); writeFileSync(eventPath, JSON.stringify(event));
  const env = { GITHUB_EVENT_NAME: 'merge_group', GITHUB_EVENT_PATH: eventPath, CI_IMPACTED_TESTS: 'on' };
  assert.throws(() => currentLane(env, args => args[1] === 'HEAD' ? base : head));
  assert.throws(() => currentLane(env, args => { if (args[0] === 'merge-base') throw new Error('shallow'); return head; }));
});
