// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { test } from 'node:test';
import { checkOwnReleaseNotes } from './check-own-release-notes.mjs';

function fixture(sequence) {
  const root = mkdtempSync(join(tmpdir(), 'aeon-own-notes-'));
  const version = { product: 'PAIMOS AEON', version: '260930160000.0.0', release_channel: 'stable', release_sequence: sequence };
  writeFileSync(join(root, 'version.json'), JSON.stringify(version));
  const path = join(root, 'internal/releasehistory/data');
  mkdirSync(path, { recursive: true });
  const notes = { snapshot_sha256: 'a'.repeat(64), captured_at: '2026-09-30T16:00:00Z', release_revision: 1, release_channel: 'stable', release_sequence: sequence, items: [] };
  const bundle = { schema: 'aeon.product-release-notes.v1', product: 'PAIMOS AEON', repository: 'inspr-at/paimos', releases: { [version.version]: notes } };
  const save = () => writeFileSync(join(path, 'product-notes.json'), JSON.stringify(bundle));
  save();
  return { root, version, notes, bundle, save };
}

test('the checked-in tree has valid own-release notes', () => {
  assert.doesNotThrow(() => checkOwnReleaseNotes());
});

for (const sequence of [1, 112, 113]) {
  test(`legacy sequence ${sequence} permits omissions but validates present fields`, () => {
    const { root, notes, save } = fixture(sequence);
    delete notes.release_sequence;
    delete notes.release_channel;
    notes.items.push({ key: 'AEON-405', group: 'fixes', pill_en: 'Honest release history', pill_de: 'Ehrliche Release Historie', benefit_en: 'Notes travel with this release.', benefit_de: 'Die Hinweise werden mit diesem Release ausgeliefert.' });
    save();
    assert.equal(checkOwnReleaseNotes(root).sequence, sequence);
    for (const field of ['release_sequence', 'release_channel']) {
      notes[field] = field === 'release_sequence' ? sequence + 1 : 'preview';
      save();
      assert.throws(() => checkOwnReleaseNotes(root), /channel\/sequence differs/);
      notes[field] = null;
      save();
      assert.throws(() => checkOwnReleaseNotes(root), /channel\/sequence differs/);
      delete notes[field];
    }
    for (const field of ['pill_en', 'pill_de', 'benefit_en', 'benefit_de']) {
      const original = notes.items[0][field];
      delete notes.items[0][field];
      save();
      assert.equal(checkOwnReleaseNotes(root).public_items, 1);
      notes.items[0][field] = '';
      save();
      assert.throws(() => checkOwnReleaseNotes(root), new RegExp(`${field} is required`));
      notes.items[0][field] = original;
    }
    notes.items[0].pill_de = 'Ehrlich';
    notes.written_after_release = true;
    save();
    assert.throws(() => checkOwnReleaseNotes(root), /pill_de must contain 2–4 words/);
  });
}

for (const sequence of [114, 115, 1000]) {
  test(`sequence ${sequence} requires full metadata and translations despite backfill marker`, () => {
    const { root, notes, save } = fixture(sequence);
    notes.written_after_release = true;
    delete notes.release_sequence;
    delete notes.release_channel;
    save();
    assert.throws(() => checkOwnReleaseNotes(root), /channel\/sequence differs/);
    notes.release_sequence = sequence;
    notes.release_channel = 'stable';
    notes.items.push({ key: 'AEON-405', group: 'fixes' });
    save();
    assert.throws(() => checkOwnReleaseNotes(root), /pill_en is required/);
  });
}

test('the PR must contain its own sequence, not just the previous release', () => {
  const { root, version, notes, bundle, save } = fixture(114);
  delete bundle.releases[version.version];
  bundle.releases['260930150000.0.0'] = notes;
  save();
  assert.throws(() => checkOwnReleaseNotes(root), /release 114.*no frozen entry/);
  bundle.releases[version.version] = notes;
  save();
  assert.equal(checkOwnReleaseNotes(root).sequence, 114);
  notes.release_sequence = 113;
  save();
  assert.throws(() => checkOwnReleaseNotes(root), /channel\/sequence differs/);
  notes.release_sequence = 114;
  notes.items.push({ key: 'AEON-405', group: 'fixes', pill_en: 'Honest release history', pill_de: 'Ehrliche Release Historie', benefit_en: 'Notes travel with this release.', benefit_de: 'Die Hinweise werden mit diesem Release ausgeliefert.' });
  save();
  assert.equal(checkOwnReleaseNotes(root).public_items, 1);
  for (const field of ['release_sequence', 'release_channel']) {
    const original = notes[field];
    delete notes[field];
    save();
    assert.throws(() => checkOwnReleaseNotes(root), /channel\/sequence differs/);
    notes[field] = original;
  }
  notes.release_channel = 'preview';
  save();
  assert.throws(() => checkOwnReleaseNotes(root), /channel\/sequence differs/);
  notes.release_channel = 'stable';
  notes.items[0].benefit_de = '';
  save();
  assert.throws(() => checkOwnReleaseNotes(root), /benefit_de is required/);
  notes.written_after_release = true;
  save();
  assert.throws(() => checkOwnReleaseNotes(root), /benefit_de is required/);
  notes.items[0].benefit_de = 'Die Hinweise werden mit diesem Release ausgeliefert.';
  notes.items[0].pill_de = 'Ehrlich';
  save();
  assert.throws(() => checkOwnReleaseNotes(root), /pill_de must contain 2–4 words/);
  notes.items[0].pill_de = 'Ehrliche Release Historie';
  save();
  assert.equal(checkOwnReleaseNotes(root).public_items, 1);
});

test('captured task keys keep identity and malformed or duplicate keys fail', () => {
  const { root, notes, save } = fixture(122);
  const item = { key: 'TSK-1', group: 'features', pill_en: 'Captured task notes', pill_de: 'Erfasste Aufgaben Hinweise', benefit_en: 'Tasks retain their actual keys.', benefit_de: 'Aufgaben behalten ihre Schlüssel.' };
  notes.items.push(item); save();
  assert.equal(checkOwnReleaseNotes(root).public_items, 1);
  for (const key of ['TSK-0', 'TSK-01', 'tsk-1', 'T-1', 'A'.repeat(11) + '-1', 'TSK-' + '1'.repeat(27)]) {
    item.key = key; save(); assert.throws(() => checkOwnReleaseNotes(root), /invalid public item/);
  }
  item.key = 'TSK-1'; notes.items.push({ ...item }); save();
  assert.throws(() => checkOwnReleaseNotes(root), /invalid public item/);
});
