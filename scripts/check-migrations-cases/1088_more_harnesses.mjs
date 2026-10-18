// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';

test('1088 stages every widened check before definition-selected drops', () => {
  const sql = readFileSync(new URL('../../internal/db/migrations/1088_more_harnesses.sql', import.meta.url), 'utf8');
  const adds = [...sql.matchAll(/ALTER TABLE (\w+) ADD CONSTRAINT (\w+)\s+CHECK ([\s\S]*?);/g)];
  assert.equal(adds.length, 10);
  for (const [, table, constraint, check] of adds) {
    assert.match(check, /NOT VALID$/);
    const validate = `ALTER TABLE ${table} VALIDATE CONSTRAINT ${constraint};`;
    assert.ok(sql.includes(validate), `missing ${validate}`);
    assert.ok(sql.indexOf(validate) < sql.indexOf('DROP CONSTRAINT'), 'drop before validation');
  }
  assert.match(sql, /pg_get_constraintdef/);
  assert.doesNotMatch(sql, /DROP CONSTRAINT work_order_reviews_check/);
  assert.match(sql, /reviewer_profile_id IS NULL/);
  assert.match(sql, /reviewer_family IS NULL/);
});

