// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync, readdirSync } from 'node:fs';
import { checkMigrations, publishedMigrations } from '../check-migrations.mjs';

test('AEON-503 waiting measurement follows every release-123 migration', () => {
  const directory = new URL('../../internal/db/migrations/', import.meta.url);
  const names = readdirSync(directory).filter(name => name.endsWith('_run_waiting_measurement.sql'));
  assert.equal(names.length, 1, 'exactly one waiting measurement migration');
  const published = publishedMigrations('refs/tags/v261005070923.0.0');
  const latest = [...published.keys()].sort().at(-1);
  assert.equal(latest, '1240_work_account_pins.sql', 'release-123 fixture boundary');
  assert.ok(names[0] > latest, `${names[0]} must follow released ${latest}`);
  assert.equal(names[0], '1243_run_waiting_measurement.sql', 'coordinator reservation');
  assert.deepEqual(checkMigrations(new Map([[names[0], readFileSync(new URL(names[0], directory), 'utf8')]])), []);
});

