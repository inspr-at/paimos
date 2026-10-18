// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { destructive, loadMigrationExceptions, splitSQL } from '../check-migrations.mjs';

test('AIT-89 storage-bound relaxation has an explicit pinned coordinator-review exception', () => {
  const manifest = loadMigrationExceptions();
  const entry = manifest.exceptions.find(entry => entry.file === '1100_aithema_pending_content.sql');
  assert.equal(entry.ticket, 'AEON-483');
  assert.match(entry.reason, /coordinator review before merge\/release/);
  assert.match(entry.reason, /does not.*skip previous-binary compatibility/);
  const source = readFileSync(new URL(`../../internal/db/migrations/${entry.file}`, import.meta.url), 'utf8');
  assert.equal(entry.sha256, createHash('sha256').update(source).digest('hex'));
  assert.equal(destructive(source), true);
  assert.match(source, /octet_length\(original_bytes\) <= 1048576/);
  assert.match(source, /contract = 'aithema\.spec\.snapshot'/);
  assert.match(source, /contract = 'aithema\.journal\.record' AND kind = 'pending_op\.content'/);
  const statements = splitSQL(source);
  const drops = statements.filter(sql => /DROP CONSTRAINT/.test(sql));
  assert.deepEqual(drops, ['ALTER TABLE aithema_journal_records\n    DROP CONSTRAINT aithema_journal_records_original_bytes_check']);
  assert.equal(destructive(statements.filter(sql => !drops.includes(sql)).join(';')), false);
  assert.match(source, /\) NOT VALID;/);
  assert.doesNotMatch(source, /VALIDATE CONSTRAINT|CREATE UNIQUE INDEX/);
  assert.match(source, /total BETWEEN 1048577 AND 16777216/);
  const validation = readFileSync(new URL('../../internal/db/migrations/1101_aithema_content_bytes_validate.sql', import.meta.url), 'utf8');
  assert.equal(destructive(validation), false);
  assert.match(validation, /VALIDATE CONSTRAINT aithema_journal_records_content_bytes_check/);
  const index = readFileSync(new URL('../../internal/db/migrations/1102_aithema_content_address.sql', import.meta.url), 'utf8');
  assert.equal(destructive(index), false);
  assert.ok(index.startsWith('-- aeon:no-transaction\n'));
  assert.equal(splitSQL(index).length, 1);
  assert.match(index, /CREATE UNIQUE INDEX CONCURRENTLY/);

});

