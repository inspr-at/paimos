// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { requireRehearsal, requiredJobs, requiredSteps, repository, workflow } from './check-release-rehearsal.mjs';

const sha = 'a'.repeat(40);
const mainSha = 'b'.repeat(40);
const run = () => ({ id: 42, workflow_id: 7, path: workflow, head_sha: sha, head_branch: 'main',
  event: 'workflow_dispatch', status: 'completed', conclusion: 'success', run_attempt: 2,
  repository: { full_name: repository }, head_repository: { full_name: repository }, html_url: 'fixture-run' });
function fixture(change = () => {}) {
  const data = { policy: { id: 7, path: workflow, state: 'active' }, runs: [run()],
    jobs: requiredJobs.map(name => ({ name, status: 'completed', conclusion: 'success',
      steps: requiredSteps(name).map(name => ({ name, status: 'completed', conclusion: 'success' })) })),
    artifacts: [{ name: `release-rehearsal-${sha}-2`, expired: false, size_in_bytes: 100, workflow_run: { head_sha: sha, id: 42 } }],
    main: { object: { type: 'commit', sha: mainSha } },
    comparison: { status: 'ahead', base_commit: { sha }, merge_base_commit: { sha } } };
  change(data);
  const calls = [];
  const api = async (path, paginate) => {
    calls.push({ path, paginate });
    if (path === `repos/${repository}/git/ref/heads/main`) {
      if (data.mainError) throw new Error('Main API unavailable');
      return data.main;
    }
    if (path === `repos/${repository}/compare/${sha}...${data.main?.object?.sha}`) {
      if (data.compareError) throw new Error('Compare API unavailable');
      return data.comparison;
    }
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
  ['empty step inventory', d => { d.jobs[0].steps = []; }],
  ['missing smoke operation', d => { d.jobs[0].steps = d.jobs[0].steps.filter(s => s.name !== 'Smoke production image'); }],
  ['duplicate job', d => { d.jobs[1] = d.jobs[0]; }],
  ['no receipt', d => { d.artifacts = []; }],
  ['expired receipt', d => { d.artifacts[0].expired = true; }],
  ['receipt from previous attempt', d => { d.artifacts[0].name = `release-rehearsal-${sha}-1`; }],
  ['wrong receipt SHA', d => { d.artifacts[0].workflow_run.head_sha = 'b'.repeat(40); }],
  ['wrong receipt run', d => { d.artifacts[0].workflow_run.id = 41; }],
  ['missing receipt size', d => { delete d.artifacts[0].size_in_bytes; }],
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

for (const status of ['ahead', 'identical']) test(`hotfix/132 needs proof against current main: ${status}`, async () => {
  const { api, calls } = fixture(d => {
    d.runs[0].head_branch = 'hotfix/132';
    if (status === 'identical') d.main.object.sha = sha;
    d.comparison.status = status;
  });
  const receipt = await requireRehearsal(sha, api);
  assert.deepEqual(receipt, { sha, run_id: 42, run_attempt: 2, url: 'fixture-run' });
  assert.deepEqual(calls.slice(2, 4), [
    { path: `repos/${repository}/git/ref/heads/main`, paginate: undefined },
    { path: `repos/${repository}/compare/${sha}...${status === 'identical' ? sha : mainSha}`, paginate: undefined },
  ]);
  assert.equal(calls.length, 6);
  assert.ok([calls[1], ...calls.slice(4)].every(c => c.paginate === true));
});

test('main push still passes without any ancestry API lookup', async () => {
  const { api, calls } = fixture(d => { d.runs[0].event = 'push'; d.mainError = true; });
  assert.equal((await requireRehearsal(sha, api)).run_id, 42);
  assert.equal(calls.length, 4);
});

test('numbered hotfix branches also retain push eligibility', async () => {
  const { api } = fixture(d => { d.runs[0].head_branch = 'hotfix/1300'; d.runs[0].event = 'push'; });
  assert.equal((await requireRehearsal(sha, api)).run_id, 42);
});

for (const [name, mutate, reason] of [
  ['not an ancestor', d => { d.comparison.base_commit.sha = mainSha; }, 'ancestry proof'],
  ['behind current main', d => { d.comparison.status = 'behind'; }, 'ancestry proof'],
  ['diverged from current main', d => { d.comparison.status = 'diverged'; }, 'ancestry proof'],
  ['different merge base', d => { d.comparison.merge_base_commit.sha = mainSha; }, 'ancestry proof'],
  ['missing compare status', d => { delete d.comparison.status; }, 'ancestry proof'],
  ['missing base commit', d => { delete d.comparison.base_commit; }, 'ancestry proof'],
  ['missing merge base', d => { delete d.comparison.merge_base_commit; }, 'ancestry proof'],
  ['missing compare response', d => { d.comparison = null; }, 'ancestry proof'],
  ['missing main response', d => { d.main = null; }, 'current main commit'],
  ['missing main object', d => { d.main = {}; }, 'current main commit'],
  ['missing main SHA', d => { delete d.main.object.sha; }, 'current main commit'],
  ['short main SHA', d => { d.main.object.sha = mainSha.slice(0, 8); }, 'current main commit'],
  ['non-string main SHA', d => { d.main.object.sha = [mainSha]; }, 'current main commit'],
  ['main is not a commit', d => { d.main.object.type = 'tag'; }, 'current main commit'],
  ['missing main object type', d => { delete d.main.object.type; }, 'current main commit'],
  ['main API failure', d => { d.mainError = true; }, 'current main API read failed'],
  ['compare API failure', d => { d.compareError = true; }, 'ancestry API read failed'],
]) test(`hotfix fails closed: ${name}`, async () => {
  const { api, calls } = fixture(d => { d.runs[0].head_branch = 'hotfix/132'; mutate(d); });
  await assert.rejects(requireRehearsal(sha, api), new RegExp(`Hotfix rehearsal is not on main: ${reason}`));
  assert.ok(!calls.some(c => c.path.includes('/attempts/')));
});

for (const branch of ['feature/x', 'hotfix/abc', 'hotfixes/132', 'release/132', 'hotfix/12', 'hotfix/132/extra']) {
  test(`ineligible branch stays blocked: ${branch}`, async () => {
    const { api, calls } = fixture(d => { d.runs[0].head_branch = branch; });
    await assert.rejects(requireRehearsal(sha, api), /No current green main rehearsal for this exact SHA/);
    assert.equal(calls.length, 2);
  });
}

test('a non-string hotfix branch field stays ineligible', async () => {
  const { api, calls } = fixture(d => { d.runs[0].head_branch = ['hotfix/132']; });
  await assert.rejects(requireRehearsal(sha, api), /No current green main rehearsal/);
  assert.equal(calls.length, 2);
});

for (const branch of ['main', 'hotfix/132']) test(`newest green ${branch} run wins across main and hotfix`, async () => {
  const { api, calls } = fixture(d => {
    d.runs[0].head_branch = branch;
    d.runs.unshift({ ...run(), id: 41, head_branch: branch === 'main' ? 'hotfix/132' : 'main', conclusion: 'failure' });
  });
  assert.equal((await requireRehearsal(sha, api)).run_id, 42);
  assert.equal(calls.length, branch === 'main' ? 4 : 6);
});

for (const branch of ['main', 'hotfix/132']) for (const status of ['failed', 'pending']) {
  test(`newest ${status} ${branch} run revokes the other branch's success`, async () => {
    const { api, calls } = fixture(d => {
      d.runs[0].head_branch = branch === 'main' ? 'hotfix/132' : 'main';
      d.runs.push({ ...run(), id: 43, head_branch: branch,
        status: status === 'pending' ? 'in_progress' : 'completed', conclusion: status === 'pending' ? null : 'failure' });
    });
    await assert.rejects(requireRehearsal(sha, api), new RegExp(`No current green ${branch === 'main' ? 'main' : 'hotfix'} rehearsal`));
    assert.equal(calls.length, 2);
  });
}

test('newer ineligible branch cannot revoke a proven hotfix run', async () => {
  const { api } = fixture(d => {
    d.runs[0].head_branch = 'hotfix/132';
    d.runs.push({ ...run(), id: 43, head_branch: 'feature/x', conclusion: 'failure' });
  });
  assert.equal((await requireRehearsal(sha, api)).run_id, 42);
});

for (const [name, mutate, reason] of [
  ['PR event', d => { d.runs[0].event = 'pull_request'; }, /No current green main rehearsal/],
  ['fork', d => { d.runs[0].head_repository.full_name = 'fork/paimos'; }, /No current green main rehearsal/],
  ['missing required job', d => { d.jobs.pop(); }, /Rehearsal jobs or steps/],
  ['failed required step', d => { d.jobs[0].steps[0].conclusion = 'failure'; }, /Rehearsal jobs or steps/],
  ['wrong attempt receipt', d => { d.artifacts[0].name = `release-rehearsal-${sha}-1`; }, /Exact-SHA rehearsal receipt/],
]) test(`hotfix retains the existing release guard: ${name}`, async () => {
  await assert.rejects(requireRehearsal(sha, fixture(d => { d.runs[0].head_branch = 'hotfix/132'; mutate(d); }).api), reason);
});
