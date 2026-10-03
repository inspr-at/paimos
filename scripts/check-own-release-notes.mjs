// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { validCalendarVersion } from './verify-release.mjs';

// AEON-405: release 113 is the last legacy backfill. Keep this cutoff pinned;
// every later reservation requires complete metadata and bilingual notes.
const LEGACY_RELEASE_SEQUENCE_CUTOFF = 113;

// The bundle is keyed by version; version.json binds that coordinate to the
// PR's channel and sequence. Empty public captures are valid internal releases.
export function checkOwnReleaseNotes(root = resolve(dirname(fileURLToPath(import.meta.url)), '..')) {
  const fail = why => { throw new Error(`own release notes: ${why}`); };
  const version = JSON.parse(readFileSync(join(root, 'version.json'), 'utf8'));
  const bundle = JSON.parse(readFileSync(join(root, 'internal/releasehistory/data/product-notes.json'), 'utf8'));
  if (version.product !== 'PAIMOS AEON' || !validCalendarVersion(version.version) || !Number.isInteger(version.release_sequence) || version.release_sequence < 1 || !version.release_channel) fail('invalid version.json reservation');
  if (bundle.schema !== 'aeon.product-release-notes.v1' || bundle.product !== version.product || !['inspr-at/aeon', 'inspr-at/paimos'].includes(bundle.repository)) fail('invalid product bundle');
  const notes = bundle.releases?.[version.version];
  if (!notes) fail(`release ${version.release_sequence} (${version.version}) has no frozen entry in product-notes.json; export and pack its own notes before the PR`);
  if (!Array.isArray(notes.items) || !/^[a-f0-9]{64}$/.test(notes.snapshot_sha256) || !Number.isFinite(Date.parse(notes.captured_at)) || !Number.isInteger(notes.release_revision) || notes.release_revision < 1) fail('invalid capture provenance');
  const legacy = version.release_sequence <= LEGACY_RELEASE_SEQUENCE_CUTOFF;
  for (const field of ['release_sequence', 'release_channel']) {
    if ((!legacy || Object.hasOwn(notes, field)) && notes[field] !== version[field]) fail('capture channel/sequence differs from version.json');
  }
  // Legacy fields are checked when present. From sequence 114, all EN/DE text
  // is required; written_after_release cannot waive metadata or translations.
  const seen = new Set();
  for (const item of notes.items) {
    if (typeof item.key !== 'string' || item.key.length > 30 || !/^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*$/.test(item.key) || seen.has(item.key) || !['features', 'fixes', 'other'].includes(item.group)) fail('invalid public item');
    seen.add(item.key);
    for (const field of ['pill_en', 'pill_de', 'benefit_en', 'benefit_de']) {
      if (legacy && !Object.hasOwn(item, field)) continue;
      if (typeof item[field] !== 'string' || !item[field].trim()) fail(`${item.key}: ${field} is required`);
      if (field.startsWith('pill_') && (item[field].trim().split(/\s+/).length < 2 || item[field].trim().split(/\s+/).length > 4)) fail(`${item.key}: ${field} must contain 2–4 words`);
    }
  }
  return { version: version.version, sequence: version.release_sequence, public_items: notes.items.length };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try { console.log(JSON.stringify(checkOwnReleaseNotes())); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
