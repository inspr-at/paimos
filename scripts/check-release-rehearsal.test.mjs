// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { requireRehearsal, requiredJobs, repository, workflow } from './check-release-rehearsal.mjs';

const sha = 'a'.repeat(40);
const run = () => ({ id: 42, workflow_id: 7, path: workflow, head_sha: sha, head_branch: 'main',
  event: 'workflow_dispatch', status: 'completed', conclusion: 'success', run_attempt: 2,
  repository: { full_name: repository }, head_repository: { full_name: repository }, html_url: 'fixture-run' });
function fixture(change = () => {}) {
  const data = { policy: { id: 7, path: workflow, state: 'active' }, runs: [run()],
    jobs: requiredJobs.map(name => ({ name, status: 'completed', conclusion: 'success',
      steps: [{ name: 'fixture', status: 'completed', conclusion: 'success' }] })),
    artifacts: [{ name: `release-rehearsal-${sha}-2`, expired: false, size_in_bytes: 100, workflow_run: { head_sha: sha } }] };
  change(data);
  const calls = [];
  const api = async (path, paginate) => {
    calls.push({ path, paginate });
    if (path.endsWith('/workflows/release-image-check.yml')) return data.policy;
    if (path.includes('/workflows/7/runs?')) return [{ workflow_runs: data.runs.slice(0, 1) }, { workflow_runs: data.runs.slice(1) }];
    if (path.includes('/runs/42/attempts/2/jobs?')) return [{ jobs: data.jobs.slice(0, 2) }, { jobs: data.jobs.slice(2) }];
    if (path.includes('/runs/42/artifacts?')) return [{ artifacts: data.artifacts }];
    throw new Error('Unexpected receipt lookup');
  };
  return { api, calls };
}

test('only exact-SHA main success with all jobs, steps and a receipt passes', async () => {
  const { api, calls } = fixture();
  assert.deepEqual(await requireRehearsal(sha, api), { sha, run_id: 42, run_attempt: 2, url: 'fixture-run' });
  assert.equal(calls.length, 4);
  assert.ok(calls.slice(1).every(c => c.paginate === true));
  assert.ok(calls[1].path.includes(`head_sha=${sha}`));
});

for (const [name, mutate] of [
  ['PR receipt', d => { d.runs[0].event = 'pull_request'; }],
  ['feature branch', d => { d.runs[0].head_branch = 'work/feature'; }],
  ['fork', d => { d.runs[0].head_repository.full_name = 'fork/paimos'; }],
  ['wrong repository', d => { d.runs[0].repository.full_name = 'other/paimos'; }],
  ['different SHA', d => { d.runs[0].head_sha = 'b'.repeat(40); }],
  ['different workflow', d => { d.runs[0].workflow_id = 8; }],
  ['forged workflow path', d => { d.runs[0].path = '.github/workflows/other.yml'; }],
  ['inactive workflow', d => { d.policy.state = 'disabled_manually'; }],
  ['no receipt run', d => { d.runs = []; }],
  ['new failed run revokes old success', d => { d.runs.push({ ...run(), id: 43, conclusion: 'failure' }); }],
  ['pending', d => { d.runs[0].status = 'in_progress'; }],
  ['failed rerun', d => { d.runs[0].conclusion = 'failure'; }],
  ['skipped native job', d => { d.jobs[2].conclusion = 'skipped'; }],
  ['failed step masked by continue-on-error', d => { d.jobs[0].steps[0].conclusion = 'failure'; }],
  ['skipped smoke step', d => { d.jobs[0].steps[0].conclusion = 'skipped'; }],
  ['missing platform', d => { d.jobs.pop(); }],
  ['duplicate job', d => { d.jobs[1] = d.jobs[0]; }],
  ['no receipt', d => { d.artifacts = []; }],
  ['expired receipt', d => { d.artifacts[0].expired = true; }],
  ['receipt from previous attempt', d => { d.artifacts[0].name = `release-rehearsal-${sha}-1`; }],
  ['wrong receipt SHA', d => { d.artifacts[0].workflow_run.head_sha = 'b'.repeat(40); }],
  ['duplicate receipt', d => { d.artifacts.push(d.artifacts[0]); }],
]) test(`release stays blocked: ${name}`, async () => {
  await assert.rejects(requireRehearsal(sha, fixture(mutate).api));
});

test('API failures, malformed pages and short SHAs fail closed', async () => {
  await assert.rejects(requireRehearsal(sha, async () => { throw new Error('unavailable'); }));
  await assert.rejects(requireRehearsal(sha.slice(0, 8), fixture().api));
  for (const reply of [null, {}, [{ workflow_runs: null }]]) {
    await assert.rejects(requireRehearsal(sha, async path => path.endsWith('.yml')
      ? { id: 7, path: workflow, state: 'active' } : reply));
  }
});
