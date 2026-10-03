// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { execute, isQuarantined, parseGo, parsePlaywright, quarantineSchema, retryCommand, runGuard, validateQuarantine, writeEvidence } from './ci-flake-guard.mjs';
import { weeklyReport } from './ci-flake-report.mjs';

const now = new Date('2026-10-03T20:48:00Z');
const pkg = 'github.com/inspr-at/paimos/internal/harness';
const quarantine = JSON.parse(readFileSync(new URL('./ci-quarantine.json', import.meta.url), 'utf8'));
const empty = { ...quarantine, entries: [] };
const badTest = 'TestTierChangeConfirmUndoAndSafePoint';
const goID = `internal/harness ${badTest}`;
const goResult = (name = badTest, status = 'fail', packageName = pkg) => [
  ...(name ? [{ Action: status, Package: packageName, Test: name }] : []),
  { Action: status, Package: packageName },
].map(value => JSON.stringify(value)).join('\n') + '\n';
const goFailure = name => parseGo(goResult(name)).failures[0];
const pwFailure = () => parsePlaywright('  1) [chromium] › tests/a.spec.ts:12:3 › group › needs a [safe] choice =====\n').failures[0];
function pwJSON(status = 'unexpected', overrides = {}) {
  return JSON.stringify({
    suites: [{ title: 'tests/a.spec.ts', file: 'tests/a.spec.ts', suites: [{ title: 'group', file: 'tests/a.spec.ts', specs: [{
      title: 'needs a [safe] choice', file: 'tests/a.spec.ts', line: 12, column: 3,
      tests: [{ projectName: 'chromium', status, expectedStatus: 'passed', results: [{ status: status === 'unexpected' ? 'failed' : 'passed' }], ...overrides }],
    }] }] }], errors: [],
  });
}
async function guard(results, options = {}) {
  const calls = [], logs = [];
  const code = await runGuard({
    command: ['go', 'test', '-race', './...'], quarantine: empty, now,
    env: { GITHUB_EVENT_NAME: 'merge_group' }, print: text => logs.push(text),
    run: async (command, details) => {
      calls.push({ command, env: details.env });
      assert.ok(results.length, 'unexpected command / extra retry');
      return results.shift();
    }, ...options,
  });
  assert.equal(results.length, 0, 'expected commands were not run');
  return { code, calls, evidence: logs.map(line => JSON.parse(line.slice('CI_FLAKE '.length))) };
}

// Small schema evaluator supports every keyword in the exported schema, making
// the checked-in JSON validation independent of the production validator.
function assertSchema(value, schema, root = schema) {
  if (schema.$ref) return assertSchema(value, root.$defs[schema.$ref.split('/').at(-1)], root);
  if (schema.const !== undefined) assert.deepEqual(value, schema.const);
  if (schema.enum) assert.ok(schema.enum.includes(value));
  if (schema.type === 'object') {
    assert.ok(value && typeof value === 'object' && !Array.isArray(value));
    for (const key of schema.required ?? []) assert.ok(Object.hasOwn(value, key), `missing ${key}`);
    if (schema.additionalProperties === false) assert.ok(Object.keys(value).every(key => Object.hasOwn(schema.properties, key)));
    for (const [key, item] of Object.entries(value)) if (schema.properties[key]) assertSchema(item, schema.properties[key], root);
  }
  if (schema.type === 'array') {
    assert.ok(Array.isArray(value));
    if (schema.maxItems !== undefined) assert.ok(value.length <= schema.maxItems);
    for (const item of value) assertSchema(item, schema.items, root);
  }
  if (schema.type === 'string') {
    assert.equal(typeof value, 'string');
    if (schema.minLength !== undefined) assert.ok(value.length >= schema.minLength);
    if (schema.maxLength !== undefined) assert.ok(value.length <= schema.maxLength);
    if (schema.pattern) assert.match(value, new RegExp(schema.pattern));
    if (schema.format === 'date') {
      assert.match(value, /^\d{4}-\d{2}-\d{2}$/);
      assert.equal(new Date(value).toISOString().slice(0, 10), value);
    }
  }
}

test('quarantine JSON validates against its schema and seeds only documented short-lived Go entries', () => {
  assertSchema(quarantine, quarantineSchema);
  assert.equal(validateQuarantine(quarantine), quarantine);
  assert.equal(quarantine.entries.length, 2);
  assert.equal(quarantine.entries[0].id, goID);
  assert.equal(quarantine.entries[0].kind, 'go');
  assert.equal(quarantine.entries[0].owner, 'AEON-623');
  assert.match(quarantine.entries[0].note, /Fixed by AEON-623/);
  assert.equal(quarantine.entries[1].id, 'internal/importer TestSourceRequestCapAndDelay');
  assert.equal(quarantine.entries[1].kind, 'go');
  assert.equal(quarantine.entries[1].owner, 'AEON-675');
  for (const entry of quarantine.entries) assert.ok(Date.parse(entry.expires) - Date.parse(entry.added) <= 7 * 86400000);
});
test('invalid quarantine configuration fails closed (schema, identities, dates, owner)', () => {
  for (const changed of [
    null, [], { ...empty, version: 2 }, { ...empty, extra: 1 }, { ...empty, template: {} },
    ...[ { kind: 'jest' }, { expires: '2026-02-31' }, { expires: '2026-10-03' },
      { added: '2026-13-01' }, { owner: 'nobody' }, { id: '' }, { note: '' }, { extra: true } ]
      .map(change => ({ ...empty, entries: [{ ...quarantine.entries[0], ...change }] })),
    { ...empty, entries: [quarantine.entries[0], quarantine.entries[0]] },
  ]) assert.throws(() => validateQuarantine(changed), /quarantine/i);
});
test('quarantines expire at UTC midnight, cannot activate early, exclude build failures and templates', () => {
  const failure = goFailure(badTest);
  assert.equal(isQuarantined(failure, quarantine.entries, now), true);
  assert.equal(isQuarantined(failure, quarantine.entries, new Date('2026-10-10T00:00:00Z')), false);
  assert.equal(isQuarantined(failure, quarantine.entries, new Date('2026-10-02T23:59:59Z')), false);
  assert.equal(isQuarantined({ ...failure, infrastructure: true }, quarantine.entries, now), false);
  assert.equal(isQuarantined(goFailure(`${badTest}/undo`), quarantine.entries, now), true);
  assert.equal(isQuarantined(goFailure(`${badTest}Other`), quarantine.entries, now), false);
  assert.equal(isQuarantined({ ...failure, kind: 'playwright' }, quarantine.entries, now), false);
});
test('Go JSON identifies full package paths, deduplicates parent failures and keeps only leaf subtests', () => {
  const output = goResult('TestParent/child.a') + goResult('TestParent') + goResult('TestOther', 'pass', 'example.net/other');
  const parsed = parseGo(output);
  assert.deepEqual(parsed.failures.map(value => value.id), ['internal/harness TestParent/child.a']);
  assert.equal(parsed.failures[0].package, pkg);
  assert.equal(parsed.packages.get('example.net/other').status, 'pass');
});
test('Go interleaved same-package runs retain failures and must all terminate', () => {
  const start = JSON.stringify({ Action: 'start', Package: pkg }) + '\n';
  const partial = start + start + goResult(null);
  assert.equal(parseGo(partial).incomplete, true);
  const finished = parseGo(partial + goResult(null, 'pass'));
  assert.equal(finished.incomplete, false);
  assert.equal(finished.failures[0].id, 'internal/harness');
  const names = parseGo(goResult('TestA') + goResult('TestA-other') + goResult('TestA/child'));
  assert.deepEqual(names.failures.map(value => value.name), ['TestA-other', 'TestA/child']);
});
test('Go text binds failures to packages and identifies compile/package-only failures', () => {
  const parsed = parseGo(`--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/undo (0.00s)\nFAIL\nFAIL\t${pkg}\t0.2s\nFAIL\tother/package\t[build failed]\n`);
  assert.deepEqual(parsed.failures.map(value => value.id), ['internal/harness TestA/undo', 'other/package']);
  assert.equal(parsed.failures[1].infrastructure, true);
  assert.equal(parseGo(`FAIL\t${pkg}\t0.2s`).failures[0].id, 'internal/harness');
});
test('Go text requires a package terminator; JSON output events do not duplicate text failures', () => {
  assert.equal(parseGo('--- FAIL: TestLost (0.0s)\n').incomplete, true);
  const event = JSON.stringify({ Action: 'output', Package: pkg, Output: '--- FAIL: TestLost (0.0s)\n' });
  assert.equal(parseGo(event + '\n' + goResult()).failures.length, 1);
  assert.equal(parseGo(event + '\n' + goResult()).incomplete, false);
});
test('Go JSON terminal package results are required, even for quarantined failures', async () => {
  const testOnly = JSON.stringify({ Action: 'fail', Package: pkg, Test: badTest });
  assert.equal(parseGo(testOnly).incomplete, true);
  const partial = goResult() + JSON.stringify({ Action: 'start', Package: 'other/package' });
  const result = await guard([{ code: 1, output: partial }], { quarantine });
  assert.equal(result.code, 1);
  assert.deepEqual(result.evidence.map(value => value.label), ['QUARANTINED', 'FAILED']);
});
test('Playwright JSON accepts npm banners/multiple reports, nested titles, projects and final status', () => {
  const parsed = parsePlaywright(`npm banner\n${pwJSON()}\n${pwJSON('expected')}\n`);
  assert.equal(parsed.failures.length, 1);
  assert.equal(parsed.failures[0].id, pwFailure().id);
  assert.equal(parsed.failures[0].title, 'group › needs a [safe] choice');
  for (const status of ['expected', 'skipped', 'flaky']) assert.equal(parsePlaywright(pwJSON(status)).failures.length, 0);
  assert.equal(parsePlaywright(pwJSON('expected', { expectedStatus: 'failed', results: [{ status: 'failed' }] })).failures.length, 0);
});
test('Playwright JSON handles timeouts/interruption and report-level setup failures', () => {
  for (const status of ['timedOut', 'interrupted', 'failed']) {
    assert.equal(parsePlaywright(pwJSON(undefined, { status: undefined, results: [{ status }] })).failures.length, 1);
  }
  assert.equal(parsePlaywright(JSON.stringify({ suites: [], errors: [{ message: 'global setup' }] })).infrastructure, true);
});
test('Playwright line failure headings strip ANSI, preserve unicode, deduplicate summaries and ignore progress as failure', () => {
  const line = '  1) [webkit] › dialog.spec.ts:3:1 › wählen › a | b =====';
  const parsed = parsePlaywright(`\x1b[31m${line}\x1b[0m\n${line}\n[2/3] [webkit] › pass.spec.ts:5:1 › passes\r`);
  assert.equal(parsed.failures.length, 1);
  assert.equal(parsed.failures[0].title, 'wählen › a | b');
  assert.equal(parsed.records.length, 2);
  assert.equal(parsePlaywright('  1) dialog.spec.ts:3:1 › works without project ===').failures[0].project, '');
});
test('Go retry keeps execution flags, removes package/run selectors, disables caching, and escapes slash segments', () => {
  const failure = goFailure('TestParent/a.[b]+/undo');
  const plan = retryCommand(['go', 'test', '-race', '-timeout', '3m', '-tags=integration', '-run', 'Test.*', '-count=2', '-skip=Slow', '-failfast', './...', './other'], failure);
  assert.deepEqual(plan.command, ['go', 'test', '-race', '-timeout', '3m', '-tags=integration', '-count=1', '-json', '-run', String.raw`^TestParent$/^a\.\[b\]\+$/^undo$`, pkg]);
  const wrapper = retryCommand(['go', 'run', './scripts/ci-go-shards', 'test'], failure, { CI_FLAKE_GO_RETRY_ARGS: '["-race","-tags=integration"]' });
  assert.deepEqual(wrapper.command.slice(0, 4), ['go', 'test', '-race', '-tags=integration']);
});
test('Go package retry narrows to exactly the failed package and unsafe/custom args are refused', () => {
  const failure = parseGo(`FAIL\t${pkg}\t[build failed]`).failures[0];
  assert.deepEqual(retryCommand(['go', 'test', './...'], failure).command, ['go', 'test', '-count=1', '-json', pkg]);
  assert.throws(() => retryCommand(['go', 'test', './...', '-args', 'custom'], failure), /safely narrow/);
  for (const extra of ['{}', '["./..."]', '[4]', 'null']) {
    assert.throws(() => retryCommand(['wrapper'], failure, { CI_FLAKE_GO_RETRY_ARGS: extra }), /JSON array/);
  }
});
test('Go test-prefixed selector aliases and fuzz flags cannot broaden a retry', () => {
  const plan = retryCommand(['go', 'test', '-test.run', 'Old', '-test.skip=Real', '-test.count=4', '-fuzz', 'Fuzz.*', '-fuzztime=1h', '-test.timeout', '5m', './...'], goFailure('TestReal'));
  assert.deepEqual(plan.command, ['go', 'test', '-test.timeout', '5m', '-count=1', '-json', '-run', '^TestReal$', pkg]);
});
test('direct Playwright retry selects the one location/title/project and preserves config/workers', () => {
  const plan = retryCommand(['npx', '--no-install', 'playwright', 'test', 'tests/all', '--config', 'special.ts', '--workers=1', '--shard=1/12', '--retries', '3', '--project=all', '--grep', 'original'], pwFailure());
  assert.deepEqual(plan.command, ['npx', '--no-install', 'playwright', 'test', '--config', 'special.ts', '--workers=1', 'tests/a.spec.ts:12', '--grep', String.raw`(?:^| )group needs a \[safe\] choice$`, '--retries=0', '--no-deps', '--project', 'chromium']);
  assert.equal(plan.env.PW_RETRIES, '0');
});
test('Playwright shard-wrapper retry exposes exact descriptors, location filter, title filter and retries=0', () => {
  const command = ['npm', '--prefix', 'web', 'run', 'ci:web:shard', '--', '3/12'];
  const plan = retryCommand(command, pwFailure());
  assert.deepEqual(plan.command, command);
  assert.deepEqual(JSON.parse(plan.env.CI_FLAKE_PLAYWRIGHT_TESTS), [pwFailure()]);
  assert.deepEqual(JSON.parse(plan.env.PW_TEST_FILES), ['tests/a.spec.ts:12']);
  assert.match(plan.env.PW_GREP, /safe/);
  assert.equal(plan.env.PW_RETRIES, '0');
});
test('merge_group retries only failed test once, succeeds on recovery, and records the attempt', async () => {
  const result = await guard([{ code: 1, output: goResult('TestReal') + goResult('TestPassing', 'pass') }, { code: 0, output: goResult('TestReal', 'pass') }]);
  assert.equal(result.code, 0);
  assert.equal(result.calls.length, 2);
  assert.deepEqual(result.calls[1].command.slice(-3), ['-run', '^TestReal$', pkg]);
  assert.deepEqual(result.evidence.map(value => [value.label, value.kind, value.id, value.attempt]), [['RETRIED', 'go', 'internal/harness TestReal', 2]]);
});
test('repeated failure fails the job with no third attempt', async () => {
  const result = await guard([{ code: 1, output: goResult('TestReal') }, { code: 1, output: goResult('TestReal') }]);
  assert.equal(result.code, 1);
  assert.deepEqual(result.evidence.map(value => value.label), ['RETRIED', 'FAILED AFTER RETRY']);
});
test('merge_group retries distinct failures sequentially, once each, never passing tests', async () => {
  const result = await guard([{ code: 1, output: goResult('TestA') + goResult('TestB') },
    { code: 0, output: goResult('TestA', 'pass') }, { code: 1, output: goResult('TestB') }]);
  assert.equal(result.code, 1);
  assert.deepEqual(result.calls.slice(1).map(value => value.command.at(-2)), ['^TestA$', '^TestB$']);
});
test('all non-queue events fail without retry', async () => {
  for (const event of ['pull_request', 'pull_request_target', 'push', 'workflow_dispatch', '', undefined]) {
    const result = await guard([{ code: 1, output: goResult('TestReal') }], { env: { GITHUB_EVENT_NAME: event } });
    assert.equal(result.code, 1);
    assert.equal(result.calls.length, 1);
    assert.equal(result.evidence[0].label, 'FAILED');
  }
});
test('quarantined failures run on every event, are reported, waive failure and are never retried', async () => {
  for (const event of ['merge_group', 'pull_request', 'push']) {
    const result = await guard([{ code: 1, output: goResult() }], { quarantine, env: { GITHUB_EVENT_NAME: event } });
    assert.equal(result.code, 0);
    assert.equal(result.calls.length, 1);
    assert.deepEqual(result.evidence.map(value => [value.label, value.id, value.attempt]), [['QUARANTINED', goID, 1]]);
  }
});
test('a mixed failure retries the real test, reports quarantine and keeps a real repeated failure red', async () => {
  const result = await guard([{ code: 1, output: goResult() + goResult('TestReal') }, { code: 1, output: goResult('TestReal') }], { quarantine });
  assert.equal(result.code, 1);
  assert.deepEqual(result.evidence.map(value => value.label), ['QUARANTINED', 'RETRIED', 'FAILED AFTER RETRY']);
  assert.equal(result.calls[1].command.at(-2), '^TestReal$');
});
test('expired quarantine fails on PR and becomes retryable on merge_group', async () => {
  const later = new Date('2026-10-10T00:00:00Z');
  const pr = await guard([{ code: 1, output: goResult() }], { quarantine, now: later, env: { GITHUB_EVENT_NAME: 'pull_request' } });
  assert.equal(pr.code, 1);
  const queue = await guard([{ code: 1, output: goResult() }, { code: 0, output: goResult(badTest, 'pass') }], { quarantine, now: later });
  assert.equal(queue.code, 0);
  assert.equal(queue.evidence[0].label, 'RETRIED');
});
test('quarantine cannot hide another package compile failure', async () => {
  const output = goResult() + 'FAIL\tother/package\t[build failed]\n';
  const result = await guard([{ code: 1, output }], { quarantine, env: { GITHUB_EVENT_NAME: 'pull_request' } });
  assert.equal(result.code, 1);
  assert.deepEqual(result.evidence.map(value => value.label), ['QUARANTINED', 'FAILED']);
});
test('launch, truncation, interruption, missing parseable failures, and incomplete output stay red without retry', async () => {
  for (const result of [
    { code: 127, error: 'not found', output: '' }, { code: 1, output: 'setup crashed' },
    { code: 0, output: '', overflow: true }, { code: 1, output: goResult(), interrupted: true },
    { code: 1, output: goResult(), timedOut: true }, { code: 1, output: '--- FAIL: TestLost (0.0s)' },
  ]) {
    const answer = await guard([result], { quarantine });
    assert.notEqual(answer.code, 0);
    assert.equal(answer.calls.length, 1);
    assert.equal(answer.evidence[0].label, 'FAILED');
  }
});
test('zero exit code cannot hide parsed real failures', async () => {
  const result = await guard([{ code: 0, output: goResult('TestReal') }], { env: { GITHUB_EVENT_NAME: 'pull_request' } });
  assert.equal(result.code, 1);
});
test('successful retry must show the requested test ran, not an empty/skipped/other test result', async () => {
  for (const output of ['', `ok\t${pkg}\t0.01s [no tests to run]`, goResult('TestOther', 'pass'),
    JSON.stringify({ Action: 'skip', Package: pkg, Test: 'TestReal' }) + '\n' + goResult(null, 'pass')]) {
    const result = await guard([{ code: 1, output: goResult('TestReal') }, { code: 0, output }]);
    assert.equal(result.code, 1);
    assert.equal(result.evidence.at(-1).label, 'FAILED AFTER RETRY');
  }
});
test('a retry failure in another test remains red; a newly observed quarantine is reported', async () => {
  const result = await guard([{ code: 1, output: goResult('TestReal') },
    { code: 1, output: goResult('TestReal', 'pass') + goResult() + goResult('TestOther') }], { quarantine });
  assert.equal(result.code, 1);
  assert.ok(result.evidence.some(value => value.id === goID && value.label === 'QUARANTINED' && value.attempt === 2));
});
test('Playwright retries recognise line and JSON output with exact same identity', async () => {
  for (const output of [pwJSON('expected'), '[1/1] [chromium] › tests/a.spec.ts:12:3 › group › needs a [safe] choice\n1 passed']) {
    const result = await guard([{ code: 1, output: pwJSON() }, { code: 0, output }], { command: ['npx', 'playwright', 'test', 'tests'], kind: 'playwright' });
    assert.equal(result.code, 0);
    assert.equal(result.calls[0].command.at(-1), '--retries=0');
    assert.equal(result.calls[0].env.PW_RETRIES, '0');
    assert.equal(result.calls[1].env.PW_RETRIES, '0');
  }
});
test('Playwright skipped retry and wrappers running additional tests cannot clear the failure', async () => {
  const full = JSON.parse(pwJSON('expected'));
  full.suites[0].suites[0].specs.push({ title: 'passing sibling', file: 'tests/a.spec.ts', line: 20, column: 3,
    tests: [{ projectName: 'chromium', status: 'expected', results: [{ status: 'passed' }] }] });
  for (const output of [pwJSON('skipped', { results: [{ status: 'skipped' }] }), JSON.stringify(full)]) {
    const result = await guard([{ code: 1, output: pwJSON() }, { code: 0, output }], { command: ['npm', 'run', 'ci:web:shard'], kind: 'playwright' });
    assert.equal(result.code, 1);
    assert.equal(result.evidence.at(-1).label, 'FAILED AFTER RETRY');
  }
});
test('Playwright quarantine works on PRs and errors outside test cases cannot be waived', async () => {
  const config = { ...empty, entries: [{ ...quarantine.entries[0], kind: 'playwright', id: pwFailure().id }] };
  const good = await guard([{ code: 1, output: pwJSON() }], { quarantine: config, kind: 'playwright', env: { GITHUB_EVENT_NAME: 'pull_request' } });
  assert.equal(good.code, 0);
  const report = JSON.parse(pwJSON()); report.errors = [{ message: 'teardown error' }];
  const bad = await guard([{ code: 1, output: JSON.stringify(report) }], { quarantine: config, kind: 'playwright' });
  assert.equal(bad.code, 1);
});
test('retry launch error, timeout and truncation produce FAILED AFTER RETRY', async () => {
  for (const change of [{ code: 127, error: 'bad exec' }, { code: 1, timedOut: true }, { code: 0, overflow: true }]) {
    const result = await guard([{ code: 1, output: goResult('TestReal') }, { output: '', ...change }]);
    assert.equal(result.code, 1);
    assert.equal(result.evidence.at(-1).label, 'FAILED AFTER RETRY');
  }
});
test('stdout and summary include id/kind/attempt/label; archived comments safely encode Markdown delimiters', () => {
  const directory = mkdtempSync(join(tmpdir(), 'ci-flake-summary-'));
  const summary = join(directory, 'step-summary');
  const lines = [];
  for (const label of ['RETRIED', 'QUARANTINED', 'FAILED AFTER RETRY']) {
    writeEvidence({ id: 'spec|test --> ü', kind: 'playwright', attempt: 2, label }, { summary, print: line => lines.push(line), now });
  }
  const text = readFileSync(summary, 'utf8');
  assert.match(text, /RETRIED.*playwright.*spec&#124;test.*2/);
  assert.equal(text.match(/<!-- CI_FLAKE /g).length, 3);
  assert.equal(JSON.parse(lines[0].slice(9)).id, 'spec|test --> ü');
  assert.match(weeklyReport(directory), /spec&#124;test --&gt; ü \| 1/);
});
test('weekly report groups per Monday UTC week and test, including nested summaries; excludes quarantine and final failures', () => {
  const directory = mkdtempSync(join(tmpdir(), 'ci-flake-weekly-'));
  mkdirSync(join(directory, 'nested'));
  const summary = join(directory, 'nested', 'summary');
  const record = { label: 'RETRIED', kind: 'go', id: 'internal/a TestA', attempt: 2 };
  for (const date of ['2026-10-03T23:30:00Z', '2026-10-04T00:00:00Z', '2026-10-05T00:00:00Z']) writeEvidence(record, { summary, print: () => {}, now: new Date(date) });
  writeEvidence({ ...record, label: 'QUARANTINED' }, { summary, print: () => {}, now });
  writeEvidence({ ...record, id: 'internal/a TestB' }, { summary, print: () => {}, now });
  const text = weeklyReport(directory);
  assert.match(text, /2026-09-28 \| go \| internal\/a TestA \| 2/);
  assert.match(text, /2026-10-05 \| go \| internal\/a TestA \| 1/);
  assert.match(text, /2026-09-28 \| go \| internal\/a TestB \| 1/);
});
test('weekly report empty input and malformed records are honest', () => {
  const directory = mkdtempSync(join(tmpdir(), 'ci-flake-empty-'));
  assert.match(weeklyReport(directory), /No retries recorded/);
  writeFileSync(join(directory, 'bad'), '<!-- CI_FLAKE {"label":"RETRIED","kind":"go","id":"a","attempt":2,"date":"invalid"} -->\n');
  assert.throws(() => weeklyReport(directory), /Invalid flake summary/);
});

test('CLI subprocess wraps arguments without a shell and surfaces summary evidence', () => {
  const directory = mkdtempSync(join(tmpdir(), 'ci-flake-cli-'));
  const fake = join(directory, 'go');
  const calls = join(directory, 'calls.jsonl');
  const summary = join(directory, 'summary');
  writeFileSync(fake, `#!${process.execPath}\nconst fs=require('node:fs');const args=process.argv.slice(2);fs.appendFileSync(${JSON.stringify(calls)},JSON.stringify(args)+'\\n');const retry=args.includes('-run');const status=retry?'pass':'fail';console.log(JSON.stringify({Action:status,Package:${JSON.stringify(pkg)},Test:'TestReal'}));console.log(JSON.stringify({Action:status,Package:${JSON.stringify(pkg)}}));process.exitCode=retry?0:1;\n`, { mode: 0o755 });
  const guardPath = new URL('./ci-flake-guard.mjs', import.meta.url).pathname;
  const result = spawnSync(process.execPath, [guardPath, '--kind', 'go', '--', fake, 'test', '-race', './...'], {
    encoding: 'utf8', env: { GITHUB_EVENT_NAME: 'merge_group', GITHUB_STEP_SUMMARY: summary },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /RETRIED/);
  assert.match(readFileSync(summary, 'utf8'), /RETRIED.*go.*internal\/harness TestReal.*2/);
  const invoked = readFileSync(calls, 'utf8').trim().split('\n').map(JSON.parse);
  assert.deepEqual(invoked[1], ['test', '-race', '-count=1', '-json', '-run', '^TestReal$', pkg]);
});
test('CLI preserves non-test command status, accepts omitted kind, and rejects invalid syntax', () => {
  const path = new URL('./ci-flake-guard.mjs', import.meta.url).pathname;
  const failure = spawnSync(process.execPath, [path, '--', process.execPath, '-e', 'process.exit(17)'], { encoding: 'utf8', env: {} });
  assert.equal(failure.status, 17);
  const success = spawnSync(process.execPath, [path, '--', process.execPath, '-e', 'console.log("literal $(command) argument")'], { encoding: 'utf8', env: {} });
  assert.equal(success.status, 0);
  assert.match(success.stdout, /literal \$\(command\)/);
  for (const args of [[], ['--kind', 'jest', '--', 'go'], ['--kind', 'go'], ['--']]) {
    const invalid = spawnSync(process.execPath, [path, ...args], { encoding: 'utf8', env: {} });
    assert.equal(invalid.status, 2);
    assert.match(invalid.stderr, /Usage:/);
  }
});
test('execute streams both outputs, handles launch failure and enforces an output bound', async () => {
  const logs = [];
  const result = await execute([process.execPath, '-e', 'console.log("out");console.error("err")'], { env: {}, print: (text, stream) => logs.push([text.toString(), stream]) });
  assert.equal(result.code, 0);
  assert.match(result.output, /out/); assert.match(result.output, /err/);
  assert.ok(logs.some(([, stream]) => stream === 'stderr'));
  const missing = await execute(['/nonexistent/ci-flake-test-command'], { env: {}, print: () => {} });
  assert.equal(missing.code, 127);
  assert.match(missing.error, /ENOENT/);
  const bounded = await execute([process.execPath, '-e', 'console.log("x".repeat(100))'], { env: {}, print: () => {}, maxOutputBytes: 8 });
  assert.equal(bounded.overflow, true);
  assert.equal(bounded.output, '');
});

test('with CI_FLAKE_REQUIRE_JSON a failure beside another group\'s startup error is never retried green', async () => {
  const env = { GITHUB_EVENT_NAME: 'merge_group', CI_FLAKE_REQUIRE_JSON: '1' }
  const command = ['npm', 'run', 'ci:web:shard']
  // Merged wrapper report: one assertion failure plus an error from a group that never started.
  const merged = JSON.parse(pwJSON()); merged.errors = [{ message: 'Missing or invalid Playwright JSON report for status-help' }]
  const mixed = await guard([{ code: 1, output: JSON.stringify(merged) }], { env, command, kind: 'playwright' })
  assert.equal(mixed.code, 1)
  assert.equal(mixed.calls.length, 1, 'infrastructure error was retried')
  assert.equal(mixed.evidence.at(-1).label, 'FAILED')
  // Line output only: the guard cannot see the other group, so it refuses to retry.
  const line = '  1) [chromium] › tests/a.spec.ts:12:3 › group › needs a [safe] choice\n  1 failed'
  const lineOnly = await guard([{ code: 1, output: line }], { env, command, kind: 'playwright' })
  assert.equal(lineOnly.code, 1)
  assert.equal(lineOnly.calls.length, 1)
  // A structured retry that passes is still accepted, and a retry without a report is not.
  const ok = await guard([{ code: 1, output: pwJSON() }, { code: 0, output: pwJSON('expected') }], { env, command, kind: 'playwright' })
  assert.equal(ok.code, 0)
  const noReport = await guard([{ code: 1, output: pwJSON() },
    { code: 0, output: '[1/1] [chromium] › tests/a.spec.ts:12:3 › group › needs a [safe] choice\n1 passed' }], { env, command, kind: 'playwright' })
  assert.equal(noReport.code, 1)
})
