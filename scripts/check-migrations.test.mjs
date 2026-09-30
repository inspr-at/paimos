// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import test from 'node:test';
import { checkMigrations, contractMarker, destructive } from './check-migrations.mjs';

const marker = '-- aeon:contract-phase AEON-415 expanded-in=v260930115354.0.0\n';

test('duplicate numbers fail even with different names and SQL', () => {
  const problems = checkMigrations(new Map([['1050_first.sql', 'SELECT 1;'], ['1050_second.sql', 'SELECT 2;']]));
  assert.match(problems.join('\n'), /duplicate migration number 1050/);
});

test('drops and renames require a header contract marker', () => {
  for (const sql of [
    'DROP TABLE nodes;', 'DROP TABLE IF EXISTS public.nodes CASCADE;',
    'ALTER TABLE nodes DROP COLUMN title;', 'ALTER TABLE nodes DROP title;',
    'ALTER TABLE ONLY public.nodes DROP IF EXISTS title;',
    'ALTER TABLE nodes ADD COLUMN extra text, DROP COLUMN "Title";',
    'ALTER TABLE nodes RENAME COLUMN title TO label;',
    'ALTER TABLE nodes RENAME title TO label;', 'ALTER TABLE nodes RENAME TO items;',
    'DO $body$ BEGIN ALTER TABLE nodes DROP COLUMN title; END $body$;',
    "DO $$ BEGIN EXECUTE 'ALTER TABLE nodes DROP COLUMN title'; END $$;",
    "DO $$ BEGIN EXECUTE format('DROP TABLE %I', 'nodes'); END $$;",
    'ALTER/* comment */TABLE "nodes" RENAME COLUMN "title" TO "label";',
  ]) {
    assert.equal(destructive(sql), true, sql);
    assert.equal(checkMigrations(new Map([['1050_contract.sql', sql]])).length, 1, sql);
    assert.deepEqual(checkMigrations(new Map([['1050_contract.sql', marker + sql]])), []);
  }
});

test('data, comments, literals and constraint changes do not count as dropped columns', () => {
  for (const sql of [
    "INSERT INTO notes (body) VALUES ('DROP TABLE nodes;');",
    '-- ALTER TABLE nodes DROP COLUMN title;\nSELECT 1;',
    '/* DROP TABLE nodes; /* nested */ ALTER TABLE nodes RENAME TO x; */ SELECT 1;',
    'ALTER TABLE nodes DROP CONSTRAINT old_check;',
    'ALTER TABLE nodes ALTER COLUMN title DROP NOT NULL;',
    'ALTER TABLE nodes ALTER COLUMN title DROP DEFAULT;',
    'DROP INDEX old_index;', 'ALTER TABLE nodes RENAME CONSTRAINT old_check TO new_check;',
    'CREATE TABLE "DROP TABLE nodes" (id text);',
    "INSERT INTO notes(body) VALUES (E'escaped \\' DROP TABLE nodes;');",
  ]) assert.equal(destructive(sql), false, sql);
});

test('marker must name an expansion release and ticket before SQL', () => {
  assert.equal(contractMarker(marker + 'DROP TABLE nodes;'), true);
  for (const sql of [
    '-- aeon:contract-phase\nDROP TABLE nodes;',
    '-- aeon:contract-phase AEON-415 expanded-in=latest\nDROP TABLE nodes;',
    'SELECT 1;\n' + marker, "SELECT '" + marker + "';",
  ]) assert.equal(contractMarker(sql), false);
  assert.equal(contractMarker('-- aeon:contract-phase AEON-415 expanded-in=v260229115354.0.0\nDROP TABLE nodes;'), false);
  assert.equal(contractMarker(marker, '260929115354.0.0'), false);
  assert.equal(contractMarker(marker, '260930115354.0.0'), true);
});

test('published migrations are immutable and retain historical SQL without new markers', () => {
  const published = new Map([['0771_historical.sql', 'ALTER TABLE tokens DROP COLUMN token;']]);
  assert.deepEqual(checkMigrations(new Map(published), published), []);
  assert.match(checkMigrations(new Map([['0771_historical.sql', 'SELECT 1;']]), published).join('\n'), /published migration changed/);
  assert.match(checkMigrations(new Map(), published).join('\n'), /published migration removed/);
});

test('candidate migration filename format fails closed', () => {
  assert.match(checkMigrations(new Map([['migration.sql', 'SELECT 1;']])).join('\n'), /expected NNNN_name.sql/);
});
