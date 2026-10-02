// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { releaseLane } from './release-lane.mjs';
import { repository } from './release-hold.mjs';
const sha = 'a'.repeat(40);
const workflow = { id: 7, path: '.github/workflows/ci.yml' };
const run = (id, event) => ({ id, event, head_sha: sha, head_branch: 'main', run_attempt: 1,
  repository: { full_name: repository }, head_repository: { full_name: repository }, workflow_id: 7,
  path: workflow.path, status: 'completed', conclusion: 'success' });
function fixture({ lane = 'impacted', reuse = false, expire = false, missing = false, duplicate = false, queue = true, wrongSHA = false, partial = false } = {}) {
  const runs = [...(queue ? [run(2, 'merge_group')] : []), run(3, 'push')];
  return async path => {
    if (path === 'actions/workflows/ci.yml') return workflow;
    if (path.includes('/runs?')) return { total_count: partial ? 101 : runs.length, workflow_runs: runs };
    if (path.includes('/jobs?')) return { total_count: 1, jobs: [{ steps: [{ name: 'Reuse the verified successful tree', conclusion: reuse ? 'success' : 'skipped' }] }] };
    if (path.includes('/artifacts?')) {
      const id = Number(path.split('/')[2]);
      const artifact = { id: 10, name: `ci-lane-${id === 3 ? 'full' : lane}-${sha}-1`, expired: expire,
        workflow_run: { id, head_sha: wrongSHA ? 'b'.repeat(40) : sha } };
      const artifacts = missing ? [] : duplicate ? [artifact, { ...artifact, id: 11 }] : [artifact];
      return { total_count: artifacts.length, artifacts };
    }
    throw new Error('Unexpected metrics API request');
  };
}
for (const lane of ['full', 'impacted', 'tree']) test(`release records its exact queue ${lane} lane`, async () => {
  const result = await releaseLane(sha, fixture({ lane }));
  assert.equal(result.lane, lane); assert.equal(result.source_run, 2); assert.equal(result.sha, sha);
});
test('AEON-423 verified reuse takes precedence over the queue fallback metric', async () => {
  assert.equal((await releaseLane(sha, fixture({ reuse: true }))).lane, 'tree');
});
test('a release without queue evidence records its full main lane', async () => {
  assert.equal((await releaseLane(sha, fixture({ queue: false }))).lane, 'full');
});
test('missing/expired/misbound queue receipts are unknown; full main cannot mislabel the queue', async () => {
  for (const options of [{ missing: true }, { expire: true }, { wrongSHA: true }]) {
    assert.equal((await releaseLane(sha, fixture(options))).lane, 'unknown');
  }
});
test('ambiguous or incomplete lane inventories and failed reads surface as measurement errors', async () => {
  await assert.rejects(releaseLane(sha, fixture({ duplicate: true })));
  await assert.rejects(releaseLane(sha, fixture({ partial: true })));
  await assert.rejects(releaseLane(sha, async () => { throw new Error('unavailable'); }));
});
