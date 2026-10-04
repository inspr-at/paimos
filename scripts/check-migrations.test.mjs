// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { checkMigrations, contractMarker, destructive, publishedMigrations, splitSQL } from './check-migrations.mjs';

const marker = '-- aeon:contract-phase AEON-415 expanded-in=v260930115354.0.0 expansion-migration=0001_tenants.sql\n';

test('duplicate numbers fail even with different names and SQL', () => {
  const problems = checkMigrations(new Map([['1050_first.sql', 'SELECT 1;'], ['1050_second.sql', 'SELECT 2;']]));
  assert.match(problems.join('\n'), /duplicate migration number 1050/);
});

test('AEON-613 quota migrations coexist with the AEON-615 and lead block reservations', () => {
  const directory = new URL('../internal/db/migrations/', import.meta.url);
  const names = readdirSync(directory).filter(name => /_quota_warning(?:s|_observations)\.sql$/.test(name)).sort();
  assert.equal(names.length, 2);
  assert.match(names[0], /_quota_warnings\.sql$/);
  assert.match(names[1], /_quota_warning_observations\.sql$/);
  assert.ok(names[0] > '1135_account_readiness.sql', 'quota tables follow their readiness foreign key target');
  const files = new Map(names.map(name => [name, readFileSync(new URL(name, directory), 'utf8')]));
  // 1136/1137 belong to AEON-615; the lead reserved the whole 1139–1146
  // block for other workers. Validate a combined candidate, since these
  // branch-only migrations previously passed the isolated-tree guard.
  for (const number of [1136, 1137, 1138, 1139, 1140, 1141, 1142, 1143, 1144, 1145, 1146]) {
    files.set(`${number}_reserved_other_ticket.sql`, `CREATE TABLE reserved_${number}(id uuid);`);
  }
  assert.deepEqual(checkMigrations(files), []);
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
    '/* DROP TABLE nodes; */',
    'CREATE TABLE "DROP TABLE nodes" (id text);',
  ]) assert.equal(destructive(sql), false, sql);
});

test('SQL exposed by the runner after nested-looking comments requires contract evidence', () => {
  for (const sql of [
    'ALTER TABLE nodes ADD COLUMN extra text; /* a /* b */ ALTER TABLE nodes ALTER COLUMN title TYPE text; -- */',
    'ALTER TABLE nodes ADD COLUMN extra text; /* a /* b */ DROP TABLE nodes; -- */',
    '/* DROP TABLE nodes; /* nested */ ALTER TABLE nodes RENAME TO x; */',
    'DR/**/OP TABLE nodes;',
    "INSERT INTO notes(body) VALUES (E'escaped \\' DROP TABLE nodes;');",
  ]) {
    assert.equal(destructive(sql), true, sql);
    assert.match(checkMigrations(new Map([['1050_contract.sql', sql]])).join('\n'), /contract-phase/);
  }
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
    "CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS U&'BEGIN EX\\0045CUTE ''DROP TABLE nodes''; END';",
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

test('new filenames require contract evidence even below the highest published number', () => {
  const published = new Map([['1043_released.sql', 'DROP TABLE obsolete;']]);
  for (const name of ['0000_new.sql', '1040_new.sql', '1041_new.sql', '1042_new.sql', '1044_new.sql']) {
    for (const sql of ['DO $$ BEGIN END $$;', 'DROP TABLE nodes;', 'DELETE FROM nodes;', 'ALTER TABLE nodes ALTER COLUMN title TYPE text;']) {
      assert.match(checkMigrations(new Map([...published, [name, sql]]), published).join('\n'), /contract-phase/, name);
      assert.deepEqual(checkMigrations(new Map([...published, [name, marker + sql]]), published), []);
    }
    assert.deepEqual(checkMigrations(new Map([...published, [name, 'CREATE TABLE extra(id text);']]), published), []);
  }
});

test('pre-policy SQL is grandfathered by exact content, never by its name alone', () => {
  const name = '1047_existing.sql', sql = 'DO $$ BEGIN END $$;';
  const baseline = {grandfatheredFiles: {[name]: createHash('sha256').update(sql).digest('hex')}};
  assert.deepEqual(checkMigrations(new Map([[name, sql]]), new Map(), null, {baseline}), []);
  assert.match(checkMigrations(new Map([[name, sql + ' DROP TABLE nodes;']]), new Map(), null, {baseline}).join('\n'), /pre-policy migration changed/);
  assert.match(checkMigrations(new Map([['1048_new.sql', sql]]), new Map(), null, {baseline}).join('\n'), /contract-phase/);
  assert.match(checkMigrations(new Map([[name, marker + 'DROP TABLE nodes;']]), new Map(), null, {baseline}).join('\n'), /pre-policy migration changed/);
  assert.match(checkMigrations(new Map(), new Map(), null, {baseline}).join('\n'), /pre-policy migration removed/);
});

test('grandfathered low-numbered files are immutable and do not exempt adjacent gaps', () => {
  const name = '0001_existing.sql', sql = 'DROP TABLE obsolete;';
  const baseline = {grandfatheredFiles: {[name]: createHash('sha256').update(sql).digest('hex')}};
  const options = {baseline};
  assert.deepEqual(checkMigrations(new Map([[name, sql]]), new Map(), null, options), []);
  assert.match(checkMigrations(new Map([[name, 'CREATE TABLE extra(id text);']]), new Map(), null, options).join('\n'), /pre-policy migration changed/);
  assert.match(checkMigrations(new Map([[name, sql], ['0000_gap.sql', 'DROP TABLE nodes;']]), new Map(), null, options).join('\n'), /contract-phase/);
});

test('checked-in grandfathering pins exactly the release and pre-policy source filenames and bytes', () => {
  const baseline = JSON.parse(readFileSync(new URL('./migration-policy-baseline.json', import.meta.url), 'utf8'));
  assert.equal(execFileSync('git', ['rev-parse', `refs/tags/${baseline.releasedTag}^{commit}`], {encoding: 'utf8'}).trim(), baseline.releasedSourceCommit);
  const historical = new Map([...publishedMigrations(`refs/tags/${baseline.releasedTag}`), ...publishedMigrations(baseline.legacySourceCommit)]);
  const hashes = Object.fromEntries([...historical].sort(([a], [b]) => a.localeCompare(b)).map(([name, sql]) => [name, createHash('sha256').update(sql).digest('hex')]));
  assert.deepEqual(baseline.grandfatheredFiles, hashes);
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
  const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8');
  assert.match(workflow, /^  pull_request:/m);
  assert.match(workflow, /^  merge_group:/m);
  assert.match(workflow, /merge_group:\n    types: \[checks_requested\]/);
  assert.match(workflow, /^  migration-compat:/m);
  const migrationJob = workflow.split(/^  migration-compat:\n/m)[1].split(/^  [\w-]+:\n/m)[0];
  assert.match(migrationJob, /fetch-depth: 0/);
  assert.match(migrationJob, /node scripts\/check-migrations.mjs --base-ref/);
  assert.match(migrationJob, /go test -p 2 \.\/internal\/db -run '\^TestMigrationCheckerSplitParity\$' -count=1\s*$/m);
  // Migration compatibility remains unconditional, including its steps. Other
  // jobs, such as the PR/queue review gate, deliberately have event guards.
  assert.doesNotMatch(migrationJob, /^\s+if:/m);
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

test('a validated foreign key may include a newly added NULL column in the same ALTER', () => {
  for (const sql of [
    'ALTER TABLE intake_drafts ADD COLUMN requester_principal_id uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (tenant_id, requester_principal_id) REFERENCES principals(tenant_id, id);',
    'ALTER TABLE public.drafts ADD requester uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES public.principals(id);',
    'ALTER TABLE drafts ADD COLUMN "requester" uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
  ]) assert.equal(destructive(sql), false, sql);
  for (const sql of [
    'ALTER TABLE drafts ADD CONSTRAINT requester_fk FOREIGN KEY (tenant_id, requester) REFERENCES principals(tenant_id, id);',
    'ALTER TABLE drafts ADD COLUMN extra uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
    'ALTER TABLE drafts ADD COLUMN IF NOT EXISTS requester uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
    "ALTER TABLE drafts ADD COLUMN requester uuid DEFAULT '00000000-0000-0000-0000-000000000000', ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);",
    'ALTER TABLE drafts ADD COLUMN requester uuid DEFAULT gen_random_uuid(), ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
    'ALTER TABLE drafts ADD COLUMN requester uuid NOT NULL, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
    'ALTER TABLE drafts ADD COLUMN requester custom_uuid_domain, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
    'ALTER TABLE drafts ADD COLUMN requester uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (tenant_id, requester) REFERENCES principals(tenant_id, id) MATCH FULL;',
    'ALTER TABLE drafts ADD COLUMN requester uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id) ON DELETE CASCADE;',
    'ALTER TABLE drafts ADD COLUMN requester uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id), ALTER COLUMN title TYPE text;',
    'ALTER TABLE drafts ADD COLUMN requester uuid; ALTER TABLE drafts ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
    'ALTER TABLE drafts ADD COLUMN "Requester" uuid, ADD CONSTRAINT requester_fk FOREIGN KEY (requester) REFERENCES principals(id);',
  ]) assert.equal(destructive(sql), true, sql);
});

test('1056 is expand-safe without allowing replacements or opaque RLS blocks', () => {
  const sql = readFileSync(new URL('../internal/db/migrations/1056_intake_replacements.sql', import.meta.url), 'utf8');
  assert.equal(destructive(sql), false);
  assert.deepEqual(checkMigrations(new Map([['1056_intake_replacements.sql', sql]])), []);
  assert.equal(destructive(sql.replace('requester_principal_id uuid,', 'requester_principal_id uuid DEFAULT gen_random_uuid(),')), true);
  assert.equal(destructive('CREATE OR REPLACE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN RETURN; END; $$;'), true);
  assert.equal(destructive("CREATE TABLE extra(id text); DO $$ BEGIN EXECUTE 'ALTER TABLE extra ENABLE ROW LEVEL SECURITY'; END $$;"), true);
});

test('contract exceptions pin exact filenames and bytes with a ticket and reason', () => {
  const name = '1054_contract.sql', sql = 'CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;';
  const entry = {file: name, sha256: createHash('sha256').update(sql).digest('hex'), ticket: 'AEON-415', reason: 'Explicit contract behavior change for coordinator review.'};
  const manifest = entries => ({schema: 'aeon.migration-policy-exceptions.v1', exceptions: entries});
  const check = (files, entries = [entry]) => checkMigrations(files, new Map(), null, {exceptions: manifest(entries)});
  assert.deepEqual(check(new Map([[name, sql]])), []);
  assert.match(check(new Map([[name, sql + '\nDROP TABLE nodes;']])).join('\n'), /exception migration changed/);
  assert.match(check(new Map()).join('\n'), /exception migration removed/);
  assert.match(check(new Map([[name, sql], ['1055_adjacent.sql', sql]])).join('\n'), /1055_adjacent.sql: non-allowlisted/);
  assert.match(check(new Map([[name, sql], ['1054_duplicate.sql', 'CREATE TABLE extra(id text);']])).join('\n'), /duplicate migration number/);
  assert.match(check(new Map([[name, sql]]), [entry, entry]).join('\n'), /duplicate migration exception/);
  for (const invalid of [{file: '*.sql'}, {sha256: 'bad'}, {ticket: ''}, {reason: ''}, {reason: 42}]) {
    const problems = check(new Map([[name, sql]]), [{...entry, ...invalid}]).join('\n');
    assert.match(problems, /invalid migration exception/);
    assert.match(problems, /non-allowlisted/);
  }
  for (const exceptions of [{}, {schema: 'unknown', exceptions: [entry]}, {schema: manifest([]).schema, exceptions: {}}]) {
    assert.match(checkMigrations(new Map([[name, sql]]), new Map(), null, {exceptions}).join('\n'), /invalid migration exception manifest/);
  }
  assert.match(checkMigrations(new Map([[name, sql]]), new Map([[name, 'original']]), null, {exceptions: manifest([entry])}).join('\n'), /published migration changed/);
  const baseline = {grandfatheredFiles: {[name]: createHash('sha256').update('original').digest('hex')}};
  assert.match(checkMigrations(new Map([[name, sql]]), new Map(), null, {baseline, exceptions: manifest([entry])}).join('\n'), /pre-policy migration changed/);
});

test('integration exceptions pin the declared migration bytes', () => {
  const manifest = JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'));
  assert.equal(manifest.schema, 'aeon.migration-policy-exceptions.v1');
  assert.deepEqual(manifest.exceptions.map(entry => entry.file), ['1054_confirmed_quota_pools.sql', '1066_run_kinds.sql', '1087_briefing_autopilot_visibility.sql', '1088_more_harnesses.sql', '1100_aithema_pending_content.sql', '1104_work_kinds.sql', '1112_session_service_tiers.sql', '1114_owner_guard_access_fence.sql', '1116_owner_workstation.sql', '1117_desk_delivery.sql', '1122_recurrence_event_visibility.sql', '1138_portal_products.sql', '1141_chat_identity.sql', '1206_themes.sql', '1207_theme_principal_links.sql', '1208_theme_selection_generation.sql', '1235_agent_appearance_themes.sql']);
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
  const runKindSQL = readFileSync(new URL('../internal/db/migrations/' + runKinds.file, import.meta.url), 'utf8');
  assert.equal(runKinds.sha256, createHash('sha256').update(runKindSQL).digest('hex'));
  assert.match(runKinds.reason, /strict superset/);

  assert.equal(entry.ticket, 'AEON-397');
  assert.match(entry.reason, /contract|unconfirmed|person/i);
  const source = execFileSync('git', ['show', `${entry.sourceCommit}:internal/db/migrations/${entry.file}`], {encoding: 'utf8'});
  assert.ok(source);
  assert.equal(entry.sha256, createHash('sha256').update(source).digest('hex'));
  assert.equal(readFileSync(new URL(`../internal/db/migrations/${entry.file}`, import.meta.url), 'utf8'), source);
  assert.equal(destructive(source), true);

  const workstation = manifest.exceptions.find(entry => entry.file === '1116_owner_workstation.sql');
  assert.equal(workstation.ticket, 'AEON-580');
  assert.match(workstation.reason, /coordinator review before merge\/release/);
  const workstationSource = execFileSync('git', ['show', `${workstation.sourceCommit}:internal/db/migrations/${workstation.file}`], {encoding: 'utf8'});
  assert.equal(workstation.sha256, createHash('sha256').update(workstationSource).digest('hex'));
  assert.equal(readFileSync(new URL(`../internal/db/migrations/${workstation.file}`, import.meta.url), 'utf8'), workstationSource);
});

test('AIT-89 storage-bound relaxation has an explicit pinned coordinator-review exception', () => {
  const manifest = JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'));
  const entry = manifest.exceptions.find(entry => entry.file === '1100_aithema_pending_content.sql');
  assert.equal(entry.ticket, 'AEON-483');
  assert.match(entry.reason, /coordinator review before merge\/release/);
  assert.match(entry.reason, /does not.*skip previous-binary compatibility/);
  const source = readFileSync(new URL(`../internal/db/migrations/${entry.file}`, import.meta.url), 'utf8');
  assert.equal(entry.sha256, createHash('sha256').update(source).digest('hex'));
  assert.equal(destructive(source), true);
  assert.match(source, /octet_length\(original_bytes\) <= 1048576/);
  assert.match(source, /contract = 'aithema\.spec\.snapshot'/);
  assert.match(source, /contract = 'aithema\.journal\.record' AND kind = 'pending_op\.content'/);
  const statements = splitSQL(source);
  const drops = statements.filter(sql => /DROP CONSTRAINT/.test(sql));
  assert.deepEqual(drops, ['ALTER TABLE aithema_journal_records\n    DROP CONSTRAINT aithema_journal_records_original_bytes_check']);
  assert.equal(destructive(statements.filter(sql => !drops.includes(sql)).join(';')), false);
  assert.match(source, /\) NOT VALID;/);
  assert.doesNotMatch(source, /VALIDATE CONSTRAINT|CREATE UNIQUE INDEX/);
  assert.match(source, /total BETWEEN 1048577 AND 16777216/);
  const validation = readFileSync(new URL('../internal/db/migrations/1101_aithema_content_bytes_validate.sql', import.meta.url), 'utf8');
  assert.equal(destructive(validation), false);
  assert.match(validation, /VALIDATE CONSTRAINT aithema_journal_records_content_bytes_check/);
  const index = readFileSync(new URL('../internal/db/migrations/1102_aithema_content_address.sql', import.meta.url), 'utf8');
  assert.equal(destructive(index), false);
  assert.ok(index.startsWith('-- aeon:no-transaction\n'));
  assert.equal(splitSQL(index).length, 1);
  assert.match(index, /CREATE UNIQUE INDEX CONCURRENTLY/);

});

test('R1 chat identity has a pinned exception with bounded compatibility evidence', () => {
  const manifest = JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'));
  const name = '1141_chat_identity.sql';
  const entry = manifest.exceptions.find(entry => entry.file === name);
  assert.ok(entry);
  assert.equal(entry.ticket, 'AEON-618');
  const sql = readFileSync(new URL(`../internal/db/migrations/${name}`, import.meta.url), 'utf8');
  assert.equal(entry.sha256, createHash('sha256').update(sql).digest('hex'));
  assert.equal(destructive(sql), true);
  assert.match(entry.reason, /disabled by default/);
  assert.match(entry.reason, /alias-only backfill create no owners, bindings or deliveries/);
  assert.match(entry.reason, /not every legacy write/);
  assert.match(entry.reason, /coordinator review before merge\/release/);
  assert.match(entry.reason, /does not.*skip previous-binary compatibility/);

  // Compare the shipped SQL registry, rather than assuming a replacement is
  // additive: old permissions must survive exactly, with only chat appended.
  const previous = readFileSync(new URL('../internal/db/migrations/0841_inbox_receipt.sql', import.meta.url), 'utf8');
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

test('desk delivery relaxation is pinned and rejects altered constraint enforcement', () => {
  const file = '1117_desk_delivery.sql';
  const sql = readFileSync(new URL('../internal/db/migrations/' + file, import.meta.url), 'utf8');
  const manifest = JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'));
  const entry = manifest.exceptions.find(entry => entry.file === file);
  assert.equal(entry.ticket, 'AEON-564');
  assert.equal(entry.sha256, createHash('sha256').update(sql).digest('hex'));
  assert.match(entry.reason, /Previous-binary writers continue supplying non-null event IDs at INSERT/);
  assert.match(entry.reason, /coordinator review before merge\/release/);
  const exceptions = {schema: manifest.schema, exceptions: [entry]};
  const files = new Map([[file, sql]]);
  assert.match(checkMigrations(files).join('\n'), /1117_desk_delivery.sql: non-allowlisted/);
  assert.deepEqual(checkMigrations(files, new Map(), null, {exceptions}), []);
  for (const altered of [
    sql.replace('DEFERRABLE INITIALLY DEFERRED', 'DEFERRABLE INITIALLY IMMEDIATE'),
    sql.replace("USING ERRCODE='23502'", "USING ERRCODE='23514'"),
    sql.replace('WHERE tenant_id=$1 AND id=$2 AND sent_event_id IS NULL', 'WHERE false'),
  ]) {
    const problems = checkMigrations(new Map([[file, altered]]), new Map(), null, {exceptions});
    assert.ok(problems.includes(`${file}: exception migration changed; add a new migration instead`));
    assert.ok(problems.some(problem => problem.startsWith(`${file}: non-allowlisted`)));
  }
});

test('the current tree requires all exact-byte contract exceptions', () => {
  const directory = new URL('../internal/db/migrations/', import.meta.url);
  const files = new Map(readdirSync(directory).filter(name => name.endsWith('.sql')).map(name => [name, readFileSync(new URL(name, directory), 'utf8')]));
  const baseline = JSON.parse(readFileSync(new URL('./migration-policy-baseline.json', import.meta.url), 'utf8'));
  const exceptions = JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'));
  const published = publishedMigrations(`refs/tags/${baseline.releasedTag}`);
  assert.deepEqual(checkMigrations(files, published, baseline.releasedTag.slice(1), {baseline, exceptions}), []);
  const withoutException = checkMigrations(files, published, baseline.releasedTag.slice(1), {baseline});
  assert.deepEqual(withoutException.map(problem => problem.split(':')[0]).sort(), ['1054_confirmed_quota_pools.sql', '1066_run_kinds.sql', '1087_briefing_autopilot_visibility.sql', '1088_more_harnesses.sql', '1100_aithema_pending_content.sql', '1104_work_kinds.sql', '1112_session_service_tiers.sql', '1114_owner_guard_access_fence.sql', '1116_owner_workstation.sql', '1117_desk_delivery.sql', '1122_recurrence_event_visibility.sql', '1138_portal_products.sql', '1141_chat_identity.sql', '1206_themes.sql', '1207_theme_principal_links.sql', '1208_theme_selection_generation.sql', '1235_agent_appearance_themes.sql']);
  for (const problem of withoutException) assert.match(problem, /: non-allowlisted/);
});

test('briefing visibility expansion preserves every existing restriction', () => {
  const original = readFileSync(new URL('../internal/db/migrations/0823_event_reference_visibility.sql', import.meta.url), 'utf8');
  const expanded = readFileSync(new URL('../internal/db/migrations/1087_briefing_autopilot_visibility.sql', import.meta.url), 'utf8');
  const addition = "OR type IN ('status_autopilot.changed', 'status_autopilot.skipped')";
  assert.equal(expanded.split(addition).length, 2);
  const condition = sql => sql.slice(sql.indexOf('USING ((SELECT aeon_visible_all())')).replace(/\s+/g, ' ').trim();
  // Removing exactly the two admitted types recovers the original policy.
  assert.equal(condition(expanded.replace(addition, '')), condition(original));
  const name = '1087_briefing_autopilot_visibility.sql';
  const exceptions = JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'));
  const entry = exceptions.exceptions.find(e => e.file === name);
  assert.ok(entry);
  const files = new Map(exceptions.exceptions.map(e => [e.file, readFileSync(new URL(`../internal/db/migrations/${e.file}`, import.meta.url), 'utf8')]));
  files.set(name, expanded.replace(addition, "OR type LIKE 'status_autopilot.%'"));
  assert.match(checkMigrations(files, new Map(), null, {exceptions}).join('\n'), /1087_briefing_autopilot_visibility.sql: exception migration changed/);
});

test('recurrence visibility expands only the project event domain with pinned bytes', () => {
  const original = readFileSync(new URL('../internal/db/migrations/1087_briefing_autopilot_visibility.sql', import.meta.url), 'utf8');
  const name = '1122_recurrence_event_visibility.sql';
  const expanded = readFileSync(new URL(`../internal/db/migrations/${name}`, import.meta.url), 'utf8');
  const condition = sql => sql.slice(sql.indexOf('USING ((SELECT aeon_visible_all())')).replace(/\s+/g, ' ').trim();
  assert.equal(expanded.split(", 'recurrence'").length, 2);
  assert.equal(condition(expanded.replace(", 'recurrence'", '')), condition(original));
  const exceptions = JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'));
  const entry = exceptions.exceptions.find(e => e.file === name);
  assert.equal(entry.ticket, 'AEON-573');
  assert.equal(entry.sha256, createHash('sha256').update(expanded).digest('hex'));
  assert.match(entry.reason, /coordinator review/);
  const files = new Map(exceptions.exceptions.map(e => [e.file, readFileSync(new URL(`../internal/db/migrations/${e.file}`, import.meta.url), 'utf8')]));
  files.set(name, expanded.replace("'profile', 'recurrence'", "'profile', 'recurrence', 'run'"));
  assert.match(checkMigrations(files, new Map(), null, {exceptions}).join('\n'), /1122_recurrence_event_visibility.sql: exception migration changed/);
});

test('1088 stages every widened check before definition-selected drops', () => {
  const sql = readFileSync(new URL('../internal/db/migrations/1088_more_harnesses.sql', import.meta.url), 'utf8');
  const adds = [...sql.matchAll(/ALTER TABLE (\w+) ADD CONSTRAINT (\w+)\s+CHECK ([\s\S]*?);/g)];
  assert.equal(adds.length, 10);
  for (const [, table, constraint, check] of adds) {
    assert.match(check, /NOT VALID$/);
    const validate = `ALTER TABLE ${table} VALIDATE CONSTRAINT ${constraint};`;
    assert.ok(sql.includes(validate), `missing ${validate}`);
    assert.ok(sql.indexOf(validate) < sql.indexOf('DROP CONSTRAINT'), 'drop before validation');
  }
  assert.match(sql, /pg_get_constraintdef/);
  assert.doesNotMatch(sql, /DROP CONSTRAINT work_order_reviews_check/);
  assert.match(sql, /reviewer_profile_id IS NULL/);
  assert.match(sql, /reviewer_family IS NULL/);
});
