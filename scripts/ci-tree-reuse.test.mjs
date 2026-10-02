// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decide, verifyRun, registry, transport, publish, workflowPath } from './ci-tree-reuse.mjs';

const repository = 'inspr-at/paimos';
const sha = 'a'.repeat(40);
const tree = 'b'.repeat(40);
const base = 'c'.repeat(40);
const jobNames = ['tree-reuse', 'go', 'go-static', 'go-timing', 'web', 'release-check', 'e2e', 'footer-ui', 'status-help-ui', 'status-autopilot-ui', 'migration-compat', 'runner-route / route', ...Array.from({ length: 7 }, (_, index) => `go-test (${index + 1})`)];

function fixture(event = 'push') {
  const run = { id: 123, repository: { id: 10, full_name: repository }, head_repository: { full_name: repository }, workflow_id: 20, path: workflowPath, status: 'completed', conclusion: 'success', run_attempt: 1, event, head_sha: sha, head_branch: 'main', pull_requests: [{ head: { sha, repo: { id: 10 } }, base: { sha: base, repo: { id: 10 } } }] };
  const workflow = { id: 20, path: workflowPath };
  const commit = { sha, tree: { sha: tree } };
  const comparison = { status: 'ahead', merge_base_commit: { sha: base } };
  const jobs = { total_count: jobNames.length, jobs: jobNames.map(name => ({ name, status: 'completed', conclusion: 'success' })) };
  const calls = [];
  const api = async path => {
    calls.push(path);
    if (path === 'actions/runs/123') return run;
    if (path === 'actions/workflows/ci.yml') return workflow;
    if (path === `git/commits/${sha}`) return commit;
    if (path === `compare/${base}...${sha}?per_page=1`) return comparison;
    if (path === 'actions/runs/123/attempts/1/jobs?per_page=100') return jobs;
    throw new Error('Unexpected fixture request');
  };
  const record = { schema: 'aeon.ci.tree-green.v1', repository, repository_id: 10, workflow_id: 20, workflow: workflowPath, run: 123, attempt: 1, sha, tree };
  return { run, workflow, commit, comparison, jobs, record, calls, api };
}

function check(f, overrides = {}) {
  return decide({ event: 'merge_group', tree, repository, readRecord: async () => f.record, api: f.api, ...overrides });
}

test('equal trees reuse even though merge commit and source commit differ', async () => {
  const f = fixture();
  const result = await check(f);
  assert.equal(result.reuse, 'tree');
  assert.equal(result.run, 123);
  assert.equal(result.tree, tree);
  assert(f.calls.includes(`git/commits/${sha}`));
  assert(f.calls.some(path => path.includes('/attempts/1/jobs')));
});

test('a differing target tree runs the full suite', async () => {
  const f = fixture();
  assert.equal((await check(f, { tree: 'd'.repeat(40) })).reuse, 'none');
  assert.equal(f.calls.length, 0);
});

for (const [name, mutate] of [
  ['wrong repository in record', f => { f.record.repository = 'other/repository'; }],
  ['wrong repository in API', f => { f.run.repository.full_name = 'other/repository'; }],
  ['fork source repository', f => { f.run.head_repository.full_name = 'other/repository'; }],
  ['wrong repository ID', f => { f.record.repository_id = 99; }],
  ['wrong workflow path', f => { f.run.path = '.github/workflows/other.yml'; }],
  ['wrong workflow ID', f => { f.run.workflow_id = 99; }],
  ['wrong workflow lookup', f => { f.workflow.path = '.github/workflows/other.yml'; }],
  ['failed run', f => { f.run.conclusion = 'failure'; }],
  ['unfinished run', f => { f.run.status = 'in_progress'; }],
  ['wrong run ID', f => { f.run.id = 999; }],
  ['tree mismatch from API', f => { f.commit.tree.sha = 'd'.repeat(40); }],
  ['source commit mismatch', f => { f.record.sha = 'd'.repeat(40); }],
  ['stale run attempt', f => { f.record.attempt = 2; }],
  ['merge group cannot mint a record', f => { f.run.event = 'merge_group'; }],
  ['manual run cannot mint a record', f => { f.run.event = 'workflow_dispatch'; }],
  ['non-main push cannot mint a record', f => { f.run.head_branch = 'work/other'; }],
  ['skipped suite job', f => { f.jobs.jobs[4].conclusion = 'skipped'; }],
  ['missing suite job', f => { f.jobs.jobs.splice(4, 1); f.jobs.total_count--; }],
  ['missing Go shard', f => { f.jobs.jobs.pop(); f.jobs.total_count--; }],
  ['incomplete pagination', f => { f.jobs.total_count++; }],
  ['oversized job list', f => { f.jobs.total_count = 101; }],
  ['invalid schema', f => { f.record.schema = 'other'; }],
  ['unsafe run number', f => { f.record.run = Number.MAX_SAFE_INTEGER + 1; }],
]) {
  test(`forged record rejected: ${name}`, async () => {
    const f = fixture(); mutate(f);
    assert.equal((await check(f)).reuse, 'none');
  });
}

test('an ancestor PR base proves synthetic merge and head trees equal', async () => {
  const f = fixture('pull_request');
  assert.equal((await check(f)).reuse, 'tree');
  assert(f.calls.includes(`compare/${base}...${sha}?per_page=1`));
});

for (const [name, mutate] of [
  ['diverged PR', f => { f.comparison.status = 'diverged'; }],
  ['different merge base', f => { f.comparison.merge_base_commit.sha = 'd'.repeat(40); }],
  ['missing PR', f => { f.run.pull_requests = []; }],
  ['wrong PR head', f => { f.run.pull_requests[0].head.sha = 'd'.repeat(40); }],
  ['foreign PR base', f => { f.run.pull_requests[0].base.repo.id = 99; }],
]) test(`${name} falls back`, async () => {
  const f = fixture('pull_request'); mutate(f);
  assert.equal((await check(f)).reuse, 'none');
});

test('kill switch and non-queue events perform no record or API reads', async () => {
  for (const options of [{ killSwitch: 'off' }, { event: 'push' }, { event: 'pull_request' }, { event: 'workflow_dispatch' }]) {
    const f = fixture();
    assert.equal((await check(f, { ...options, readRecord: () => { assert.fail('must not read a record'); } })).reuse, 'none');
    assert.equal(f.calls.length, 0);
  }
});

test('missing registry record and API failure fall back', async () => {
  assert.equal((await check(fixture(), { readRecord: async () => { throw new Error('not found'); } })).reuse, 'none');
  assert.equal((await check(fixture(), { api: async () => { throw new Error('unavailable'); } })).reuse, 'none');
});

test('registry uses isolated tree tag and round-trips the record', async () => {
  const f = fixture();
  let manifest;
  const calls = [];
  const request = async (url, options = {}) => {
    calls.push({ url, options });
    if (url.includes('/token?')) return { json: () => ({ token: 'fixture' }) };
    if (url.endsWith('/blobs/uploads/')) return { headers: new Headers({ location: '/v2/inspr-at/paimos-ci-green/blobs/uploads/id?upload=fixture' }) };
    if (options.method === 'PUT' && url.includes('/manifests/')) manifest = JSON.parse(options.body);
    return { json: () => manifest };
  };
  const store = await registry(repository, 'fixture', 'fixture', request, true);
  await store.publish(f.record);
  assert.deepEqual(await store.read(tree), f.record);
  assert(calls.some(call => call.url === `https://ghcr.io/v2/inspr-at/paimos-ci-green/manifests/tree-${tree}` && call.options.method === 'PUT'));
  assert(calls[0].url.includes('pull%2Cpush'));
  assert.equal(manifest.annotations['org.opencontainers.image.source'], `https://github.com/${repository}`);
  const reader = await registry(repository, 'fixture', 'fixture', request);
  await assert.rejects(reader.publish(f.record), /read-only/);
});

test('registry rejects an upload redirect to a foreign origin', async () => {
  const request = async url => url.includes('/token?') ? { json: () => ({ token: 'fixture' }) } : { headers: new Headers({ location: 'https://example.com/upload' }) };
  const store = await registry(repository, 'fixture', 'fixture', request, true);
  await assert.rejects(store.publish(fixture().record), /destination/);
});

test('transport bounds response bytes before JSON and refuses redirects', async () => {
  const calls = [];
  const request = transport('fixture', 'fixture', new AbortController().signal, async (url, options) => {
    calls.push(options);
    return new Response('{}');
  });
  assert.deepEqual((await request('https://api.github.com/repos/inspr-at/paimos')).json(), {});
  assert.equal(calls[0].redirect, 'error');
  await assert.rejects(request('https://example.com'), /endpoint/);
  const large = transport('fixture', 'fixture', undefined, async () => new Response('not JSON', { headers: { 'Content-Length': String(3 * 1024 * 1024) } }));
  await assert.rejects(large('https://ghcr.io/v2/'), /too large/);
  const chunked = transport('fixture', 'fixture', undefined, async () => new Response(Buffer.alloc(2 * 1024 * 1024 + 1)));
  await assert.rejects(chunked('https://ghcr.io/v2/'), /too large/);
});

test('transport forwards cancellation without sleeps or timing thresholds', async () => {
  const controller = new AbortController(); controller.abort();
  const request = transport('fixture', 'fixture', controller.signal, async (_url, options) => { assert(options.signal.aborted); throw options.signal.reason; });
  await assert.rejects(request('https://ghcr.io/v2/'));
});

test('publication writes both records and reports a partial write as failure', async () => {
  const f = fixture();
  const record = await verifyRun(repository, 123, f.api);
  const calls = [];
  const store = { publish: async value => { assert.deepEqual(value, record); calls.push('registry'); } };
  const api = async (path, options) => { calls.push(path); const status = JSON.parse(options.body); assert.equal(status.description, `tree=${tree} run=123`); assert.equal(status.context, 'ci/tree-green'); };
  assert.deepEqual(await publish(record, api, store), record);
  assert.deepEqual(calls, ['registry', `statuses/${sha}`]);
  await assert.rejects(publish(record, async () => { throw new Error('status write failed'); }, store), /status write failed/);
  let statusWritten = false;
  await assert.rejects(publish(record, async () => { statusWritten = true; }, { publish: async () => { throw new Error('registry write failed'); } }), /registry write failed/);
  assert.equal(statusWritten, false);
});
