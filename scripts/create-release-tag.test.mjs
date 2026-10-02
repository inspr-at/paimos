// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { createReleaseTag } from './create-release-tag.mjs';

const sha = 'a'.repeat(40), version = '261002004358.0.0';
function fixture(options = {}) {
  const writes = [];
  let heads = 0;
  const dependencies = {
    git: args => {
      if (args[0] === 'diff') { if (options.dirty) throw new Error('Dirty checkout'); return ''; }
      if (args[0] === 'tag') { writes.push(args); return ''; }
      if (args[0] === 'branch') return options.branch ?? 'main';
      if (args[0] === 'rev-parse' && args[1] === 'HEAD') return options.changed && heads++ ? 'b'.repeat(40) : sha;
      if (args[0] === 'rev-parse') return options.existing ? 'c'.repeat(40) : '';
      throw new Error('Unexpected git operation');
    },
    check: async () => {
      if (options.failed) throw new Error('No green exact-SHA main rehearsal');
      return { sha: options.wrongReceipt ? 'b'.repeat(40) : sha, run_id: 42 };
    },
  };
  return { dependencies, writes };
}
test('default preview checks the receipt and creates no tag', async () => {
  const { dependencies, writes } = fixture();
  const result = await createReleaseTag({ version }, {}, dependencies);
  assert.equal(result.mode, 'preview'); assert.deepEqual(writes, []);
});
test('explicit write creates one annotated local tag at the immutable checked SHA', async () => {
  const { dependencies, writes } = fixture();
  const result = await createReleaseTag({ version }, { write: true }, dependencies);
  assert.equal(result.mode, 'created-local-tag');
  assert.deepEqual(writes, [['tag', '-a', `v${version}`, '-m', `Release v${version}`, sha]]);
});
for (const [name, options] of [['failed/pending receipt', { failed: true }], ['wrong receipt SHA', { wrongReceipt: true }],
  ['work branch', { branch: 'work/feature' }], ['detached checkout', { branch: '' }],
  ['existing tag', { existing: true }], ['changed HEAD', { changed: true }], ['uncommitted changes', { dirty: true }]]) test(`no tag write: ${name}`, async () => {
  const { dependencies, writes } = fixture(options);
  await assert.rejects(createReleaseTag({ version }, { write: true }, dependencies));
  assert.deepEqual(writes, []);
});
