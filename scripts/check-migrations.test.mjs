// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { checkMigrations, contractMarker, destructive } from './check-migrations.mjs';

const marker = '-- aeon:contract-phase AEON-415 expanded-in=v260930115354.0.0 expansion-migration=0001_tenants.sql\n';

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

test('comments and data literals cannot masquerade as executable SQL', () => {
  for (const sql of [
    "INSERT INTO notes (body) VALUES ('DROP TABLE nodes;');",
    '-- ALTER TABLE nodes DROP COLUMN title;\n',
    '/* DROP TABLE nodes; /* nested */ ALTER TABLE nodes RENAME TO x; */',
    'CREATE TABLE "DROP TABLE nodes" (id text);',
    "INSERT INTO notes(body) VALUES (E'escaped \\' DROP TABLE nodes;');",
  ]) assert.equal(destructive(sql), false, sql);
});

test('every non-allowlisted statement requires a contract marker', () => {
  for (const sql of [
    'ALTER TABLE nodes ALTER COLUMN title TYPE text;',
    'ALTER TABLE nodes ALTER COLUMN title SET DATA TYPE text;',
    'ALTER TABLE nodes ALTER COLUMN title SET NOT NULL;',
    'ALTER TABLE nodes DROP CONSTRAINT IF EXISTS old_check;',
    'ALTER TABLE nodes ALTER COLUMN title DROP DEFAULT;',
    'ALTER TABLE nodes ALTER COLUMN title DROP NOT NULL;',
    'ALTER TYPE phase RENAME VALUE \'open\' TO \'new\';',
    'DROP POLICY tenant ON nodes;', 'DROP INDEX old_index;',
    'TRUNCATE nodes;', 'DELETE FROM nodes;', 'DELETE FROM nodes WHERE archived;',
    'SELECT dangerous_function();', 'SET session_replication_role = replica;',
    'ALTER TABLE nodes ADD CONSTRAINT title_check CHECK (title <> \'\');',
    'ALTER TABLE nodes ADD COLUMN extra text NOT NULL;',
    'ALTER TABLE nodes ADD COLUMN extra text NOT NULL DEFAULT NULL;',
    'ALTER TABLE nodes ADD COLUMN extra uuid DEFAULT gen_random_uuid();',
    'ALTER TABLE nodes ADD COLUMN extra text DEFAULT current_user;',
    'ALTER TABLE nodes ADD COLUMN extra text DEFAULT (SELECT title FROM nodes LIMIT 1);',
    'ALTER TABLE nodes ADD COLUMN extra text, ALTER COLUMN title TYPE text;',
    "DO $$ DECLARE s text := 'ALTER TABLE nodes DROP COLUMN title'; BEGIN EXECUTE s; END $$;",
    'DO $$ BEGIN INSERT INTO nodes VALUES (1); END $$;',
    "CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN EXECUTE 'DROP TABLE nodes'; END $$;",
    "CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS 'BEGIN EXECUTE ''DROP TABLE nodes''; END';",
    "CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS E'BEGIN EX\\x45CUTE ''DROP TABLE nodes''; END';",
    "CREATE OR REPLACE FUNCTION f() RETURNS text LANGUAGE sql AS $$ SELECT 'x' $$;",
    'CREATE TABLE extra (id text); DROP VIEW old_view;',
    '/* unterminated', "INSERT INTO notes VALUES ('unterminated);",
  ]) assert.equal(destructive(sql), true, sql);
});

test('explicit expand-safe forms are accepted, including mixed ALTER actions', () => {
  for (const sql of [
    'CREATE TABLE extra (id text NOT NULL PRIMARY KEY);',
    'CREATE INDEX CONCURRENTLY IF NOT EXISTS extra_idx ON nodes(title);',
    'CREATE UNIQUE INDEX extra_idx ON nodes(title);',
    "CREATE TYPE phase AS ENUM ('DROP TABLE nodes', 'open');",
    "CREATE FUNCTION f() RETURNS text LANGUAGE sql AS $$ SELECT 'DROP TABLE nodes;' $$;",
    'CREATE POLICY tenant ON nodes USING (tenant_id IS NOT NULL);',
    'CREATE TRIGGER changed AFTER UPDATE ON nodes FOR EACH ROW EXECUTE FUNCTION f();',
    'ALTER TABLE nodes ADD COLUMN extra text;',
    'ALTER TABLE nodes ADD extra numeric(12, 2) DEFAULT -1.25 NOT NULL;',
    "ALTER TABLE nodes ADD COLUMN extra text NOT NULL DEFAULT ''::text CHECK (extra <> 'x');",
    'ALTER TABLE nodes ADD COLUMN a boolean DEFAULT false, ADD COLUMN b text;',
    "ALTER TABLE nodes ADD CONSTRAINT title_check CHECK (title <> 'NOT VALID') NOT VALID;",
    'ALTER TABLE nodes VALIDATE CONSTRAINT title_check;',
    "COMMENT ON COLUMN nodes.title IS 'DROP TABLE nodes;';",
    'GRANT SELECT, UPDATE ON nodes TO app;',
    "INSERT INTO notes(body) VALUES ($note$DROP TABLE nodes;$note$);",
    "UPDATE nodes SET title = 'DELETE; WHERE' WHERE title IS NULL;",
    "SET LOCAL lock_timeout = '5s';",
    'CREATE TABLE extra (id text); ALTER TABLE extra ENABLE ROW LEVEL SECURITY; ALTER TABLE extra FORCE ROW LEVEL SECURITY;',
  ]) assert.equal(destructive(sql), false, sql);
  assert.equal(destructive('ALTER TABLE nodes ENABLE ROW LEVEL SECURITY;'), true);
  assert.equal(destructive('CREATE TABLE IF NOT EXISTS nodes(id text); ALTER TABLE nodes FORCE ROW LEVEL SECURITY;'), true);
});

test('an explicit numeric baseline preserves legacy gaps but keeps published SQL immutable', () => {
  const published = new Map([['1043_released.sql', 'DROP TABLE obsolete;']]);
  assert.deepEqual(checkMigrations(new Map([...published, ['1042_legacy_gap.sql', 'DO $$ BEGIN END $$;']]), published), []);
  assert.match(checkMigrations(new Map([...published, ['1044_new.sql', 'DO $$ BEGIN END $$;']]), published).join('\n'), /contract-phase/);
});

test('pre-policy SQL is grandfathered by exact content, never by its name alone', () => {
  const name = '1047_existing.sql', sql = 'DO $$ BEGIN END $$;';
  const baseline = {releasedThrough: 1043, legacyFiles: {[name]: createHash('sha256').update(sql).digest('hex')}};
  assert.deepEqual(checkMigrations(new Map([[name, sql]]), new Map(), null, {baseline}), []);
  assert.match(checkMigrations(new Map([[name, sql + ' DROP TABLE nodes;']]), new Map(), null, {baseline}).join('\n'), /pre-policy migration changed/);
  assert.match(checkMigrations(new Map([['1048_new.sql', sql]]), new Map(), null, {baseline}).join('\n'), /contract-phase/);
});

test('contract evidence names an existing earlier tag containing the expansion migration', () => {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-415-tags-'));
  const git = (...args) => execFileSync('git', args, {cwd: dir, encoding: 'utf8'}).trim();
  git('init', '-q');
  const migrations = join(dir, 'internal/db/migrations');
  mkdirSync(migrations, {recursive: true});
  const expansion = '0001_expansion.sql';
  writeFileSync(join(migrations, expansion), 'CREATE TABLE expanded(id text);');
  git('add', '.');
  git('-c', 'user.name=Markus Barta', '-c', 'user.email=markus@barta.com', 'commit', '-qm', 'expansion fixture');
  const earlier = 'v260929115354.0.0', previous = 'v260930115354.0.0';
  git('tag', earlier); git('tag', previous);
  const good = `-- aeon:contract-phase AEON-415 expanded-in=${earlier} expansion-migration=${expansion}\nDROP TABLE obsolete;`;
  const options = {repository: dir, previousTag: previous};
  const check = sql => checkMigrations(new Map([['0002_contract.sql', sql]]), new Map(), previous.slice(1), options);
  assert.deepEqual(check(good), []);
  for (const sql of [good.replace(earlier, 'v260928115354.0.0'), good.replace(expansion, '0000_absent.sql'), good.replace(` expansion-migration=${expansion}`, '')]) {
    assert.match(check(sql).join('\n'), /expansion|release tag/);
  }
  git('tag', 'v261001115354.0.0');
  assert.match(check(good.replace(earlier, 'v261001115354.0.0')).join('\n'), /expansion|release tag/);
  // A correctly dated tag on a divergent commit is not an earlier release.
  git('checkout', '-q', '--orphan', 'divergent');
  writeFileSync(join(migrations, '0003_other.sql'), 'CREATE TABLE other(id text);');
  git('add', '.');
  git('-c', 'user.name=Markus Barta', '-c', 'user.email=markus@barta.com', 'commit', '-qm', 'divergent fixture');
  git('tag', 'v260928115354.0.0');
  assert.match(check(good.replace(earlier, 'v260928115354.0.0')).join('\n'), /expansion|release tag/);
});

test('the static guard runs on PR and merge-group checkouts with full release-tag history', () => {
  const workflow = readFileSync(new URL('../.github/workflows/migration-compat.yml', import.meta.url), 'utf8');
  assert.match(workflow, /^  pull_request:/m);
  assert.match(workflow, /^  merge_group:/m);
  assert.match(workflow, /fetch-depth: 0/);
  assert.match(workflow, /node scripts\/check-migrations.mjs --base-ref/);
  assert.doesNotMatch(workflow, /if:.*pull_request/);
});

test('the runtime probe pulls by digest and rejects a malformed digest before Docker', () => {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-415-image-'));
  const log = join(dir, 'docker.log');
  writeFileSync(join(dir, 'docker'), `#!/bin/bash\nprintf '%s\\n' "$*" >> '${log}'\nexit 19\n`, {mode: 0o700});
  writeFileSync(join(dir, 'go'), '#!/bin/bash\nexit 19\n', {mode: 0o700});
  const script = new URL('./migration-compat.sh', import.meta.url).pathname;
  const digest = 'sha256:' + 'a'.repeat(64);
  assert.throws(() => execFileSync('bash', [script, 'v260930115354.0.0', digest], {env: {...process.env, PATH: `${dir}:${process.env.PATH}`}, stdio: 'pipe'}));
  assert.match(readFileSync(log, 'utf8'), new RegExp(`pull --platform linux/amd64 ghcr.io/inspr-at/aeon@${digest}`));
  writeFileSync(log, '');
  assert.throws(() => execFileSync('bash', [script, 'v260930115354.0.0', 'sha256:bad'], {env: {...process.env, PATH: `${dir}:${process.env.PATH}`}, stdio: 'pipe'}));
  assert.equal(readFileSync(log, 'utf8'), '');
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
