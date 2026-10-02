// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { assertReleaseAllowed, decideHold, github, readHold, reconcileHold, repository } from './release-hold.mjs';

const a = 'a'.repeat(40), b = 'b'.repeat(40), c = 'c'.repeat(40);
const root = `/repos/${repository}/`;
const workflow = { id: 7, path: '.github/workflows/ci.yml' };
const run = (sha, conclusion, id, more = {}) => ({ id, run_attempt: 1, head_sha: sha, conclusion,
  event: 'push', head_branch: 'main', status: 'completed', path: workflow.path, workflow_id: 7,
  repository: { full_name: repository }, head_repository: { full_name: repository },
  updated_at: '2026-10-02T16:00:00Z', ...more });
const greenJobs = ['go', 'go-static', 'go-timing', 'web', 'release-check', 'e2e', 'footer-ui',
  'status-help-ui', 'status-autopilot-ui', 'migration-compat', 'runner-route / route',
  ...[1, 2, 3, 4, 5, 6, 7].map(n => `go-test (${n})`)].map(name => ({ name, status: 'completed', conclusion: 'success' }));
const order = async (left, right) => [a, b, c].indexOf(left) - [a, b, c].indexOf(right);

function fixture({ hold = '', runs = [run(b, 'failure', 2)], jobs = greenJobs, fail, issueFail = false, ignoreWrite = false } = {}) {
  const calls = [], issues = [];
  let exists = Boolean(hold);
  const api = async (method, fullPath, body) => {
    calls.push({ method, path: fullPath, body });
    const path = fullPath.slice(root.length);
    if (fail?.(method, path)) return { status: 403, data: { message: 'private API response must never be printed' } };
    if (path === 'actions/variables?per_page=30&page=1') return { status: 200,
      data: { total_count: exists ? 1 : 0, variables: exists ? [{ name: 'RELEASE_HOLD', value: hold }] : [] } };
    if (path === 'actions/workflows/ci.yml') return { status: 200, data: workflow };
    if (path.startsWith('actions/workflows/7/runs?')) return { status: 200, data: { total_count: runs.length, workflow_runs: structuredClone(runs) } };
    if (/^actions\/runs\/\d+$/.test(path)) return { status: 200, data: runs.find(r => r.id === Number(path.split('/').at(-1))) };
    if (path.includes('/jobs?')) return { status: 200, data: { total_count: jobs.length, jobs } };
    if (path.startsWith('compare/')) {
      const [left, right] = path.slice(8).split('?')[0].split('...');
      const before = await order(left, right) < 0;
      return { status: 200, data: { status: before ? 'ahead' : 'behind', base_commit: { sha: left }, merge_base_commit: { sha: before ? left : right } } };
    }
    if (method === 'GET' && path.startsWith('issues?')) return { status: 200, data: issues.filter(i => i.state === 'open') };
    if (method === 'POST' && path === 'issues') {
      if (issueFail) return { status: 503, data: null };
      const issue = { number: issues.length + 1, state: 'open', ...body }; issues.push(issue);
      return { status: 201, data: issue };
    }
    if (method === 'PATCH' && path.startsWith('issues/')) {
      const issue = issues.find(i => i.number === Number(path.split('/')[1])); Object.assign(issue, body);
      return { status: 200, data: issue };
    }
    if (['POST', 'PATCH', 'DELETE'].includes(method) && path.startsWith('actions/variables')) {
      if (!ignoreWrite) { hold = method === 'DELETE' ? '' : body.value; exists = method !== 'DELETE'; }
      return { status: method === 'POST' ? 201 : 204, data: null };
    }
    throw new Error(`unexpected fixture operation: ${method} ${path}`);
  };
  return { api, calls, runs, issues, value: () => hold };
}
const changes = f => f.calls.filter(c => c.method !== 'GET');

test('preview is read-only; engage persists the SHA before opening one alert', async () => {
  const f = fixture();
  assert.equal((await reconcileHold(f.api)).action, 'engage');
  assert.deepEqual(changes(f), []);
  const result = await reconcileHold(f.api, { write: true, sourceRunID: 2, now: () => new Date('2026-10-02T16:05:59Z') });
  assert.equal(result.value, b); assert.equal(f.value(), b);
  assert.equal(result.engage_after_red_s, 359); assert.equal(result.six_minute_target_met, true);
  assert.deepEqual(changes(f).map(c => c.method), ['POST', 'POST']);
  await reconcileHold(f.api, { write: true });
  assert.equal(f.issues.length, 1); assert.equal(changes(f).length, 2);
});

test('a green descendant clears the hold and closes the matching alert', async () => {
  const f = fixture();
  await reconcileHold(f.api, { write: true });
  f.runs.push(run(c, 'success', 3));
  const result = await reconcileHold(f.api, { write: true });
  assert.equal(result.action, 'clear'); assert.equal(f.value(), ''); assert.equal(f.issues[0].state, 'closed');
});

test('stale older SHA clears only on a full green descendant, including a paged-out failure', async () => {
  const f = fixture({ hold: a, runs: [run(c, 'success', 3)] });
  assert.equal((await reconcileHold(f.api, { write: true })).action, 'clear');
  assert.equal(f.value(), '');
});

test('an older green cannot clear a newer hold; cancellation does not count as recovery', async () => {
  for (const rows of [[run(a, 'success', 1)], [run(c, 'cancelled', 3)], [run(c, 'failure', 3, { status: 'in_progress' })]]) {
    const f = fixture({ hold: b, runs: rows });
    assert.equal((await reconcileHold(f.api, { write: true })).value, b);
    assert.deepEqual(changes(f), []);
  }
});

test('late red/green completion orders converge by ancestry, never by timestamps', async () => {
  const failed = run(b, 'failure', 2, { updated_at: '2026-10-02T16:10:00Z' });
  const passed = run(c, 'success', 3, { updated_at: '2026-10-02T16:01:00Z' });
  for (const rows of [[failed, passed], [passed, failed]]) {
    for (const sourceRunID of [2, 3]) {
      const f = fixture({ hold: b, runs: rows });
      assert.equal((await reconcileHold(f.api, { write: true, sourceRunID })).value, '');
      assert.equal((await reconcileHold(f.api, { write: true, sourceRunID: 2 })).value, '');
    }
  }
  for (const rows of [[run(c, 'failure', 3), run(b, 'success', 2)], [run(b, 'success', 2), run(c, 'failure', 3)]]) {
    assert.equal((await reconcileHold(fixture({ runs: rows }).api)).value, c);
  }
});

test('barrier-delayed red delivery after green recovery reads current state and stays clear', async () => {
  const f = fixture({ hold: b });
  let release, entered;
  const barrier = new Promise(resolve => { release = resolve; });
  const arrived = new Promise(resolve => { entered = resolve; });
  let blocked = false;
  const delayed = reconcileHold(async (...args) => {
    if (!blocked) { blocked = true; entered(); await barrier; }
    return f.api(...args);
  }, { write: true, sourceRunID: 2 });
  await arrived;
  f.runs.push(run(c, 'success', 3));
  await reconcileHold(f.api, { write: true, sourceRunID: 3 });
  assert.equal(f.value(), '');
  release();
  assert.equal((await delayed).value, '');
  assert.equal(f.value(), '');
});

test('a rerun at the held commit can recover; superseded same-SHA attempts cannot reengage it', async () => {
  assert.equal((await decideHold(b, [run(b, 'success', 2, { run_attempt: 2 }), run(b, 'failure', 2)], order)).action, 'clear');
  assert.equal((await decideHold('', [run(b, 'success', 3), run(b, 'failure', 2)], order)).value, '');
});

test('bad provenance and incomplete green jobs leave the hold set and perform no writes', async () => {
  for (const bad of [
    { event: 'pull_request' }, { head_branch: 'work/feature' }, { workflow_id: 9 },
    { repository: { full_name: 'other/repo' } }, { head_repository: { full_name: 'fork/paimos' } },
    { path: '.github/workflows/forged.yml' }, { conclusion: 'unrecognized' },
  ]) {
    const f = fixture({ hold: b, runs: [run(c, 'success', 3, bad)] });
    await assert.rejects(reconcileHold(f.api, { write: true }));
    assert.equal(f.value(), b); assert.deepEqual(changes(f), []);
  }
  for (const jobs of [greenJobs.slice(1), greenJobs.map(j => j.name === 'web' ? { ...j, conclusion: 'skipped' } : j), greenJobs.filter(j => j.name !== 'go-test (7)')]) {
    const f = fixture({ hold: b, runs: [run(c, 'success', 3)], jobs });
    await assert.rejects(reconcileHold(f.api, { write: true }));
    assert.equal(f.value(), b); assert.deepEqual(changes(f), []);
  }
});

test('API/write/alert failure never reports success; failed alert delivery retains the brake', async () => {
  for (const options of [{ fail: (method, path) => method === 'POST' && path === 'actions/variables' }, { ignoreWrite: true }, { issueFail: true }]) {
    const f = fixture(options);
    await assert.rejects(reconcileHold(f.api, { write: true }));
    if (options.issueFail) assert.equal(f.value(), b);
  }
  const f = fixture({ fail: () => true });
  await assert.rejects(reconcileHold(f.api, { write: true }), /HTTP 403/);
  assert.deepEqual(changes(f), []);
});

test('late engagement is reported honestly with an injected clock', async () => {
  const f = fixture();
  const result = await reconcileHold(f.api, { write: true, now: () => new Date('2026-10-02T16:06:01Z') });
  assert.equal(result.engage_after_red_s, 361); assert.equal(result.six_minute_target_met, false);
});

test('tag/rollout live guard rejects held, malformed, unavailable and incomplete observations', async () => {
  await assertReleaseAllowed(fixture({ runs: [] }).api);
  for (const hold of [a, 'broken']) await assert.rejects(assertReleaseAllowed(fixture({ hold }).api));
  await assert.rejects(assertReleaseAllowed(fixture({ fail: () => true }).api));
  for (const data of [{ total_count: 31, variables: [] }, { total_count: 1, variables: [] }, {}]) {
    await assert.rejects(readHold(async () => ({ status: 200, data })));
  }
  await assert.rejects(readHold(async () => ({ status: 404, data: null })));
});

test('bounded transport refuses redirects, oversized responses and redacts upstream errors', async () => {
  let observed;
  const api = github('fixture-only', { fetcher: async (_url, options) => { observed = options;
    return new Response(JSON.stringify({ total_count: 0, variables: [] })); } });
  await assertReleaseAllowed(api);
  assert.equal(observed.redirect, 'error'); assert.ok(observed.signal);
  await assert.rejects(github('fixture-only', { fetcher: async () => { throw new Error('private-body'); } })('GET', `${root}actions/variables`), error => !error.message.includes('private-body'));
  await assert.rejects(github('fixture-only', { fetcher: async () => new Response('x', { headers: { 'content-length': '2097153' } }) })('GET', `${root}actions/variables`));
  await assert.rejects(api('GET', '/repos/other/repo/actions/variables'));
});
