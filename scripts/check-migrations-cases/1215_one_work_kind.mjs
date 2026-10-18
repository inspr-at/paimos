// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { destructive, loadMigrationExceptions } from '../check-migrations.mjs';

test('AEON-649 kind retirement pins its backup-only coordinator rollout exception', () => {
  const manifest = loadMigrationExceptions();
  const entry = manifest.exceptions.find(entry => entry.file === '1215_one_work_kind.sql');
  assert.equal(entry.ticket, 'AEON-649');
  const source = readFileSync(new URL('../../internal/db/migrations/' + entry.file, import.meta.url), 'utf8');
  assert.equal(entry.sha256, createHash('sha256').update(source).digest('hex'));
  assert.equal(destructive(source), true);
  assert.match(entry.reason, /only rollback/);
  assert.match(entry.reason, /incompatible with older binaries/);
  assert.match(entry.reason, /coordinator consolidated review/);
  assert.match(entry.reason, /instance-specific verified backup\/restore/);
  assert.match(entry.reason, /No production backup, push or deployment is claimed/);
});

