// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, loadMigrationExceptions, splitSQL } from '../check-migrations.mjs';

const read = name => readFileSync(new URL(`../../internal/db/migrations/${name}`, import.meta.url), 'utf8');
const keys = sql => [...new RegExp('CHECK\\(step_key IN \\(([^)]*)\\)\\)').exec(sql)[1].matchAll(/'([a-z_]+)'/g)].map(m => m[1]);

test('the widened step_key check keeps every released key and adds only rehearsal and catalogue', () => {
  const name = '1305_delivery_flow_step_keys.sql';
  const sql = read(name);
  const before = keys(read('1298_delivery_flow.sql'));
  const after = keys(sql);
  assert.equal(before.length, 23, 'the released list has 23 keys');
  // A previous binary writes only the old keys: all of them must still pass, in the same order, with nothing renamed.
  assert.deepEqual(after.slice(0, before.length), before);
  assert.deepEqual(after.slice(before.length), ['rehearsal', 'catalogue']);
  assert.equal(new Set(after).size, after.length, 'no duplicate key');

  // One drop and one add of the same named constraint, nothing else: the replacement is atomic and leaves no window without a check.
  const statements = splitSQL(sql);
  assert.equal(statements.length, 3);
  assert.equal(statements[0], "SET LOCAL lock_timeout = '5s'");
  assert.equal(statements[1], 'ALTER TABLE delivery_flow_steps DROP CONSTRAINT delivery_flow_steps_step_key_check');
  assert.match(statements[2], /^ALTER TABLE delivery_flow_steps ADD CONSTRAINT delivery_flow_steps_step_key_check\s+CHECK\(step_key IN \(/);

  const manifest = loadMigrationExceptions();
  const entry = manifest.exceptions.find(item => item.file === name);
  assert.equal(entry.ticket, 'AEON-1022');
  assert.equal(entry.sha256, createHash('sha256').update(sql).digest('hex'));
  assert.match(entry.reason, /strict superset/);
  assert.match(entry.reason, /previous-binary compatibility gate/);
  const exceptions = { schema: manifest.schema, exceptions: [entry] };
  const files = new Map([[name, sql]]);
  assert.deepEqual(checkMigrations(files, new Map(), null, { exceptions }), []);
  assert.deepEqual(checkMigrations(files).map(problem => problem.split(':')[0]), [name], 'without the exception the drop is contract-phase');
  for (const changed of [sql + '\n', sql.replace("'catalogue'", "'catalogue','x'")]) {
    assert.match(checkMigrations(new Map([[name, changed]]), new Map(), null, { exceptions }).join('\n'), /1305_delivery_flow_step_keys.sql: exception migration changed/);
  }
});

test('the release record columns are expand-safe: nullable, no exception needed', () => {
  const name = '1304_delivery_flow_release_record.sql';
  const sql = read(name);
  assert.deepEqual(checkMigrations(new Map([[name, sql]])), []);
  assert.ok(!/NOT NULL|DEFAULT/i.test(sql.replace(/^--.*$/gm, '')), 'older writers never set these columns');
});
