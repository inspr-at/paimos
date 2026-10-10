// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, destructive, loadMigrationExceptions } from '../check-migrations.mjs';

test('R1 chat identity has a pinned exception with bounded compatibility evidence', () => {
  const manifest = loadMigrationExceptions();
  const name = '1141_chat_identity.sql';
  const entry = manifest.exceptions.find(entry => entry.file === name);
  assert.ok(entry);
  assert.equal(entry.ticket, 'AEON-618');
  const sql = readFileSync(new URL(`../../internal/db/migrations/${name}`, import.meta.url), 'utf8');
  assert.equal(entry.sha256, createHash('sha256').update(sql).digest('hex'));
  assert.equal(destructive(sql), true);
  assert.match(entry.reason, /disabled by default/);
  assert.match(entry.reason, /alias-only backfill create no owners, bindings or deliveries/);
  assert.match(entry.reason, /not every legacy write/);
  assert.match(entry.reason, /coordinator review before merge\/release/);
  assert.match(entry.reason, /does not.*skip previous-binary compatibility/);

  // Compare the shipped SQL registry, rather than assuming a replacement is
  // additive: old permissions must survive exactly, with only chat appended.
  const previous = readFileSync(new URL('../../internal/db/migrations/0841_inbox_receipt.sql', import.meta.url), 'utf8');
  const permissions = source => {
    const registry = source.slice(source.indexOf('FUNCTION aeon_authz_registry_permission')).split('$$;')[0];
    return [...registry.matchAll(/\('([a-z_]+)','([a-z_ ]+)'\)/g)]
      .flatMap(([, resource, actions]) => actions.split(' ').map(action => `${resource}.${action}`)).sort();
  };
  const oldPermissions = permissions(previous);
  assert.ok(oldPermissions.length > 0);
  assert.deepEqual(permissions(sql), [...oldPermissions, 'chat.read', 'chat.send', 'chat.bind', 'chat.receive'].sort());

  const exceptions = {schema: manifest.schema, exceptions: [entry]};
  const check = files => checkMigrations(files, new Map(), null, {exceptions});
  assert.deepEqual(check(new Map([[name, sql]])), []);
  assert.match(checkMigrations(new Map([[name, sql]])).join('\n'), /1141_chat_identity.sql: non-allowlisted/);
  // Even harmless byte drift invalidates the reviewed pin. The exception must
  // never extend to another file or bypass published migration immutability.
  assert.match(check(new Map([[name, sql + '\n']])).join('\n'), /1141_chat_identity.sql: exception migration changed/);
  assert.match(check(new Map()).join('\n'), /1141_chat_identity.sql: exception migration removed/);
  assert.match(check(new Map([[name, sql], ['1142_unreviewed.sql', sql]])).join('\n'), /1142_unreviewed.sql: non-allowlisted/);
  assert.match(checkMigrations(new Map([[name, sql]]), new Map([[name, sql + '\n']]), null, {exceptions}).join('\n'), /1141_chat_identity.sql: published migration changed/);
});

