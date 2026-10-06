// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { decide, verifyRun, transport, main, requiredJobs, executionSteps, workflowPath } from './ci-tree-reuse.mjs';

const repository = 'inspr-at/paimos';
const sha = 'a'.repeat(40);
const listPath = `actions/workflows/ci.yml/runs?event=merge_group&status=success&head_sha=${sha}&per_page=20`;
function fixture(attempt = 1) {
  const run = { id: 123, name: 'CI', repository: { id: 10, full_name: repository }, head_repository: { full_name: repository }, workflow_id: 20, path: workflowPath, status: 'completed', conclusion: 'success', run_attempt: attempt, event: 'merge_group', head_sha: sha };
  const workflow = { id: 20, name: 'CI', path: workflowPath };
  const jobs = { total_count: requiredJobs.length + 2, jobs: requiredJobs.map(name => {
    const stepName = executionSteps[name] || (name.startsWith('go-test (') ? 'Test this shard (essential plus changed area, or full on main)' : name.startsWith('web-unit (') ? 'Run selected web units without retries' : name.startsWith('web-shard (') ? 'Run selected UI cases without retries' : undefined);
    return { name, run_attempt: attempt, status: 'completed', conclusion: 'success', steps: [...(stepName ? [{ name: stepName, status: 'completed', conclusion: 'success' }] : []), ...(/^(go-test|web-unit|web-shard) \(/.test(name)?[{name:'Confirm full tier execution',status:'completed',conclusion:'success'}]:[])] };
  }) };
  jobs.jobs.push({ name: 'cache-prime', status: 'completed', conclusion: 'skipped', run_attempt: attempt });
  jobs.jobs.push({ name: 'tree-reuse', status: 'completed', conclusion: 'skipped', run_attempt: attempt });
  const listed = { total_count: 1, workflow_runs: [{ id: 123, head_sha: sha, event: 'merge_group' }] };
  const calls = [];
  const api = async path => {
    calls.push(path);
    if (path === listPath) return listed;
    if (path === 'actions/runs/123') return run;
    if (path === 'actions/workflows/ci.yml') return workflow;
    if (path === `actions/runs/123/attempts/${attempt}/jobs?per_page=100`) return jobs;
    throw new Error(`Unexpected fixture request: ${path}`);
  };
  return { run, workflow, jobs, listed, calls, api };
}
const check = (f, options = {}) => decide({ event: 'push', ref: 'refs/heads/main', sha, repository, api: f.api, ...options });
const job = (f, name) => f.jobs.jobs.find(j => j.name === name);

test('found: an exact-SHA successful merge-group full suite reuses with source note', async () => {
  const f = fixture();
  assert.deepEqual(await check(f), { reuse: 'merge_group', run: 123, reason: 'reused merge_group run 123' });
  assert.deepEqual(await verifyRun(repository, 123, f.api, sha), { run: 123, attempt: 1, sha });
  assert(f.calls.includes(listPath));
  assert(f.calls.includes('actions/runs/123/attempts/1/jobs?per_page=100'));
  assert(!f.calls.some(p => /git\/|compare\/|pulls\/|statuses\//.test(p)));
});

test('the full suite reuses without the retired comparison job and still requires every surviving job', async () => {
  const f = fixture();
  // Do not inherit the verifier's obsolete requirement through the fixture.
  f.jobs.jobs = f.jobs.jobs.filter(j => j.name !== 'release-list-comparison');
  f.jobs.total_count = f.jobs.jobs.length;
  assert.deepEqual(await verifyRun(repository, 123, f.api, sha), { run: 123, attempt: 1, sha });
  assert.deepEqual(await check(f), { reuse: 'merge_group', run: 123, reason: 'reused merge_group run 123' });

  const fullJobs = f.jobs.jobs;
  for (const { name } of fullJobs.filter(j => !['cache-prime', 'tree-reuse'].includes(j.name))) {
    f.jobs.jobs = fullJobs.filter(j => j.name !== name);
    f.jobs.total_count = f.jobs.jobs.length;
    await assert.rejects(verifyRun(repository, 123, f.api, sha), /missing or failed suite job/, name);
    assert.equal((await check(f)).reuse, 'none', name);
  }
});

test('not found: a direct main push runs the full suite', async () => {
  const f = fixture(); f.listed.workflow_runs = []; f.listed.total_count = 0;
  assert.equal((await check(f)).reuse, 'none');
  assert.deepEqual(f.calls, [listPath]);
});

for (const [name, mutate] of [
  ['found but failed', f => { f.run.conclusion = 'failure'; }],
  ['found but cancelled', f => { f.run.conclusion = 'cancelled'; }],
  ['found but still running', f => { f.run.status = 'in_progress'; }],
  ['found but other workflow path', f => { f.run.path = '.github/workflows/other.yml'; }],
  ['found but other workflow ID', f => { f.run.workflow_id = 99; }],
  ['found but other workflow name', f => { f.run.name = 'Other CI'; }],
  ['workflow lookup has other name', f => { f.workflow.name = 'Other CI'; }],
  ['workflow lookup has other path', f => { f.workflow.path = '.github/workflows/other.yml'; }],
  ['workflow lookup has invalid ID', f => { f.workflow.id = 0; }],
  ['wrong repository', f => { f.run.repository.full_name = 'other/repo'; }],
  ['fork repository', f => { f.run.head_repository.full_name = 'other/repo'; }],
  ['missing repository ID', f => { delete f.run.repository.id; }],
  ['wrong run ID', f => { f.run.id = 321; }],
  ['wrong SHA despite discovery filter', f => { f.run.head_sha = 'b'.repeat(40); }],
  ['same tree at another SHA', f => { f.run.head_sha = 'c'.repeat(40); f.run.tree = sha; }],
  ['PR run', f => { f.run.event = 'pull_request'; }],
  ['push run', f => { f.run.event = 'push'; }],
  ['manual run', f => { f.run.event = 'workflow_dispatch'; }],
  ['unsafe run attempt', f => { f.run.run_attempt = Number.MAX_SAFE_INTEGER + 1; }],
  ['invalid run attempt', f => { f.run.run_attempt = 0; }],
  ['job from a different attempt', f => { job(f, 'go').run_attempt = 2; }],
  ['green aggregate but failed shard', f => { job(f, 'go-test (4)').conclusion = 'failure'; }],
  ['green aggregate but cancelled web shard', f => { job(f, 'web-shard (4)').conclusion = 'cancelled'; }],
  ['skipped required job', f => { job(f, 'release-check').conclusion = 'skipped'; }],
  ['unfinished required job', f => { job(f, 'e2e').status = 'in_progress'; }],
  ['missing Go shard', f => { f.jobs.jobs = f.jobs.jobs.filter(j => j.name !== 'go-test (7)'); f.jobs.total_count--; }],
  ['missing web shard', f => { f.jobs.jobs = f.jobs.jobs.filter(j => j.name !== 'web-shard (12)'); f.jobs.total_count--; }],
  ['duplicate suite job', f => { f.jobs.jobs.push({ ...job(f, 'go') }); f.jobs.total_count++; }],
  ['unknown suite job', f => { f.jobs.jobs.push({ ...job(f, 'go'), name: 'unknown' }); f.jobs.total_count++; }],
  ['incomplete pagination', f => { f.jobs.total_count++; }],
  ['oversized job list', f => { f.jobs.total_count = 101; }],
  ['missing jobs', f => { delete f.jobs.jobs; }],
  ['skipped tests on green rerun', f => { job(f, 'go-test (1)').steps[0].conclusion = 'skipped'; }],
  ['skipped browser execution', f => { job(f, 'web-shard (1)').steps[0].conclusion = 'skipped'; }],
  ['missing migration execution', f => { job(f, 'migration-compat').steps = []; }],
  ['duplicate execution step', f => { job(f, 'web-setup').steps.push({ ...job(f, 'web-setup').steps[0] }); }],
  ['reused echo is not test evidence', f => { job(f, 'e2e').steps.push({ name: 'Reuse the verified merge-group run', conclusion: 'success' }); }],
  ['failed optional job', f => { job(f, 'cache-prime').conclusion = 'failure'; }],
  ['malformed discovery', f => { f.listed.workflow_runs = undefined; }],
  ['oversized discovery', f => { f.listed.workflow_runs = Array(21).fill(f.listed.workflow_runs[0]); }],
  ['unsafe candidate ID', f => { f.listed.workflow_runs[0].id = Number.MAX_SAFE_INTEGER + 1; }],
  ['wrong listed SHA', f => { f.listed.workflow_runs[0].head_sha = 'b'.repeat(40); }],
  ['wrong listed event', f => { f.listed.workflow_runs[0].event = 'push'; }],
]) test(`${name} falls back to full CI`, async () => {
  const f = fixture(); mutate(f);
  assert.equal((await check(f)).reuse, 'none');
});

test('found on full rerun: latest fully executed attempt is eligible', async () => {
  const f = fixture(2);
  assert.equal((await check(f)).reuse, 'merge_group');
  assert(f.calls.includes('actions/runs/123/attempts/2/jobs?per_page=100'));
  assert(!f.calls.some(p => p.includes('/attempts/1/')));
});

test('partial rerun cannot use retained green job results', async () => {
  const f = fixture(2);
  f.jobs.jobs = [job(f, 'go')]; f.jobs.total_count = 1;
  await assert.rejects(verifyRun(repository, 123, f.api, sha), /missing or failed suite job/);
  assert.equal((await check(f)).reuse, 'none');
});

test('skipped push-only cache job is allowed but no required job may skip', async () => {
  const f = fixture();
  assert.equal(job(f, 'cache-prime').conclusion, 'skipped');
  assert.equal((await check(f)).reuse, 'merge_group');
  for (const name of requiredJobs) {
    const changed = fixture(); job(changed, name).conclusion = 'skipped';
    assert.equal((await check(changed)).reuse, 'none', name);
  }
});

test('every heavy execution step must succeed, even with green job conclusions', async () => {
  for (const name of requiredJobs.filter(n => executionSteps[n] || /^(go-test|web-unit|web-shard) \(/.test(n))) {
    const f = fixture(); job(f, name).steps[0].conclusion = 'skipped';
    await assert.rejects(verifyRun(repository, 123, f.api, sha), /missing full execution evidence/);
    assert.equal((await check(f)).reuse, 'none', name);
  }
});

test('kill switch, PR, queue, manual and non-main push do no API reads', async () => {
  for (const options of [{ killSwitch: 'off' }, { event: 'pull_request' }, { event: 'merge_group' }, { event: 'workflow_dispatch' }, { ref: 'refs/heads/other' }]) {
    const f = fixture();
    assert.equal((await check(f, { ...options, api: () => assert.fail('must not read') })).reuse, 'none');
    assert.deepEqual(f.calls, []);
  }
});

test('network failure and invalid SHA fail closed', async () => {
  assert.equal((await check(fixture(), { api: async () => { throw new Error('offline'); } })).reuse, 'none');
  const f = fixture(); assert.equal((await check(f, { sha: 'invalid' })).reuse, 'none'); assert.deepEqual(f.calls, []);
});

test('a truncated candidate search reports truncation and falls back', async () => {
  const f = fixture(); f.listed.total_count = 21; f.listed.workflow_runs = [];
  const result = await check(f);
  assert.equal(result.reuse, 'none'); assert.match(result.reason, /search truncated/);
});

test('invalid candidate does not hide a subsequent verified run', async () => {
  const f = fixture(); f.listed.workflow_runs.unshift({ id: 999, head_sha: sha, event: 'merge_group' }); f.listed.total_count++;
  assert.equal((await check(f)).run, 123);
  assert(f.calls.includes('actions/runs/999'));
});

test('verified main entry point emits reuse and source-run outputs plus success note', async () => {
  const f = fixture(); const dir = mkdtempSync(join(tmpdir(), 'aeon-423-'));
  const output = join(dir, 'output'); const summary = join(dir, 'summary');
  const result = await main({ GITHUB_EVENT_NAME: 'push', GITHUB_REF: 'refs/heads/main', GITHUB_SHA: sha, GITHUB_REPOSITORY: repository, GITHUB_ACTOR: 'fixture', GH_TOKEN: 'fixture', GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: summary }, async url => {
    const prefix = `https://api.github.com/repos/${repository}/`;
    assert(url.startsWith(prefix));
    return new Response(JSON.stringify(await f.api(url.slice(prefix.length))));
  });
  assert.equal(result.reuse, 'merge_group');
  assert.equal(readFileSync(output, 'utf8'), 'reuse=merge_group\nrun=123\n');
  assert.match(readFileSync(summary, 'utf8'), /reused merge_group run 123/);
});

test('direct main entry point cannot reuse absent merge-group evidence', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-423-'));
  const output = join(dir, 'output'); let calls = 0;
  const result = await main({ GITHUB_EVENT_NAME: 'push', GITHUB_REF: 'refs/heads/main', GITHUB_SHA: sha, GITHUB_REPOSITORY: repository, GITHUB_ACTOR: 'fixture', GH_TOKEN: 'fixture', GITHUB_OUTPUT: output }, async url => {
    calls++; assert.equal(url, `https://api.github.com/repos/${repository}/${listPath}`);
    return new Response(JSON.stringify({ total_count: 0, workflow_runs: [] }));
  });
  assert.equal(calls, 1); assert.equal(result.reuse, 'none');
  assert.equal(readFileSync(output, 'utf8'), 'reuse=none\nrun=\n');
});

test('disabled main entry point writes fallback outputs and an honest summary', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-423-'));
  const output = join(dir, 'output'); const summary = join(dir, 'summary');
  assert.equal((await main({ GITHUB_EVENT_NAME: 'push', GITHUB_REF: 'refs/heads/main', CI_TREE_REUSE: 'off', GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: summary })).reuse, 'none');
  assert.equal(readFileSync(output, 'utf8'), 'reuse=none\nrun=\n');
  assert.match(readFileSync(summary, 'utf8'), /disabled/);
});

test('output write failure is not reported as success', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-423-'));
  await assert.rejects(main({ GITHUB_OUTPUT: dir }));
});

test('transport bounds responses and endpoints before JSON, forbids redirects', async () => {
  const calls = [];
  const request = transport('fixture', 'fixture', new AbortController().signal, async (_url, options) => { calls.push(options); return new Response('{}'); });
  assert.deepEqual((await request('https://api.github.com/repos/inspr-at/paimos')).json(), {});
  assert.equal(calls[0].redirect, 'error');
  for (const url of ['https://example.com', 'https://ghcr.io/v2/', 'http://api.github.com', 'https://user:password@api.github.com']) await assert.rejects(request(url), /endpoint/);
  const large = transport('fixture', 'fixture', undefined, async () => new Response('not JSON', { headers: { 'Content-Length': String(3 * 1024 * 1024) } }));
  await assert.rejects(large('https://api.github.com'), /too large/);
  const chunked = transport('fixture', 'fixture', undefined, async () => new Response(Buffer.alloc(2 * 1024 * 1024 + 1)));
  await assert.rejects(chunked('https://api.github.com'), /too large/);
  const failed = transport('fixture', 'fixture', undefined, async () => new Response('{}', { status: 403 }));
  await assert.rejects(failed('https://api.github.com'), /remote request failed/);
});

test('transport forwards cancellation with an injected signal, no timing thresholds', async () => {
  const controller = new AbortController(); controller.abort();
  const request = transport('fixture', 'fixture', controller.signal, async (_url, options) => { assert(options.signal.aborted); throw options.signal.reason; });
  await assert.rejects(request('https://api.github.com'));
});

// Narrowed queue coverage cannot masquerade as the full-suite proof AEON-423 requires.
test('a two-shard essential queue run falls back to fresh full main validation', async () => {
  const f=fixture();f.jobs.jobs=f.jobs.jobs.filter(j=>!(/^(go-test|web-shard) \(/.test(j.name))||/\([12]\)$/.test(j.name));f.jobs.total_count=f.jobs.jobs.length;
  assert.equal((await check(f)).reuse,'none');
});

test('full fan-out alone cannot prove full execution when the independent selector narrowed',async()=>{
  const f=fixture();job(f,'go-test (1)').steps=job(f,'go-test (1)').steps.filter(step=>step.name!=='Confirm full tier execution');
  assert.equal((await check(f)).reuse,'none');
  const web=fixture();job(web,'web-unit (1)').steps.find(step=>step.name==='Confirm full tier execution').conclusion='skipped';
  assert.equal((await check(web)).reuse,'none');
});
