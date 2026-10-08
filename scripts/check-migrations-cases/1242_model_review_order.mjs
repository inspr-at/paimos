// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { readdirSync } from 'node:fs';
import { publishedMigrations } from '../check-migrations.mjs';

test('AEON-633 review ordering follows the release 123 migration boundary', () => {
  const published = publishedMigrations('refs/tags/v261005070923.0.0');
  const lastPublished = Math.max(...[...published.keys()].map(name => Number(name.slice(0, 4))));
  assert.equal(lastPublished, 1240, 'fixture must retain the published release 123 boundary');
  const names = readdirSync(new URL('../../internal/db/migrations/', import.meta.url))
    .filter(name => name.endsWith('_model_review_order.sql'));
  assert.equal(names.length, 1, 'review ordering must have exactly one migration');
  assert.ok(Number(names[0].slice(0, 4)) > lastPublished,
    `${names[0]} must run after published migration ${lastPublished}`);
  assert.equal(names[0], '1242_model_review_order.sql', 'use the coordinator reservation');
});

