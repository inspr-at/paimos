// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { test } from 'node:test';
import { checkOwnReleaseNotes } from './check-own-release-notes.mjs';

test('the PR must contain its own sequence, not just the previous release', () => {
  const root = mkdtempSync(join(tmpdir(), 'aeon-own-notes-'));
  const version = { product: 'PAIMOS AEON', version: '260930160000.0.0', release_channel: 'stable', release_sequence: 114 };
  writeFileSync(join(root, 'version.json'), JSON.stringify(version));
  const path = join(root, 'internal/releasehistory/data');
  mkdirSync(path, { recursive: true });
  const notes = { snapshot_sha256: 'a'.repeat(64), captured_at: '2026-09-30T16:00:00Z', release_revision: 1, release_channel: 'stable', release_sequence: 114, items: [] };
  const bundle = { schema: 'aeon.product-release-notes.v1', product: 'PAIMOS AEON', repository: 'inspr-at/paimos', releases: { '260930150000.0.0': notes } };
  const save = () => writeFileSync(join(path, 'product-notes.json'), JSON.stringify(bundle));
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
  notes.items[0].benefit_de = '';
  save();
  assert.throws(() => checkOwnReleaseNotes(root), /benefit_de is required/);
});
