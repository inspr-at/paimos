// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { checkMigrations, destructive, loadMigrationExceptions } from '../check-migrations.mjs';

for (const [file, ticket] of [['1230_work_leaf_aggregates.sql', 'AEON-651']]) test(`${ticket} inherited contract evidence pins source provenance and retains rollout gates`, () => {
  const manifest = loadMigrationExceptions();
  const entry = manifest.exceptions.find(entry => entry.file === file);
  assert.ok(entry, `${file}: missing exact-byte contract evidence`);
  assert.equal(entry.ticket, ticket);
  assert.match(entry.sourceCommit, /^[a-f0-9]{40}$/);
  const source = readFileSync(new URL('../../internal/db/migrations/' + file, import.meta.url), 'utf8');
  assert.equal(entry.sha256, createHash('sha256').update(source).digest('hex'));
  assert.equal(execFileSync('git', ['show', `${entry.sourceCommit}:internal/db/migrations/${file}`], {encoding: 'utf8'}), source);
  assert.equal(destructive(source), true);
  assert.match(entry.reason, /AEON-648.*Q17/);
  assert.match(entry.reason, /coordinator consolidated review before merge\/release/);
  assert.match(entry.reason, /instance-specific verified backup\/restore/);
  assert.match(entry.reason, /does not waive previous-binary compatibility/);
  assert.match(entry.reason, /No coordinator byte approval, production backup, push or deployment is claimed/);
  const exceptions = {schema: manifest.schema, exceptions: [entry]};
  assert.deepEqual(checkMigrations(new Map([[file, source]]), new Map(), null, {exceptions}), []);
  assert.match(checkMigrations(new Map([[file, source]]), new Map(), null).join('\n'), /: non-allowlisted/);
  assert.match(checkMigrations(new Map([[file, source + '\n-- changed']]), new Map(), null, {exceptions}).join('\n'), /: exception migration changed/);
  assert.match(checkMigrations(new Map(), new Map(), null, {exceptions}).join('\n'), /: exception migration removed/);
});

