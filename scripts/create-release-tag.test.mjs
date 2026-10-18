// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { createReleaseTag, parseReleaseTagArgs } from './create-release-tag.mjs';

const sha = 'a'.repeat(40), version = '261002004358.0.0';
function fixture(options = {}) {
  const writes = [], gitCalls = [], checkCalls = [];
  let heads = 0, checked = false;
  const dependencies = {
    git: args => {
      gitCalls.push(args);
      if (args[0] === 'diff') { if (options.dirty) throw new Error('Dirty checkout'); return ''; }
      if (args[0] === 'tag') { writes.push(args); return ''; }
      if (args[0] === 'branch') return checked ? options.branchAfter ?? options.branch ?? 'main' : options.branch ?? 'main';
      if (args[0] === 'status') return options.untracked || checked && options.becameDirty ? '?? fixture-file' : '';
      if (args[0] === 'fetch') { if (options.fetchFailed) throw new Error('Fetch unavailable'); return ''; }
      if (args[0] === 'merge-base') { if (options.notAncestor) throw new Error('Not an ancestor'); return ''; }
      if (args[0] === 'rev-parse' && args[1] === 'HEAD') return options.changed && heads++ ? 'b'.repeat(40) : sha;
      if (args[0] === 'rev-parse') return options.existing ? 'c'.repeat(40) : '';
      throw new Error('Unexpected git operation');
    },
    check: async checkedSha => {
      checked = true;
      checkCalls.push(checkedSha);
      if (options.failed) throw new Error('No green exact-SHA main rehearsal');
      return { sha: options.wrongReceipt ? 'b'.repeat(40) : sha, run_id: 42 };
    },
  };
  return { dependencies, writes, gitCalls, checkCalls };
}
test('default preview checks the receipt and creates no tag', async () => {
  const { dependencies, writes, gitCalls, checkCalls } = fixture();
  const result = await createReleaseTag({ version }, {}, dependencies);
  assert.equal(result.mode, 'preview'); assert.deepEqual(writes, []);
  assert.deepEqual(checkCalls, [sha]);
  assert.ok(gitCalls.every(args => !['fetch', 'merge-base', 'status'].includes(args[0])));
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

for (const write of [false, true]) test(`hotfix mode proves ancestry before rehearsal and ${write ? 'tags' : 'previews'}`, async () => {
  const { dependencies, writes, gitCalls, checkCalls } = fixture({ branch: 'hotfix/132' });
  const check = dependencies.check;
  dependencies.check = checkedSha => {
    assert.deepEqual(gitCalls.slice(-2), [
      ['fetch', '--no-tags', 'origin', 'refs/heads/main:refs/remotes/origin/main'],
      ['merge-base', '--is-ancestor', sha, 'refs/remotes/origin/main'],
    ]);
    return check(checkedSha);
  };
  const result = await createReleaseTag({ version }, { write, hotfix: 'hotfix/132' }, dependencies);
  assert.equal(result.mode, write ? 'created-local-tag' : 'preview');
  assert.deepEqual(checkCalls, [sha]);
  assert.deepEqual(gitCalls.filter(args => ['fetch', 'merge-base'].includes(args[0])), [
    ['fetch', '--no-tags', 'origin', 'refs/heads/main:refs/remotes/origin/main'],
    ['merge-base', '--is-ancestor', sha, 'refs/remotes/origin/main'],
  ]);
  assert.equal(gitCalls.filter(args => args[0] === 'status').length, 2);
  assert.deepEqual(writes, write ? [['tag', '-a', `v${version}`, '-m', `Release v${version}`, sha]] : []);
});

for (const branch of ['feature/x', 'hotfix/abc', 'hotfixes/132', 'release/132', 'hotfix/12']) {
  test(`invalid hotfix branch does no work: ${branch}`, async () => {
    const { dependencies, writes, gitCalls, checkCalls } = fixture({ branch });
    await assert.rejects(createReleaseTag({ version }, { write: true, hotfix: branch }, dependencies), /Expected a hotfix branch/);
    assert.deepEqual([writes, gitCalls, checkCalls], [[], [], []]);
  });
}

test('non-string hotfix options fail before any git or rehearsal call', async () => {
  for (const hotfix of [null, ['hotfix/132']]) {
    const { dependencies, writes, gitCalls, checkCalls } = fixture({ branch: 'hotfix/132' });
    await assert.rejects(createReleaseTag({ version }, { write: true, hotfix }, dependencies), /Expected a hotfix branch/);
    assert.deepEqual([writes, gitCalls, checkCalls], [[], [], []]);
  }
});

for (const [name, options, reason, checks] of [
  ['different branch', { branch: 'hotfix/133' }, /checked out on hotfix\/132/, 0],
  ['main checkout', { branch: 'main' }, /checked out on hotfix\/132/, 0],
  ['detached checkout', { branch: '' }, /checked out on hotfix\/132/, 0],
  ['tracked changes', { dirty: true }, /Dirty checkout/, 0],
  ['untracked changes', { untracked: true }, /clean hotfix checkout/, 0],
  ['fetch failure', { fetchFailed: true }, /Current origin\/main fetch failed/, 0],
  ['not an ancestor of current main', { notAncestor: true }, /not on origin\/main/, 0],
  ['existing tag', { existing: true }, /Existing release tag is immutable/, 0],
  ['failed receipt', { failed: true }, /No green exact-SHA/, 1],
  ['wrong receipt SHA', { wrongReceipt: true }, /does not bind the checked-out commit/, 1],
  ['changed HEAD', { changed: true }, /checkout changed during the rehearsal check/, 1],
  ['changed branch', { branchAfter: 'hotfix/133' }, /checkout changed during the rehearsal check/, 1],
  ['checkout dirtied during API lookup', { becameDirty: true }, /clean hotfix checkout/, 1],
]) test(`hotfix refuses a tag: ${name}`, async () => {
  const { dependencies, writes, checkCalls } = fixture({ branch: 'hotfix/132', ...options });
  await assert.rejects(createReleaseTag({ version }, { write: true, hotfix: 'hotfix/132' }, dependencies), reason);
  assert.deepEqual(writes, []);
  assert.equal(checkCalls.length, checks);
});

test('a hotfix checkout still requires the explicit mode', async () => {
  const { dependencies, writes } = fixture({ branch: 'hotfix/132' });
  await assert.rejects(createReleaseTag({ version }, { write: true }, dependencies), /checked out on main/);
  assert.deepEqual(writes, []);
});

test('CLI retains preview and write defaults and accepts an explicit hotfix in either order', () => {
  assert.deepEqual(parseReleaseTagArgs([]), { write: false });
  assert.deepEqual(parseReleaseTagArgs(['--write']), { write: true });
  assert.deepEqual(parseReleaseTagArgs(['--hotfix', 'hotfix/132']), { write: false, hotfix: 'hotfix/132' });
  for (const args of [['--hotfix', 'hotfix/132', '--write'], ['--write', '--hotfix', 'hotfix/132']]) {
    assert.deepEqual(parseReleaseTagArgs(args), { write: true, hotfix: 'hotfix/132' });
  }
  for (const args of [['--hotfix'], ['--hotfix', 'feature/x'], ['--hotfix', 'hotfix/abc'], ['--write', '--write'],
    ['--hotfix', 'hotfix/132', '--hotfix', 'hotfix/133'], ['--unknown'], ['hotfix/132']]) {
    assert.throws(() => parseReleaseTagArgs(args), /Usage: create-release-tag/);
  }
});
