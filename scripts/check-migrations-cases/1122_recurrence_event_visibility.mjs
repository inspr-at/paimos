// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, loadMigrationExceptions } from '../check-migrations.mjs';

test('recurrence visibility expands only the project event domain with pinned bytes', () => {
  const original = readFileSync(new URL('../../internal/db/migrations/1087_briefing_autopilot_visibility.sql', import.meta.url), 'utf8');
  const name = '1122_recurrence_event_visibility.sql';
  const expanded = readFileSync(new URL(`../../internal/db/migrations/${name}`, import.meta.url), 'utf8');
  const condition = sql => sql.slice(sql.indexOf('USING ((SELECT aeon_visible_all())')).replace(/\s+/g, ' ').trim();
  assert.equal(expanded.split(", 'recurrence'").length, 2);
  assert.equal(condition(expanded.replace(", 'recurrence'", '')), condition(original));
  const exceptions = loadMigrationExceptions();
  const entry = exceptions.exceptions.find(e => e.file === name);
  assert.equal(entry.ticket, 'AEON-573');
  assert.equal(entry.sha256, createHash('sha256').update(expanded).digest('hex'));
  assert.match(entry.reason, /coordinator review/);
  const files = new Map(exceptions.exceptions.map(e => [e.file, readFileSync(new URL(`../../internal/db/migrations/${e.file}`, import.meta.url), 'utf8')]));
  files.set(name, expanded.replace("'profile', 'recurrence'", "'profile', 'recurrence', 'run'"));
  assert.match(checkMigrations(files, new Map(), null, {exceptions}).join('\n'), /1122_recurrence_event_visibility.sql: exception migration changed/);
});

