// SPDX-License-Identifier: AGPL-3.0-only
// Conversion-only oracle: read the legacy inputs from an explicit Git base.
// No aggregate, snapshot or generated test index is committed.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { checkMigrations, loadMigrationExceptions } from './check-migrations.mjs';

const dataURL = source => `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`;
const args = process.argv.slice(2);
assert.ok(args.length === 2 && args[0] === '--base-ref' && /^[a-f0-9]{40}$/.test(args[1]),
  'usage: node scripts/check-migrations-proof.mjs --base-ref <full legacy commit SHA>');
const git = path => execFileSync('git', ['show', `${args[1]}:${path}`], {
  encoding: 'utf8', timeout: 30_000, maxBuffer: 10 << 20,
});
const legacy = JSON.parse(git('scripts/migration-policy-exceptions.json'));
const assembled = loadMigrationExceptions();
assert.deepEqual(assembled, legacy, 'assembled exception dump must retain every legacy field and entry');

// Registration executes parameter loops but never a test body. Capture both
// names and bodies so a renamed, missing, duplicated or weakened case fails.
const scratch = mkdtempSync(join(tmpdir(), 'aeon-985-proof-'));
const mock = dataURL(`
export default function test(name, body) {
  if (typeof name !== 'string' || typeof body !== 'function') throw new Error('expected named callback');
  (globalThis.migrationCases ??= []).push({name, body: body.toString()});
}
`);
const hook = dataURL(`
import { registerHooks } from 'node:module';
registerHooks({resolve(specifier, context, next) {
  return specifier === 'node:test' ? {url: ${JSON.stringify(mock)}, shortCircuit: true} : next(specifier, context);
}});
`);
const checkerURL = new URL('./check-migrations.mjs', import.meta.url).href;
const previousTests = join(scratch, 'legacy-cases.mjs');
writeFileSync(previousTests, git('scripts/check-migrations.test.mjs')
  .replace("from './check-migrations.mjs'", `from '${checkerURL}'`));
const dump = file => JSON.parse(execFileSync(process.execPath, ['--import', hook, '--input-type=module', '-e', `
await import(${JSON.stringify(file)});
process.stdout.write(JSON.stringify(globalThis.migrationCases));
`], {encoding: 'utf8', timeout: 30_000, maxBuffer: 10 << 20}));
const oldCases = dump(pathToFileURL(previousTests).href);
const newCases = dump(new URL('./check-migrations.test.mjs', import.meta.url).href)
  .filter(row => !row.name.startsWith('AEON-985 '));
const legacyNames = `[${legacy.exceptions.map(entry => JSON.stringify(entry.file)).join(', ')}]`;
const normalize = body => body
  .replaceAll("JSON.parse(readFileSync(new URL('./migration-policy-exceptions.json', import.meta.url), 'utf8'))", 'loadMigrationExceptions()')
  .replaceAll("new URL('../../internal/", "new URL('../internal/")
  .replaceAll('new URL(`../../internal/', 'new URL(`../internal/')
  // The two legacy set assertions are independently proved by the exact dump
  // equality above; future migrations must not append to a committed list.
  .replaceAll(legacyNames, 'migrationExceptionFiles()');
const canonical = rows => rows.map(row => ({name: row.name, body: normalize(row.body)}))
  .sort((a, b) => a.name.localeCompare(b.name));
assert.equal(new Set(newCases.map(row => row.name)).size, newCases.length, 'case identities must be unique');
assert.deepEqual(canonical(newCases), canonical(oldCases), 'assembled test-case dump must retain every legacy name and assertion');

// Also prove that the policy decision function itself did not change. The
// only production change is loading the individual files into its old input.
const oldChecker = await import(dataURL(git('scripts/check-migrations.mjs')
  .replace("from './verify-release.mjs'", `from '${new URL('./verify-release.mjs', import.meta.url).href}'`)));
assert.equal(checkMigrations.toString(), oldChecker.checkMigrations.toString(), 'migration-checker semantics must remain byte-identical');
console.log(`migration conversion proof: ${assembled.exceptions.length} exception records identical; ${newCases.length} test names and assertion bodies identical; checkMigrations unchanged`);
