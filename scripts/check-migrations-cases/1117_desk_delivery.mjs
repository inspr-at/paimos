// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, loadMigrationExceptions } from '../check-migrations.mjs';

test('desk delivery relaxation is pinned and rejects altered constraint enforcement', () => {
  const file = '1117_desk_delivery.sql';
  const sql = readFileSync(new URL('../../internal/db/migrations/' + file, import.meta.url), 'utf8');
  const manifest = loadMigrationExceptions();
  const entry = manifest.exceptions.find(entry => entry.file === file);
  assert.equal(entry.ticket, 'AEON-564');
  assert.equal(entry.sha256, createHash('sha256').update(sql).digest('hex'));
  assert.match(entry.reason, /Previous-binary writers continue supplying non-null event IDs at INSERT/);
  assert.match(entry.reason, /coordinator review before merge\/release/);
  const exceptions = {schema: manifest.schema, exceptions: [entry]};
  const files = new Map([[file, sql]]);
  assert.match(checkMigrations(files).join('\n'), /1117_desk_delivery.sql: non-allowlisted/);
  assert.deepEqual(checkMigrations(files, new Map(), null, {exceptions}), []);
  for (const altered of [
    sql.replace('DEFERRABLE INITIALLY DEFERRED', 'DEFERRABLE INITIALLY IMMEDIATE'),
    sql.replace("USING ERRCODE='23502'", "USING ERRCODE='23514'"),
    sql.replace('WHERE tenant_id=$1 AND id=$2 AND sent_event_id IS NULL', 'WHERE false'),
  ]) {
    const problems = checkMigrations(new Map([[file, altered]]), new Map(), null, {exceptions});
    assert.ok(problems.includes(`${file}: exception migration changed; add a new migration instead`));
    assert.ok(problems.some(problem => problem.startsWith(`${file}: non-allowlisted`)));
  }
});

