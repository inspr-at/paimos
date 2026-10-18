// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { planRetry } from './release-retry.mjs';

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'aeon-retry-'));
  const previous = '261002110000.0.0', next = '261002120000.0.0', reservedAt = '2026-10-02T12:00:00Z';
  const version = { product: 'PAIMOS AEON', version_scheme: 'inspr-calver-3', version: previous, reserved_at: '2026-10-02T11:00:00Z', release_channel: 'stable', release_sequence: 119, codename: 'fixture name', ticket: 'AEON-532', unpublished_reservations: [] };
  const capture = { release_channel: 'stable', release_sequence: 119, snapshot_sha256: 'a'.repeat(64), captured_at: '2026-10-02T11:00:00Z', release_revision: 1, items: [] };
  const notes = { schema: 'aeon.product-release-notes.v1', product: version.product, repository: 'inspr-at/paimos', releases: { [previous]: capture } };
  mkdirSync(join(root, 'internal/releasehistory/data'), { recursive: true });
  writeFileSync(join(root, 'version.json'), JSON.stringify(version));
  writeFileSync(join(root, 'internal/releasehistory/data/product-notes.json'), JSON.stringify(notes));
  const state = { releases: [], images: [], refs: [] }, calls = [];
  const run = (program, args) => {
    calls.push([program, ...args]);
    assert.equal(program, 'gh');
    assert.ok(args.includes('GET'));
    assert.ok(args.includes('--paginate') && args.includes('--slurp'));
    return Buffer.from(JSON.stringify([args.at(-1).includes('matching-refs') ? state.refs : args.at(-1).startsWith('orgs/') ? state.images : state.releases]));
  };
  return { root, previous, next, reservedAt, version, notes, state, calls, run };
}

test('retry keeps sequence, name and exact frozen capture; preserves original history', () => {
  const f = fixture(), before = readFileSync(join(f.root, 'version.json'));
  const plan = planRetry(f.root, f.next, f.reservedAt, f.run);
  assert.deepEqual(plan.notes.releases[f.previous], f.notes.releases[f.previous]);
  assert.deepEqual(plan.notes.releases[f.next], f.notes.releases[f.previous]);
  assert.deepEqual(plan.reservation, { ...f.version, version: f.next, reserved_at: f.reservedAt, unpublished_reservations: [f.previous] });
  assert.ok(readFileSync(join(f.root, 'version.json')).equals(before));
  assert.equal(f.calls.length, 3);
});

for (const name of ['draft release', 'published release', 'published index', 'occupied new coordinate']) test(`${name} refuses reservation reuse`, () => {
  const f = fixture();
  if (name.includes('release')) f.state.releases.push({ tag_name: `v${f.previous}`, draft: name.startsWith('draft') });
  else f.state.images.push({ name: `sha256:${'a'.repeat(64)}`, metadata: { container: { tags: [name === 'published index' ? f.previous : f.next] } } });
  assert.throws(() => planRetry(f.root, f.next, f.reservedAt, f.run), /release or index exists/);
});

test('lookup failure cannot authorize retry', () => {
  const f = fixture();
  assert.throws(() => planRetry(f.root, f.next, f.reservedAt, () => { throw new Error('fixture unavailable'); }));
});

test('existing new tag and malformed registry responses cannot authorize retry', () => {
  const f = fixture();
  f.state.refs = [{ ref: `refs/tags/v${f.next}` }];
  assert.throws(() => planRetry(f.root, f.next, f.reservedAt, f.run), /already has a tag/);
  f.state.refs = []; f.state.images = [{}];
  assert.throws(() => planRetry(f.root, f.next, f.reservedAt, f.run), /invalid registry lookup/);
});

test('invalid, backward and timestamp-mismatched coordinates fail before lookup', () => {
  for (const [next, reservedAt] of [['260231120000.0.0', '2026-02-31T12:00:00Z'], ['261002100000.0.0', '2026-10-02T10:00:00Z'], ['261002120000.0.0', '2026-10-02T12:00:01Z']]) {
    const f = fixture();
    assert.throws(() => planRetry(f.root, next, reservedAt, f.run));
    assert.equal(f.calls.length, 0);
  }
});
