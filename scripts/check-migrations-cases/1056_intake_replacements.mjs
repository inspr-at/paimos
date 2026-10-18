// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, destructive } from '../check-migrations.mjs';

test('1056 is expand-safe without allowing replacements or opaque RLS blocks', () => {
  const sql = readFileSync(new URL('../../internal/db/migrations/1056_intake_replacements.sql', import.meta.url), 'utf8');
  assert.equal(destructive(sql), false);
  assert.deepEqual(checkMigrations(new Map([['1056_intake_replacements.sql', sql]])), []);
  assert.equal(destructive(sql.replace('requester_principal_id uuid,', 'requester_principal_id uuid DEFAULT gen_random_uuid(),')), true);
  assert.equal(destructive('CREATE OR REPLACE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN RETURN; END; $$;'), true);
  assert.equal(destructive("CREATE TABLE extra(id text); DO $$ BEGIN EXECUTE 'ALTER TABLE extra ENABLE ROW LEVEL SECURITY'; END $$;"), true);
});

