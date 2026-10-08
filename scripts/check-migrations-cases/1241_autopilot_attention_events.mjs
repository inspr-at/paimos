// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, loadMigrationExceptions, splitSQL } from '../check-migrations.mjs';

test('attention event expansion retains the released visibility and cause guards with pinned bytes', () => {
  const name = '1241_autopilot_attention_events.sql';
  const sql = readFileSync(new URL(`../../internal/db/migrations/${name}`, import.meta.url), 'utf8');
  const originalPolicy = readFileSync(new URL('../../internal/db/migrations/1230_work_leaf_aggregates.sql', import.meta.url), 'utf8');
  const originalCause = readFileSync(new URL('../../internal/db/migrations/1225_work_parent_status.sql', import.meta.url), 'utf8');
  const statements = splitSQL(sql);
  assert.equal(statements.length, 3);
  assert.equal(statements[0], "SET LOCAL lock_timeout = '5s'");
  const policy = source => splitSQL(source).find(statement => statement.startsWith('ALTER POLICY events_project_visibility'));
  const cause = source => splitSQL(source).find(statement => /^CREATE (OR REPLACE )?FUNCTION aeon_work_status_cause\(\)/.test(statement));
  const policyAddition = "'status_autopilot.proposed', 'status_autopilot.attention_apply', 'status_autopilot.attention_dismiss', 'status_autopilot.attention_undone', ";
  const causeAddition = "'status_autopilot.attention_apply','status_autopilot.attention_dismiss','status_autopilot.attention_undone',";
  assert.equal(policy(sql).split(policyAddition).length, 2);
  assert.equal(policy(sql).replace(policyAddition, ''), policy(originalPolicy));
  assert.equal(cause(sql).split(causeAddition).length, 2);
  assert.equal(cause(sql).replace(causeAddition, '').replace('CREATE OR REPLACE FUNCTION', 'CREATE FUNCTION'), cause(originalCause));

  const manifest = loadMigrationExceptions();
  const entry = manifest.exceptions.find(entry => entry.file === name);
  assert.equal(entry.ticket, 'AEON-697');
  assert.equal(entry.sha256, createHash('sha256').update(sql).digest('hex'));
  assert.match(entry.reason, /coordinator consolidated review/);
  assert.match(entry.reason, /previous-binary compatibility\/backup gates/);
  const exceptions = {schema: manifest.schema, exceptions: [entry]};
  const files = new Map([[name, sql]]);
  assert.deepEqual(checkMigrations(files, new Map(), null, {exceptions}), []);
  assert.deepEqual(checkMigrations(files).map(problem => problem.split(':')[0]), [name]);
  for (const changed of [sql + '\n', sql.replace('cardinality(node_refs) = 0', 'true'), sql.replace('>=10000', '>=10001')]) {
    assert.match(checkMigrations(new Map([[name, changed]]), new Map(), null, {exceptions}).join('\n'), /1241_autopilot_attention_events.sql: exception migration changed/);
  }
  assert.match(checkMigrations(files, new Map([[name, sql + '\n']]), null, {exceptions}).join('\n'), /1241_autopilot_attention_events.sql: published migration changed/);
});
