// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as reuse from './ci-tree-reuse.mjs';
const { decide, verifyRun, registry, transport, publish, cleanMergeTree, workflowPath } = reuse;

const repository = 'inspr-at/paimos';
const sha = 'a'.repeat(40);
const tree = 'b'.repeat(40);
const base = 'c'.repeat(40);
const checkoutSHA = 'd'.repeat(40);
const headTree = 'e'.repeat(40);
const jobNames = ['tree-reuse', 'go', 'go-static', 'go-timing', 'web', 'release-check', 'e2e', 'footer-ui', 'status-help-ui', 'status-autopilot-ui', 'migration-compat', 'runner-route / route', ...Array.from({ length: 7 }, (_, index) => `go-test (${index + 1})`)];

function fixture(event = 'push') {
  const run = { id: 123, repository: { id: 10, full_name: repository }, head_repository: { full_name: repository }, workflow_id: 20, path: workflowPath, status: 'completed', conclusion: 'success', run_attempt: 1, event, head_sha: sha, head_branch: 'main', pull_requests: [{ head: { sha, repo: { id: 10 } }, base: { sha: base, repo: { id: 10 } } }] };
  const workflow = { id: 20, path: workflowPath };
  const commit = { sha, tree: { sha: event === 'pull_request' ? headTree : tree } };
  const checkout = { sha: checkoutSHA, tree: { sha: tree }, parents: [{ sha: base }, { sha }] };
  const comparison = { status: 'diverged', merge_base_commit: { sha: 'f'.repeat(40) } };
  const jobs = { total_count: jobNames.length, jobs: jobNames.map(name => ({ name, status: 'completed', conclusion: 'success' })) };
  jobs.jobs[0].steps = [{ name: `Checkout tested commit ${checkoutSHA}`, status: 'completed', conclusion: 'success' }];
  const calls = [];
  const api = async path => {
    calls.push(path);
    if (path === 'actions/runs/123') return run;
    if (path === 'actions/workflows/ci.yml') return workflow;
    if (path === `git/commits/${sha}`) return commit;
    if (path === `git/commits/${checkoutSHA}`) return checkout;
    if (path === `compare/${base}...${sha}?per_page=1`) return comparison;
    if (path === 'actions/runs/123/attempts/1/jobs?per_page=100') return jobs;
    throw new Error('Unexpected fixture request');
  };
  const record = { schema: 'aeon.ci.tree-green.v1', repository, repository_id: 10, workflow_id: 20, workflow: workflowPath, run: 123, attempt: 1, sha, tree };
  if (event === 'pull_request') record.checkout = checkoutSHA;
  const mergeTree = async (baseSnapshot, headSnapshot, testedSnapshot) => {
    assert.deepEqual([baseSnapshot, headSnapshot, testedSnapshot], [base, sha, checkoutSHA]);
    calls.push('recompute clean merge');
    return tree;
  };
  return { run, workflow, commit, checkout, jobs, record, calls, api, mergeTree };
}

function check(f, overrides = {}) {
  return decide({ event: 'merge_group', tree, repository, readRecord: async () => f.record, api: f.api, mergeTree: f.mergeTree, ...overrides });
}

test('equal target and verified source trees reuse', async () => {
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

test('a behind-main PR records and reuses the tested merge tree, not its head tree', async () => {
  const f = fixture('pull_request');
  assert.notEqual(f.commit.tree.sha, f.checkout.tree.sha);
  assert.deepEqual(await verifyRun(repository, 123, f.api, f.mergeTree), f.record);
  assert.equal((await check(f)).reuse, 'tree');
  assert(f.calls.includes(`git/commits/${checkoutSHA}`));
  assert(f.calls.includes('recompute clean merge'));
  assert(!f.calls.includes(`git/commits/${sha}`));
  // Any live PR read, including merge_commit_sha, throws in this fixture.
  assert(!f.calls.some(path => path.startsWith('pulls/')));
});

test('equal head and merge trees still require the tested checkout and clean merge', async () => {
  const f = fixture('pull_request');
  f.commit.tree.sha = tree;
  assert.equal((await check(f)).reuse, 'tree');
  assert(f.calls.includes('recompute clean merge'));
});

test('verification never reads the live PR merge commit', async () => {
  const f = fixture('pull_request');
  const api = async path => {
    assert(!path.startsWith('pulls/'), 'must never read live merge_commit_sha');
    return f.api(path);
  };
  assert.equal((await check(f, { api })).reuse, 'tree');
});

test('a missing or conflicting recomputed merge fails closed', async () => {
  const f = fixture('pull_request');
  assert.equal((await check(f, { mergeTree: async () => { throw new Error('merge conflict or missing history'); } })).reuse, 'none');
  assert.equal((await check(f, { mergeTree: async () => headTree })).reuse, 'none');
});

test('clean merge proof fetches only immutable snapshots with bounded Git commands', () => {
  const calls = [];
  const execute = (command, args, options) => {
    calls.push({ command, args, options });
    return args[0] === 'merge-tree' ? `${tree}\n` : '';
  };
  assert.equal(cleanMergeTree(base, sha, checkoutSHA, { execute }), tree);
  assert.deepEqual(calls.map(call => [call.command, call.args]), [
    ['git', ['fetch', '--no-tags', '--no-write-fetch-head', '--depth=256', 'origin', base, sha, checkoutSHA]],
    ['git', ['merge-tree', '--write-tree', base, sha]],
  ]);
  assert(calls.every(call => call.options.timeout > 0 && call.options.timeout <= 5000 && call.options.maxBuffer === 65536));
  assert.throws(() => cleanMergeTree(base, sha, checkoutSHA, { execute: () => { throw new Error('conflict'); } }), /conflict/);
  assert.throws(() => cleanMergeTree(base, sha, checkoutSHA, { execute: () => 'not a clean tree' }), /not clean/);
  assert.throws(() => cleanMergeTree(base, sha, checkoutSHA, { execute: () => assert.fail('expired proof must not fetch'), deadline: 0 }), /deadline/);
});

for (const [name, mutate] of [
  ['missing PR', f => { f.run.pull_requests = []; }],
  ['wrong PR head', f => { f.run.pull_requests[0].head.sha = 'd'.repeat(40); }],
  ['foreign PR base', f => { f.run.pull_requests[0].base.repo.id = 99; }],
  ['missing checkout snapshot', f => { f.jobs.jobs[0].steps = []; }],
  ['skipped checkout snapshot', f => { f.jobs.jobs[0].steps[0].conclusion = 'skipped'; }],
  ['duplicate checkout snapshot', f => { f.jobs.jobs[0].steps.push({ ...f.jobs.jobs[0].steps[0] }); }],
  ['wrong checkout SHA', f => { f.checkout.sha = sha; }],
  ['changed run base', f => { f.run.pull_requests[0].base.sha = headTree; }],
  ['wrong checkout base', f => { f.checkout.parents[0].sha = headTree; }],
  ['wrong checkout head', f => { f.checkout.parents[1].sha = headTree; }],
  ['wrong checkout parents', f => { f.checkout.parents.pop(); }],
  ['wrong recorded checkout', f => { f.record.checkout = sha; }],
  ['wrong checkout tree', f => { f.checkout.tree.sha = headTree; }],
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
