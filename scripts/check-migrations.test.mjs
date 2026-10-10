// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { execFileSync } from 'node:child_process';
import childProcess from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import { mkdtempSync, mkdirSync, symlinkSync, writeFileSync, readFileSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { checkMigrations, contractMarker, destructive, loadMigrationExceptions, publishedMigrations, splitSQL } from './check-migrations.mjs';

const marker = '-- aeon:contract-phase AEON-415 expanded-in=v260930115354.0.0 expansion-migration=0001_tenants.sql\n';

const migrationExceptionFiles = () => readdirSync(new URL('./migration-policy-exceptions/', import.meta.url)).sort().map(name => name.replace(/\.json$/, '.sql'));

test('AEON-698 historical migration reads preserve exact bytes with two Git launches', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-698-migration-batch-'));
  const migrations = join(directory, 'internal/db/migrations');
  mkdirSync(migrations, {recursive: true});
  const git = (...args) => execFileSync('git', args, {cwd: directory, encoding: 'utf8'}).trim();
  git('init', '-q');
  const expected = new Map(Array.from({length: 8}, (_, i) => [
    `000${i}_fixture.sql`, i === 0 ? '' : `-- Grüße ${i}\r\nSELECT 'blob 123\\n';${i % 2 ? '\n' : ''}`,
  ]));
  for (const [name, sql] of expected) writeFileSync(join(migrations, name), sql);
  writeFileSync(join(migrations, 'README.txt'), 'not SQL');
  git('add', '.');
  const tree = git('write-tree');
  const cwd = process.cwd(), original = childProcess.execFileSync, calls = [];
  try {
    process.chdir(directory);
    childProcess.execFileSync = (bin, args, options) => {
      calls.push({bin, args});
      return original(bin, args, options);
    };
    syncBuiltinESMExports();
    assert.deepEqual(publishedMigrations(tree), expected, 'empty, multibyte, CRLF and unterminated SQL stay byte-exact');
    assert.equal(calls.length, 2, 'Git launch count must not grow with the migration count');
    assert.ok(calls.every(call => call.bin === 'git'));
    assert.deepEqual(calls[1].args, ['cat-file', '--batch']);
  } finally {
    childProcess.execFileSync = original;
    syncBuiltinESMExports();
    process.chdir(cwd);
  }
});

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

test('AEON-1107 compatibility keeps latest-release reads and the below-floor activated refusal', () => {
  // Risk: once the latest release supports account-use, testing its legacy
  // refusal falsely fails compatibility. Keep its supported policy probes and
  // the below-floor refusal on separate fixtures, even for the same image.
  const source = readFileSync(new URL('./migration-compat.sh', import.meta.url), 'utf8');
  const rollbackTag = 'v261009095632.0.0';
  const rollbackDigest = 'sha256:d916ebb57249fda5f192e74b37ebd770c0eb67c26aafeb1c0045a635e8aa940c';
  const latestTag = 'v261010123154.0.0', latestDigest = 'sha256:' + 'a'.repeat(64);
  for (const sameImage of [false, true]) {
    const dir = mkdtempSync(join(tmpdir(), 'aeon-1107-compat-'));
    const scripts = join(dir, 'scripts'), bin = join(dir, 'bin'), log = join(dir, 'calls.jsonl');
    mkdirSync(scripts); mkdirSync(bin);
    const script = join(scripts, 'migration-compat.sh');
    writeFileSync(script, source);
    const fixture = `#!/usr/bin/env python3
import json, os, sys
tool = os.path.basename(sys.argv[0])
args = sys.argv[1:]
with open(os.environ['AEON_COMPAT_CALLS'], 'a') as output:
    output.write(json.dumps([tool, *args]) + '\\n')
if tool == 'docker':
    if args[:2] == ['image', 'ls']:
        print('sha256:' + ('1' if args[-1].endswith('${rollbackDigest}') else '2') * 64)
    elif args[:1] == ['port']:
        print('127.0.0.1:55433' if args[-1] == '5432/tcp' else '127.0.0.1:18080')
elif tool == 'git':
    if args != ['show', '${rollbackTag}:internal/db/visibility.go']:
        sys.exit(19)
    print('func enterTenant() {}')
elif tool == 'python3':
    if args[1] == 'account-use':
        gate = 'latest' if '--release-tag' in args else 'legacy'
        if os.environ.get('AEON_COMPAT_REFUSE_FAIL') == gate:
            sys.exit(23)
`;
    // Use the real Python interpreter in the wrapper shebang to avoid calling
    // the mocked python3 recursively. No database/container/build is started.
    const python = execFileSync('python3', ['-c', 'import sys; print(sys.executable)'], {encoding: 'utf8'}).trim();
    for (const tool of ['docker', 'go', 'python3', 'git', 'trash'])
      writeFileSync(join(bin, tool), fixture.replace('#!/usr/bin/env python3', '#!' + python), {mode: 0o700});
    const tag = sameImage ? rollbackTag : latestTag, digest = sameImage ? rollbackDigest : latestDigest;
    const options = {env: {...process.env, PATH: `${bin}:${process.env.PATH}`, AEON_COMPAT_CALLS: log, GITHUB_STEP_SUMMARY: ''}, encoding: 'utf8', timeout: 15000};
    const stdout = execFileSync('bash', [script, tag, digest], options);
    const calls = readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line));
    const probes = calls.filter(call => call[0] === 'python3' && call[1] === 'scripts/migration-compat-probe.py');
    const modes = probes.map(call => call[2]);
    assert.deepEqual(modes, ['wait-ready', 'seed', 'wait-ready', 'check', 'account-use', 'wait-ready', 'check', 'account-use']);
    const version = call => call[call.indexOf('--version') + 1];
    const checks = probes.filter(call => call[2] === 'check');
    assert.deepEqual(checks.map(version), [tag.slice(1), rollbackTag.slice(1)]);
    const accountUse = probes.filter(call => call[2] === 'account-use');
    assert.deepEqual(accountUse.map(version), [tag.slice(1), rollbackTag.slice(1)]);
    assert.equal(accountUse[0][accountUse[0].indexOf('--release-tag') + 1], tag);
    assert.ok(!accountUse[1].includes('--release-tag'), 'legacy refusal stays unconditional');
    assert.equal(version(probes.at(-1)), rollbackTag.slice(1));
    const pulls = calls.filter(call => call[0] === 'docker' && call[1] === 'pull');
    assert.deepEqual(pulls.map(call => call.at(-1)), sameImage ? [`ghcr.io/inspr-at/aeon@${rollbackDigest}`] :
      [`ghcr.io/inspr-at/aeon@${latestDigest}`, `ghcr.io/inspr-at/aeon@${rollbackDigest}`]);
    const starts = calls.filter(call => call[0] === 'docker' && call[1] === 'run' && call.includes('256m'));
    assert.deepEqual(starts.map(call => call.at(-1)), sameImage ? Array(3).fill('sha256:' + '1'.repeat(64)) :
      ['sha256:' + '2'.repeat(64), 'sha256:' + '2'.repeat(64), 'sha256:' + '1'.repeat(64)]);
    assert.deepEqual(calls.filter(call => call[0] === 'git'), [['git', 'show', `${rollbackTag}:internal/db/visibility.go`]]);
    const migration = calls.findIndex(call => call[0] === 'go');
    const candidateCheck = calls.findIndex((call, i) => i > migration && call[2] === 'check');
    const activated = calls.findIndex(call => call[2] === 'account-use');
    assert.ok(migration > 0 && candidateCheck > migration && activated > candidateCheck);
    assert.match(stdout, /Account-use rollback boundary passed/);
    for (const gate of ['latest', 'legacy']) {
      assert.throws(() => execFileSync('bash', [script, tag, digest], {
        ...options, env: {...options.env, AEON_COMPAT_REFUSE_FAIL: gate},
      }), error => error.status === 23 && !error.stdout.includes('Account-use rollback boundary passed') &&
        !error.stdout.includes('Migration compatibility passed'));
    }
  }
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

test('the current tree requires all exact-byte contract exceptions', () => {
  const directory = new URL('../internal/db/migrations/', import.meta.url);
  const files = new Map(readdirSync(directory).filter(name => name.endsWith('.sql')).map(name => [name, readFileSync(new URL(name, directory), 'utf8')]));
  const baseline = JSON.parse(readFileSync(new URL('./migration-policy-baseline.json', import.meta.url), 'utf8'));
  const exceptions = loadMigrationExceptions();
  const published = publishedMigrations(`refs/tags/${baseline.releasedTag}`);
  assert.deepEqual(checkMigrations(files, published, baseline.releasedTag.slice(1), {baseline, exceptions}), []);
  const withoutException = checkMigrations(files, published, baseline.releasedTag.slice(1), {baseline});
  assert.deepEqual(withoutException.map(problem => problem.split(':')[0]).sort(), migrationExceptionFiles());
  for (const problem of withoutException) assert.match(problem, /: non-allowlisted/);
});

// Keep CI's existing entry point; adding a migration check only adds its file.
test('AEON-985 exception files assemble deterministically and fail closed on duplicates, orphans and malformed inputs', () => {
  const sql = 'CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;';
  const entry = {file: '1001_first.sql', sha256: createHash('sha256').update(sql).digest('hex'), ticket: 'AEON-985', reason: 'Fixture contract evidence.'};
  const second = {...entry, file: '1002_second.sql'};
  const manifest = exceptions => ({schema: 'aeon.migration-policy-exceptions.v1', exceptions});
  const fixture = records => {
    const directory = mkdtempSync(join(tmpdir(), 'aeon-985-exceptions-'));
    for (const [name, value] of records) writeFileSync(join(directory, name), JSON.stringify(value));
    return directory;
  };
  const directory = fixture([['1002_second.json', manifest([second])], ['1001_first.json', manifest([entry])]]);
  const assembled = loadMigrationExceptions(directory);
  assert.deepEqual(assembled, manifest([entry, second]), 'discovery order must not alter the legacy manifest or its fields');
  const files = new Map([[entry.file, sql], [second.file, sql]]);
  assert.deepEqual(checkMigrations(files, new Map(), null, {exceptions: assembled}), []);
  assert.match(checkMigrations(new Map([[entry.file, sql]]), new Map(), null, {exceptions: assembled}).join('\n'), /1002_second.sql: exception migration removed/);
  assert.match(checkMigrations(new Map([[entry.file, sql + '\n'], [second.file, sql]]), new Map(), null, {exceptions: assembled}).join('\n'), /1001_first.sql: exception migration changed/);
  assert.throws(() => loadMigrationExceptions(fixture([['1001_first.json', manifest([entry])], ['1002_second.json', manifest([entry])]])), /duplicate migration exception/);
  assert.throws(() => loadMigrationExceptions(fixture([['1001_first.json', manifest([second])]])), /matching migration filename/);
  assert.throws(() => loadMigrationExceptions(fixture([['unexpected.json', manifest([entry])]])), /expected one migration exception file/);
  for (const value of [null, {}, {schema: 'unknown', exceptions: [entry]}, manifest([]), manifest([entry, second])]) {
    assert.throws(() => loadMigrationExceptions(fixture([['1001_first.json', value]])), /invalid migration exception manifest/);
  }
  const invalid = loadMigrationExceptions(fixture([['1001_first.json', manifest([{...entry, sha256: 'bad'}])]]));
  const problems = checkMigrations(new Map([[entry.file, sql]]), new Map(), null, {exceptions: invalid}).join('\n');
  assert.match(problems, /invalid migration exception/);
  assert.match(problems, /1001_first.sql: non-allowlisted/);
  const malformed = fixture([]);
  writeFileSync(join(malformed, '1001_first.json'), '{');
  assert.throws(() => loadMigrationExceptions(malformed), SyntaxError);
  const nested = fixture([]);
  mkdirSync(join(nested, '1001_first.json'));
  assert.throws(() => loadMigrationExceptions(nested), /expected one migration exception file/);
  const linked = fixture([]);
  symlinkSync(join(directory, '1001_first.json'), join(linked, '1001_first.json'));
  assert.throws(() => loadMigrationExceptions(linked), /expected one migration exception file/);
  assert.throws(() => loadMigrationExceptions(join(directory, 'absent')), {code: 'ENOENT'});
});

const caseDirectory = new URL('./check-migrations-cases/', import.meta.url);
for (const entry of readdirSync(caseDirectory, {withFileTypes: true}).sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0)) {
  if (!entry.isFile() || !/^\d{4}_[a-z0-9_]+\.mjs$/.test(entry.name)) throw new Error(`${entry.name}: expected migration case NNNN_name.mjs`);
  const migration = new URL(`../internal/db/migrations/${entry.name.replace(/\.mjs$/, '.sql')}`, import.meta.url);
  readFileSync(migration); // An orphan check must fail even if its body forgets to read SQL.
  await import(new URL(entry.name, caseDirectory));
}
