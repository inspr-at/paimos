// SPDX-License-Identifier: AGPL-3.0-only
// Reuse a failed, never-published reservation's frozen notes. No tags or remote writes.
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { validCalendarVersion } from './verify-release.mjs';
import { checkOwnReleaseNotes } from './check-own-release-notes.mjs';
import { command, images, releases, list, REPOSITORY } from './release-completion.mjs';

export function planRetry(root, next, reservedAt, run = command) {
  checkOwnReleaseNotes(root);
  const reservation = JSON.parse(readFileSync(join(root, 'version.json')));
  const notes = JSON.parse(readFileSync(join(root, 'internal/releasehistory/data/product-notes.json')));
  if (!validCalendarVersion(next) || next <= reservation.version) throw new Error('release retry: coordinate must be valid and later');
  const date = new Date(reservedAt);
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/.test(reservedAt ?? '') || !Number.isFinite(date.valueOf()) || date.toISOString().slice(2, 19).replaceAll(/[-T:]/g, '') + '.0.0' !== next) throw new Error('release retry: coordinate must match the UTC reservation second');
  if (reservation.unpublished_reservations?.includes(reservation.version) || Object.hasOwn(notes.releases, next)) throw new Error('release retry: reservation already retired or new notes already exist');
  const releaseRecords = releases(run);
  const imageRecords = images(run);
  for (const version of [reservation.version, next]) {
    if (releaseRecords.some(release => release.tag_name === `v${version}`) || imageRecords.some(image => image.metadata.container.tags.includes(version))) throw new Error('release retry: release or index exists; use completion, never reuse a published coordinate');
  }
  const refs = list(`repos/${REPOSITORY}/git/matching-refs/tags/v${next}`, run);
  if (refs.some(ref => typeof ref?.ref !== 'string') || refs.some(ref => ref.ref === `refs/tags/v${next}`)) throw new Error('release retry: new coordinate already has a tag or tag lookup is invalid');
  // Historical entries are retained exactly. Only the new key reuses the capture.
  notes.releases[next] = structuredClone(notes.releases[reservation.version]);
  return {
    reservation: { ...reservation, version: next, reserved_at: reservedAt,
      unpublished_reservations: [...(reservation.unpublished_reservations ?? []), reservation.version] },
    notes,
    evidence: { previous: reservation.version, version: next, release_sequence: reservation.release_sequence,
      codename: reservation.codename, snapshot_sha256: notes.releases[next].snapshot_sha256 },
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const [next, reservedAt, write] = process.argv.slice(2);
    if (!next || !reservedAt || (write && write !== '--write') || process.argv.length > 5) throw new Error('release retry: usage: VERSION UTC_RESERVED_AT [--write]');
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
    const plan = planRetry(root, next, reservedAt);
    if (write) {
      // Add the capture first: an interrupted write cannot leave version.json
      // naming a missing snapshot. Never mutate the failed tag or original entry.
      writeFileSync(join(root, 'internal/releasehistory/data/product-notes.json'), JSON.stringify(plan.notes, null, 2) + '\n');
      writeFileSync(join(root, 'version.json'), JSON.stringify(plan.reservation, null, 2) + '\n');
      checkOwnReleaseNotes(root);
    }
    console.log(JSON.stringify({ ...plan.evidence, write: write === '--write', gates: 'same notes review, required CI, rehearsal and coordinator tag approval still required' }));
  } catch (error) {
    console.error(error.message?.startsWith('release retry:') ? error.message : 'release retry: lookup or reservation validation failed');
    process.exitCode = 1;
  }
}
