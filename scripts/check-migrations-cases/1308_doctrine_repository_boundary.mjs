// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { checkMigrations, loadMigrationExceptions, splitSQL } from '../check-migrations.mjs';

test('doctrine repository expansion preserves released names and pins its review artifact', () => {
  const name = '1308_doctrine_repository_boundary.sql';
  const sql = readFileSync(new URL(`../../internal/db/migrations/${name}`, import.meta.url), 'utf8');
  const old = readFileSync(new URL('../../internal/db/migrations/1024_doctrine_proposals.sql', import.meta.url), 'utf8');
  const statements = splitSQL(sql);
  assert.equal(statements.length, 5);
  assert.equal(statements[0], "SET LOCAL lock_timeout = '5s'");
  for (const [index, table] of ['doctrine_proposals', 'doctrine_machine_pins'].entries()) {
    assert.equal(statements[1 + index * 2], `ALTER TABLE ${table} DROP CONSTRAINT ${table}_repository_check`);
    const added = statements[2 + index * 2];
    assert.ok(added.startsWith(`ALTER TABLE ${table} ADD CONSTRAINT ${table}_repository_check`));
    const pattern = new RegExp(/repository ~ '([^']+)'/.exec(added)[1]);
    const released = [...old.matchAll(/repository IN \(([^)]*)\)/g)][index][1];
    for (const [, repository] of released.matchAll(/'([^']+)'/g)) assert.ok(pattern.test(repository), 'every old writer remains accepted');
    assert.ok(pattern.test('augmentoring-team/agm-doctrine'));
    assert.ok(pattern.test('library-team/shared-doctrine'));
    for (const repository of ['https://github.com/team/repo', 'team/repo/extra', 'repo', ' team/repo']) assert.ok(!pattern.test(repository));
    assert.match(added, /split_part\(repository, '\/', 2\) NOT IN \('\.', '\.\.'\)/);
  }
  const manifest = loadMigrationExceptions();
  const entry = manifest.exceptions.find(item => item.file === name);
  assert.equal(entry.ticket, 'AEON-1043');
  assert.equal(entry.sha256, createHash('sha256').update(sql).digest('hex'));
  assert.match(entry.reason, /draft exact-byte review artifact/);
  assert.match(entry.reason, /previous-binary compatibility gate/);
  const exceptions = { schema: manifest.schema, exceptions: [entry] };
  assert.deepEqual(checkMigrations(new Map([[name, sql]]), new Map(), null, { exceptions }), []);
  assert.equal(checkMigrations(new Map([[name, sql]])).length, 1, 'unreviewed drop needs a contract phase');
  assert.match(checkMigrations(new Map([[name, sql + '\n']]), new Map(), null, { exceptions }).join('\n'), /exception migration changed/);
});
