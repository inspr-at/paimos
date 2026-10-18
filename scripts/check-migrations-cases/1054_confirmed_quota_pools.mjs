// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync } from 'node:fs';
import { destructive, loadMigrationExceptions } from '../check-migrations.mjs';

const migrationExceptionFiles = () => readdirSync(new URL('../migration-policy-exceptions/', import.meta.url)).sort().map(name => name.replace(/\.json$/, '.sql'));

test('integration exceptions pin the declared migration bytes', () => {
  const manifest = loadMigrationExceptions();
  assert.equal(manifest.schema, 'aeon.migration-policy-exceptions.v1');
  assert.deepEqual(manifest.exceptions.map(entry => entry.file), migrationExceptionFiles());
  const entry = manifest.exceptions.find(entry => entry.file === '1054_confirmed_quota_pools.sql');
  assert.ok(entry);
  assert.equal(entry.file, '1054_confirmed_quota_pools.sql');
  const runKinds = manifest.exceptions.find(entry => entry.file === '1066_run_kinds.sql');
  assert.ok(runKinds);
  assert.equal(runKinds.file, '1066_run_kinds.sql');
  assert.equal(runKinds.ticket, 'AEON-501');
  const briefing = manifest.exceptions.find(entry => entry.file === '1087_briefing_autopilot_visibility.sql');
  assert.ok(briefing);
  assert.equal(briefing.file, '1087_briefing_autopilot_visibility.sql');
  assert.equal(briefing.ticket, 'AEON-454');
  const runKindSQL = readFileSync(new URL('../../internal/db/migrations/' + runKinds.file, import.meta.url), 'utf8');
  assert.equal(runKinds.sha256, createHash('sha256').update(runKindSQL).digest('hex'));
  assert.match(runKinds.reason, /strict superset/);

  assert.equal(entry.ticket, 'AEON-397');
  assert.match(entry.reason, /contract|unconfirmed|person/i);
  const source = execFileSync('git', ['show', `${entry.sourceCommit}:internal/db/migrations/${entry.file}`], {encoding: 'utf8'});
  assert.ok(source);
  assert.equal(entry.sha256, createHash('sha256').update(source).digest('hex'));
  assert.equal(readFileSync(new URL(`../../internal/db/migrations/${entry.file}`, import.meta.url), 'utf8'), source);
  assert.equal(destructive(source), true);

  const workstation = manifest.exceptions.find(entry => entry.file === '1116_owner_workstation.sql');
  assert.equal(workstation.ticket, 'AEON-580');
  assert.match(workstation.reason, /coordinator review before merge\/release/);
  const workstationSource = execFileSync('git', ['show', `${workstation.sourceCommit}:internal/db/migrations/${workstation.file}`], {encoding: 'utf8'});
  assert.equal(workstation.sha256, createHash('sha256').update(workstationSource).digest('hex'));
  assert.equal(readFileSync(new URL(`../../internal/db/migrations/${workstation.file}`, import.meta.url), 'utf8'), workstationSource);
});

