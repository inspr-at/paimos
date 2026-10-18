// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, loadMigrationExceptions } from '../check-migrations.mjs';

test('briefing visibility expansion preserves every existing restriction', () => {
  const original = readFileSync(new URL('../../internal/db/migrations/0823_event_reference_visibility.sql', import.meta.url), 'utf8');
  const expanded = readFileSync(new URL('../../internal/db/migrations/1087_briefing_autopilot_visibility.sql', import.meta.url), 'utf8');
  const addition = "OR type IN ('status_autopilot.changed', 'status_autopilot.skipped')";
  assert.equal(expanded.split(addition).length, 2);
  const condition = sql => sql.slice(sql.indexOf('USING ((SELECT aeon_visible_all())')).replace(/\s+/g, ' ').trim();
  // Removing exactly the two admitted types recovers the original policy.
  assert.equal(condition(expanded.replace(addition, '')), condition(original));
  const name = '1087_briefing_autopilot_visibility.sql';
  const exceptions = loadMigrationExceptions();
  const entry = exceptions.exceptions.find(e => e.file === name);
  assert.ok(entry);
  const files = new Map(exceptions.exceptions.map(e => [e.file, readFileSync(new URL(`../../internal/db/migrations/${e.file}`, import.meta.url), 'utf8')]));
  files.set(name, expanded.replace(addition, "OR type LIKE 'status_autopilot.%'"));
  assert.match(checkMigrations(files, new Map(), null, {exceptions}).join('\n'), /1087_briefing_autopilot_visibility.sql: exception migration changed/);
});

