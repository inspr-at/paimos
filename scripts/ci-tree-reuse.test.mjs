// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import { decide, verifyRun, transport, main, requiredJobs, executionSteps, workflowPath, decideMergeGroup, verifyPRFacts, verifyPRRun, prProofPrefix } from './ci-tree-reuse.mjs';

const repository = 'inspr-at/paimos';
const sha = 'a'.repeat(40);
const listPath = `actions/workflows/ci.yml/runs?event=merge_group&status=success&head_sha=${sha}&per_page=20`;
function fixture(attempt = 1) {
  const run = { id: 123, name: 'CI', repository: { id: 10, full_name: repository }, head_repository: { full_name: repository }, workflow_id: 20, path: workflowPath, status: 'completed', conclusion: 'success', run_attempt: attempt, event: 'merge_group', head_sha: sha };
  const workflow = { id: 20, name: 'CI', path: workflowPath, decideMergeGroup, verifyPRFacts, verifyPRRun, prProofPrefix };
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

// OPS-257 L5: immutable API facts plus a value-free aggregate job-name proof.
const prFixturePath = new URL('./releaseworkflow/testdata/ci-pr-tree-proof.json', import.meta.url);
const prListPath = 'actions/workflows/ci.yml/runs?event=pull_request&status=success&per_page=20';
function prFixture() {
  const f = JSON.parse(readFileSync(prFixturePath, 'utf8'));
  f.calls = [];
  f.api = async path => {
    f.calls.push(path);
    if (path === prListPath) return f.listed;
    if (path === 'actions/runs/123') return f.latest && f.calls.filter(x => x === path).length > 1 ? f.latest : f.run;
    if (path === 'actions/workflows/ci.yml') return f.workflow;
    if (path === 'actions/runs/123/attempts/1/jobs?per_page=100') return f.jobs;
    if (path === `git/commits/${f.mg}`) return f.candidate;
    if (path === `git/commits/${'c'.repeat(40)}`) return f.commit;
    throw new Error(`Unexpected fixture path: ${path}`);
  };
  return f;
}
const prCheck = (f, options = {}) => decideMergeGroup({ event: 'merge_group', killSwitch: 'on', sha: f.mg, repository: f.repository, api: f.api, ...options });
const prJob = f => f.jobs.jobs.find(j => j.name.startsWith(prProofPrefix));
const prFacts = f => verifyPRFacts({ repository: f.repository, run: f.run, workflow: f.workflow, jobs: f.jobs, commit: f.commit, tree: f.candidate.tree.sha });

test('MG reuses the PR checkout merge tree even when the head tree is different', async () => {
  const f = prFixture();
  assert.deepEqual(prFacts(f), { run: 123, attempt: 1, head: 'a'.repeat(40), base: 'b'.repeat(40), commit: 'c'.repeat(40), tree: 'd'.repeat(40) });
  assert.equal((await prCheck(f)).reuse, 'pull_request');
  assert(f.calls.includes(`git/commits/${f.commit.sha}`));
  assert.equal(f.calls.filter(p => p === 'actions/runs/123').length, 2);
  assert(!f.calls.some(p => /artifacts|pulls\//.test(p)), 'Mutable PR refs and expiring artifacts cannot supply proof');
});

for (const [name, mutate, error] of [
  ['different repository', f => f.run.repository.full_name = 'other/repo', /wrong repository/],
  ['fork head', f => f.run.head_repository.full_name = 'fork/paimos', /wrong repository/],
  ['missing head repo', f => delete f.run.head_repository, /wrong repository/],
  ['missing repo ID', f => delete f.run.repository.id, /wrong repository/],
  ['different workflow', f => f.run.workflow_id = 2, /wrong workflow/],
  ['different workflow path', f => f.run.path = '.github/workflows/fake.yml', /wrong workflow/],
  ['different workflow name', f => f.workflow.name = 'fake', /wrong workflow/],
  ['incomplete run', f => f.run.status = 'in_progress', /green PR/],
  ['cancelled run', f => f.run.conclusion = 'cancelled', /green PR/],
  ['failed run', f => f.run.conclusion = 'failure', /green PR/],
  ['non-PR source', f => f.run.event = 'merge_group', /green PR/],
  ['partial rerun with retained greens', f => f.run.run_attempt = 2, /reruns/],
  ['full rerun deliberately rejected', f => { f.run.run_attempt = 2; f.jobs.jobs.forEach(j => j.run_attempt = 2); }, /reruns/],
  ['missing attempt', f => delete f.run.run_attempt, /reruns/],
  ['missing proof (including legacy/expired artifact-only evidence)', f => prJob(f).name = 'tier-measurements', /missing or duplicate PR proof/],
  ['duplicate proof', f => { f.jobs.jobs.push(structuredClone(prJob(f))); f.jobs.total_count++; }, /duplicate PR proof/],
  ['malformed proof', f => prJob(f).name = prProofPrefix + 'bad', /malformed/],
  ['unsafe proof run', f => prJob(f).name = prJob(f).name.replace(':123:', ':9007199254740992:'), /unsafe/],
  ['proof for another run', f => prJob(f).name = prJob(f).name.replace(':123:', ':124:'), /match run/],
  ['proof for another attempt', f => prJob(f).name = prJob(f).name.replace(':123:1:', ':123:2:'), /match run/],
  ['proof for another head', f => f.run.head_sha = 'f'.repeat(40), /match run/],
  ['proof step skipped (essential/spec/docs)', f => prJob(f).steps[0].conclusion = 'skipped', /not emitted/],
  ['proof step missing', f => prJob(f).steps = [], /not emitted/],
  ['proof step unfinished', f => prJob(f).steps[0].status = 'in_progress', /not emitted/],
  ['proof step duplicated', f => prJob(f).steps.push({ ...prJob(f).steps[0] }), /not emitted/],
  ['different merge commit', f => f.commit.sha = 'f'.repeat(40), /proven merge/],
  ['head tree substituted for checkout tree', f => f.commit.tree.sha = f.run.head_sha, /proven merge/],
  ['different merge base', f => f.commit.parents[0].sha = 'f'.repeat(40), /proven merge/],
  ['different merge head', f => f.commit.parents[1].sha = 'f'.repeat(40), /proven merge/],
  ['head commit instead of merge checkout', f => f.commit.parents.pop(), /proven merge/],
  ['different MG tree', f => f.candidate.tree.sha = 'f'.repeat(40), /different tested tree/],
  ['retained job from another attempt', f => job(f, 'go').run_attempt = 2, /incomplete suite/],
  ['green aggregate with cancelled shard', f => job(f, 'go-test (3)').conclusion = 'cancelled', /incomplete suite/],
  ['skipped full execution', f => job(f, 'web-shard (1)').steps[0].conclusion = 'skipped', /full execution evidence/],
  ['full fanout but essential selection', f => job(f, 'web-unit (1)').steps[1].conclusion = 'skipped', /full tier execution evidence/],
  ['missing full Go shard', f => { f.jobs.jobs = f.jobs.jobs.filter(j => j.name !== 'go-test (7)'); f.jobs.total_count--; }, /missing or failed/],
  ['missing full browser shard', f => { f.jobs.jobs = f.jobs.jobs.filter(j => j.name !== 'web-shard (12)'); f.jobs.total_count--; }, /missing or failed/],
  ['truncated jobs', f => f.jobs.total_count++, /incomplete job list/],
  ['oversized jobs', f => f.jobs.total_count = 101, /incomplete job list/],
  ['unknown job', f => { f.jobs.jobs.push({ ...job(f, 'go'), name: 'unknown' }); f.jobs.total_count++; }, /unknown suite job/],
  ['missing jobs', f => delete f.jobs.jobs, /missing jobs/],
]) test(`PR tree proof fails closed: ${name}`, async () => {
  const f = prFixture(); mutate(f);
  assert.throws(() => prFacts(f), error);
  assert.equal((await prCheck(f)).reuse, 'none');
});

test('every PR full-suite job and every full execution marker remains mandatory', () => {
  for (const name of requiredJobs) {
    const f = prFixture();
    if (name === 'tier-measurements') prJob(f).conclusion = 'skipped';
    else job(f, name).conclusion = 'skipped';
    assert.throws(() => prFacts(f), /incomplete suite/, name);
  }
  for (const name of requiredJobs.filter(n => /^(go-test|web-unit|web-shard) \(/.test(n))) {
    const f = prFixture(); job(f, name).steps = job(f, name).steps.filter(s => s.name !== 'Confirm full tier execution');
    assert.throws(() => prFacts(f), /full tier execution evidence/, name);
  }
});

test('unset/off/non-on MG flag and wrong events do no API work', async () => {
  for (const options of [{ killSwitch: undefined }, { killSwitch: 'off' }, { killSwitch: 'ON' }, { killSwitch: 'true' }, { event: 'pull_request' }, { event: 'push' }, { event: 'workflow_dispatch' }]) {
    assert.equal((await prCheck(prFixture(), { ...options, api: () => assert.fail('disabled lookup') })).reuse, 'none');
  }
});

test('missing/truncated/malformed candidates, API errors and absent git objects retain full MG', async () => {
  for (const mutate of [f => { f.listed.workflow_runs = []; f.listed.total_count = 30; }, f => delete f.listed.workflow_runs, f => f.listed.workflow_runs = Array(21).fill(f.listed.workflow_runs[0]), f => f.listed.workflow_runs[0].id = 0, f => f.listed.workflow_runs[0].event = 'push', f => f.candidate.sha = 'f'.repeat(40)]) {
    const f = prFixture(); mutate(f); assert.equal((await prCheck(f)).reuse, 'none');
  }
  const f = prFixture();
  for (const failPath of [prListPath, 'actions/runs/123', 'actions/workflows/ci.yml', 'actions/runs/123/attempts/1/jobs?per_page=100', `git/commits/${f.commit.sha}`, `git/commits/${f.mg}`]) {
    assert.equal((await prCheck(prFixture(), { api: async path => { if (path === failPath) throw new Error('unavailable'); return f.api(path); } })).reuse, 'none', failPath);
  }
});

test('source rerun or cancellation during proof inspection invalidates MG reuse', async () => {
  for (const change of [{ run_attempt: 2 }, { status: 'in_progress' }, { conclusion: 'cancelled' }, { head_sha: 'f'.repeat(40) }]) {
    const f = prFixture(); f.latest = { ...f.run, ...change };
    await assert.rejects(verifyPRRun(f.repository, 123, f.api, f.mg), /changed during lookup/);
    assert.equal((await prCheck(f)).reuse, 'none');
  }
});

test('MG entry point emits proof outputs, but aggregate confirmation rejects stale or disabled proof', async () => {
  const f = prFixture(), dir = mkdtempSync(join(tmpdir(), 'ops257-mg-'));
  const vars = { GITHUB_EVENT_NAME: 'merge_group', GITHUB_SHA: f.mg, GITHUB_REPOSITORY: f.repository, CI_MG_REUSE: 'on', CI_TREE_REUSE: 'off', GH_TOKEN: 'fixture', GITHUB_ACTOR: 'fixture', GITHUB_OUTPUT: join(dir, 'lookup') };
  const fetcher = async url => new Response(JSON.stringify(await f.api(url.split(`/repos/${f.repository}/`)[1])));
  assert.equal((await main(vars, fetcher)).reuse, 'pull_request');
  assert.equal(readFileSync(vars.GITHUB_OUTPUT, 'utf8'), 'reuse=pull_request\nrun=123\n');
  const confirm = { ...vars, CONFIRM_PR_RUN: '123', GITHUB_OUTPUT: join(dir, 'confirm') };
  assert.equal((await main(confirm, fetcher)).reuse, 'pull_request');
  f.run.run_attempt = 2;
  await assert.rejects(main(confirm, fetcher), /reruns/);
  await assert.rejects(main({ ...confirm, CI_MG_REUSE: 'off' }, fetcher), /disabled/);
  await assert.rejects(main({ ...confirm, CONFIRM_PR_RUN: 'not-a-run' }, fetcher), /invalid confirmation/);
});


test('the actual inline PR capture binds HEAD, parents, run and tree without candidate code', () => {
  const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const step = workflow.split('      - name: Capture PR checkout identity\n')[1].split(/\n\n  [a-z][a-z-]*:/)[0];
  const script = step.split('        run: |\n')[1].replace(/^          /gm, '');
  const dir = mkdtempSync(join(tmpdir(), 'ops257-capture-'));
  const fixtureEnv = { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_AUTHOR_NAME: 'Fixture', GIT_AUTHOR_EMAIL: 'fixture@example.invalid',
    GIT_COMMITTER_NAME: 'Fixture', GIT_COMMITTER_EMAIL: 'fixture@example.invalid' };
  const git = (args, input) => execFileSync('git', args, { cwd: dir, encoding: 'utf8', env: fixtureEnv, input }).trim();
  git(['init', '-q']);
  const tree = git(['mktree'], '');
  const base = git(['commit-tree', tree], 'base');
  const head = git(['commit-tree', tree, '-p', base], 'head');
  const commit = git(['commit-tree', tree, '-p', base, '-p', head], 'merge');
  git(['update-ref', 'HEAD', commit]);
  const values = { ...fixtureEnv, GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1', GITHUB_SHA: commit, PR_BASE_SHA: base, PR_HEAD_SHA: head };
  for (const [label, changes, success] of [
    ['valid', {}, true], ['wrong-checkout', { GITHUB_SHA: 'f'.repeat(40) }, false],
    ['wrong-base', { PR_BASE_SHA: 'f'.repeat(40) }, false], ['wrong-head', { PR_HEAD_SHA: 'f'.repeat(40) }, false],
    ['invalid-run', { GITHUB_RUN_ID: '123\nreuse=pull_request' }, false],
  ]) {
    const output = join(dir, label);
    const result = spawnSync('bash', ['-c', script], { cwd: dir, encoding: 'utf8', env: { ...values, ...changes, GITHUB_OUTPUT: output }, timeout: 10000 });
    assert.equal(result.status === 0, success, result.stderr);
    if (success) {
      assert.equal(readFileSync(output, 'utf8'), `proof=pr-tree-v1:123:1:${head}:${base}:${commit}:${tree}\n`);
    }
  }
});
