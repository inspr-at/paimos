// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { checkMigrations, destructive } from './check-migrations.mjs';

test('AEON-596 schema passes the migration policy without exceptions or grandfathering', () => {
  const names = ['1150_project_delivery.sql', '1151_delivery_adoption_jobs.sql', '1152_delivery_guards.sql'];
  const files = new Map(names.map(name => [name, readFileSync(new URL(`../internal/db/migrations/${name}`, import.meta.url), 'utf8')]));
  assert.deepEqual(checkMigrations(files), []);
  for (const [name, sql] of files) assert.equal(destructive(sql), false, name);
});
