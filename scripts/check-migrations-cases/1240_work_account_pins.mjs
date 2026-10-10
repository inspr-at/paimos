// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { checkMigrations, loadMigrationExceptions } from '../check-migrations.mjs';

test('canonical work pin expansion preserves every existing guard and pins exact bytes', () => {
  const original = readFileSync(new URL('../../internal/db/migrations/1023_group_schedule_and_pins.sql', import.meta.url), 'utf8');
  const name = '1240_work_account_pins.sql';
  const expanded = readFileSync(new URL(`../../internal/db/migrations/${name}`, import.meta.url), 'utf8');
  const before = original.slice(original.indexOf('CREATE FUNCTION aeon_account_ticket_pin()'), original.indexOf('CREATE TRIGGER account_ticket_pins_guard')).trim();
  const after = expanded.slice(expanded.indexOf('CREATE OR REPLACE FUNCTION aeon_account_ticket_pin()')).trim();
  assert.equal(after.split("k.slug IN ('work', 'ticket')").length, 2);
  assert.equal(after.replace('CREATE OR REPLACE FUNCTION', 'CREATE FUNCTION').replace("k.slug IN ('work', 'ticket')", "k.slug = 'ticket'"), before);
  const exceptions = loadMigrationExceptions();
  const entry = exceptions.exceptions.find(e => e.file === name);
  assert.equal(entry.ticket, 'AEON-648');
  assert.equal(entry.sha256, createHash('sha256').update(expanded).digest('hex'));
  assert.match(entry.reason, /coordinator review/);
  assert.match(checkMigrations(new Map([[name, expanded.replace("a.harness = NEW.harness", 'true')]]), new Map(), null, { exceptions }).join('\n'), /1240_work_account_pins.sql: exception migration changed/);
});

