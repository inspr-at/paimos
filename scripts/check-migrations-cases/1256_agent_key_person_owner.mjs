// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, destructive } from '../check-migrations.mjs';

test('AEON-724 person ownership migration is expand-only without a contract exception', () => {
  const name = '1256_agent_key_person_owner.sql';
  const source = readFileSync(new URL('../../internal/db/migrations/' + name, import.meta.url), 'utf8');
  assert.equal(destructive(source), false);
  assert.deepEqual(checkMigrations(new Map([[name, source]]), new Map(), null), []);
});

