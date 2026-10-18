// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync, readdirSync } from 'node:fs';
import { checkMigrations } from '../check-migrations.mjs';

test('AEON-613 quota migrations coexist with the AEON-615 and lead block reservations', () => {
  const directory = new URL('../../internal/db/migrations/', import.meta.url);
  const names = readdirSync(directory).filter(name => /_quota_warning(?:s|_observations)\.sql$/.test(name)).sort();
  assert.equal(names.length, 2);
  assert.match(names[0], /_quota_warnings\.sql$/);
  assert.match(names[1], /_quota_warning_observations\.sql$/);
  assert.ok(names[0] > '1135_account_readiness.sql', 'quota tables follow their readiness foreign key target');
  const files = new Map(names.map(name => [name, readFileSync(new URL(name, directory), 'utf8')]));
  // 1136/1137 belong to AEON-615; the lead reserved the whole 1139–1146
  // block for other workers. Validate a combined candidate, since these
  // branch-only migrations previously passed the isolated-tree guard.
  for (const number of [1136, 1137, 1138, 1139, 1140, 1141, 1142, 1143, 1144, 1145, 1146]) {
    files.set(`${number}_reserved_other_ticket.sql`, `CREATE TABLE reserved_${number}(id uuid);`);
  }
  assert.deepEqual(checkMigrations(files), []);
});

