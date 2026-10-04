// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdtempSync, mkdirSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import {
  balanceShards, checkCoverage, checkSpecInventory, discoverSpecs, loadManifest, main, parseArgs,
  parseShard, planCommands, planFlakeRetry, reconcileManifest, retryCount, selectChangedSpecs, validateManifest, mergeReports, webRoot,
} from './ci-web-shard.mjs'

const group = (id, weights) => ({ id, config: 'playwright.ui.config.ts', project: null,
  flags: ['--workers=2', '--trace=retain-on-failure'], env: {}, hostedOnly: true,
  specs: weights.map((weightSeconds, i) => ({ file: `tests/${id}-${i}.spec.ts`, weightSeconds })),
})
const fixture = () => ({ version: 1, groups: [group('a', [9, 8, 7]), group('b', [6, 5, 4])] })
const files = manifest => manifest.groups.flatMap(g => g.specs.map(s => s.file))
// The real manifest plus any specs that landed on main since it was written (what every shard job runs).
const effective = () => reconcileManifest(loadManifest(), discoverSpecs()).manifest

test('source manifest accounts for every spec, including nested files, before reconciliation', () => {
  const manifest = loadManifest(), discovered = discoverSpecs(webRoot, manifest.specDirectories)
  assert.deepEqual(checkSpecInventory(manifest, discovered), { specs: discovered.length, excluded: 0 })
  assert.deepEqual(manifest.exclusions, [])
  for (const file of ['tests/clip-tip.spec.ts', 'tests/aeon-632b-clip.spec.ts', 'tests/key-trim.spec.ts', 'tests/model-prefs.spec.ts', 'tests/record-ownership.spec.ts']) {
    assert.equal(balanceShards(manifest, 12).flatMap(shard => shard.specs).filter(spec => spec.file === file).length, 1, `${file} must gate exactly once`)
  }
})

test('new specs require a map entry or a valid ticketed exclusion; stale or conflicting exclusions fail', () => {
  const manifest = fixture(), added = 'tests/new.spec.ts', discovered = [...files(manifest), added]
  assert.throws(() => checkSpecInventory(manifest, discovered), /missing from manifest or ticketed exclusions: tests\/new\.spec\.ts/)
  manifest.exclusions = [{ file: added, ticket: 'AEON-676', reason: 'Known hosted failure' }]
  assert.deepEqual(checkSpecInventory(manifest, discovered), { specs: 6, excluded: 1 })
  for (const ticket of [undefined, '', 'pending', 'AEON', 'AEON-0']) {
    const invalid = structuredClone(manifest)
    invalid.exclusions[0].ticket = ticket
    assert.throws(() => checkSpecInventory(invalid, discovered), /Exclusion needs a ticket key/)
  }
  const invalidReason = structuredClone(manifest)
  invalidReason.exclusions[0].reason = ' '
  assert.throws(() => validateManifest(invalidReason), /Exclusion needs a reason/)
  assert.throws(() => validateManifest({ ...manifest, exclusions: {} }), /exclusions array/)
  assert.throws(() => validateManifest({ ...manifest, exclusions: [null] }), /Invalid exclusion path/)
  assert.throws(() => validateManifest({ ...manifest, exclusions: [...manifest.exclusions, ...manifest.exclusions] }), /Duplicate exclusion/)
  assert.throws(() => validateManifest({ ...manifest, exclusions: [{ ...manifest.exclusions[0], file: files(manifest)[0] }] }), /both declared and excluded/)
  assert.throws(() => checkSpecInventory(manifest, files(manifest)), /Stale manifest spec or exclusion: tests\/new\.spec\.ts/)
  const { manifest: reconciled, unlisted, stale } = reconcileManifest(manifest, discovered)
  assert.deepEqual({ unlisted, stale }, { unlisted: [], stale: [] })
  for (const all of [false, true]) {
    const shards = balanceShards(reconciled, 2, { all })
    assert.ok(!shards.flatMap(shard => shard.specs).some(spec => spec.file === added))
    assert.deepEqual(checkCoverage(reconciled, shards, discovered, { all }), { specs: 6, shards: 2, excluded: 1 })
  }
  assert.deepEqual(reconcileManifest(manifest, files(manifest)).stale, [added])
})

test('LPT balancing distributes a known fixture optimally and covers it exactly once', () => {
  const manifest = fixture(), shards = balanceShards(manifest, 3)
  assert.deepEqual(shards.map(s => s.weightSeconds), [13, 13, 13])
  assert.deepEqual(checkCoverage(manifest, shards, files(manifest)), { specs: 6, shards: 3 })
})

test('assignment is deterministic despite manifest group/spec order and breaks ties by path', () => {
  const manifest = fixture(), reordered = structuredClone(manifest)
  reordered.groups.reverse().forEach(g => g.specs.reverse())
  assert.deepEqual(balanceShards(manifest, 4), balanceShards(reordered, 4))
  const ties = { version: 1, groups: [group('tie', [1, 1, 1, 1])] }
  assert.deepEqual(balanceShards(ties, 2).map(s => s.specs.map(t => t.file)), [
    ['tests/tie-0.spec.ts', 'tests/tie-2.spec.ts'], ['tests/tie-1.spec.ts', 'tests/tie-3.spec.ts'],
  ])
})

test('coverage rejects duplicates, missing assignment, undeclared files and filesystem drift', () => {
  const manifest = fixture(), shards = balanceShards(manifest, 3)
  const duplicate = structuredClone(shards)
  duplicate[1].specs.push(duplicate[0].specs[0])
  assert.throws(() => checkCoverage(manifest, duplicate, files(manifest)), /appears in two shards/)
  const missing = structuredClone(shards)
  missing[0].specs.pop()
  assert.throws(() => checkCoverage(manifest, missing, files(manifest)), /missing from shards/)
  const extra = structuredClone(shards)
  extra[0].specs.push({ file: 'tests/extra.spec.ts' })
  assert.throws(() => checkCoverage(manifest, extra, files(manifest)), /Undeclared shard spec/)
  const wrongOwner = structuredClone(shards)
  wrongOwner[0].specs[0].groupId = 'missing-group'
  assert.throws(() => checkCoverage(manifest, wrongOwner, files(manifest)), /Wrong group/)
  assert.throws(() => checkCoverage(manifest, shards, [...files(manifest), 'tests/new.spec.ts']), /missing from manifest/)
  assert.throws(() => checkCoverage(manifest, shards, files(manifest).slice(1)), /Stale manifest spec/)
})

test('manifest rejects duplicate ownership, nonpositive weights and selection flags', () => {
  const duplicate = fixture()
  duplicate.groups[1].specs.push(duplicate.groups[0].specs[0])
  assert.throws(() => validateManifest(duplicate), /Duplicate spec/)
  for (const weight of [0, -1, NaN, Infinity]) {
    const invalid = fixture()
    invalid.groups[0].specs[0].weightSeconds = weight
    assert.throws(() => validateManifest(invalid), /Invalid weight/)
  }
  for (const flag of ['--shard=1/2', '--grep', '--config=another.config.ts', '--last-failed']) {
    const invalid = fixture()
    invalid.groups[0].flags.push(flag)
    assert.throws(() => validateManifest(invalid), /Selection flags/)
  }
  const separator = fixture()
  separator.groups[0].flags.push('--')
  assert.throws(() => validateManifest(separator), /npm argument separator/)
})

test('CLI accepts positional and named shards and browser-free inspection for N', () => {
  assert.deepEqual(parseShard('12/12'), { index: 12, count: 12 })
  assert.deepEqual(parseArgs(['1/12']), { mode: 'run', shard: { index: 1, count: 12 }, count: 12 })
  assert.deepEqual(parseArgs(['--list', '--shard', '2/7']), { mode: 'list', shard: { index: 2, count: 7 }, count: 7 })
  assert.deepEqual(parseArgs(['--check', '--shards', '7']), { mode: 'check', shard: undefined, count: 7 })
  for (const value of ['0/12', '13/12', '1/0', '1/257', '1.5/2', '1/NaN']) assert.throws(() => parseShard(value))
  for (const args of [[], ['--shard'], ['--list', '--check'], ['1/12', '2/12'], ['--check', '--shards'], ['--wat']]) {
    assert.throws(() => parseArgs(args))
  }
  assert.equal(parseArgs(['1/12', '--last-failed']).lastFailed, true)
  assert.equal(parseArgs(['1/12', '--test-list=/tmp/failures.txt', '--reporter=json']).testList, '/tmp/failures.txt')
  for (const args of [['1/12', '--test-list'], ['1/12', '--last-failed', '--test-list=x'], ['--list', '1/12', '--reporter=json']]) {
    assert.throws(() => parseArgs(args))
  }
})

test('commands retain config/project/workers/traces/evidence and honor PW_RETRIES', () => {
  const manifest = fixture()
  manifest.groups[0].project = 'chromium'
  manifest.groups[0].env = { AEON_DESK_SHOTS: '${RUNNER_TEMP}/aeon-desk-shots', RELEASE_LIST_SHOT_LABEL: 'after' }
  const commands = planCommands(manifest, balanceShards(manifest, 1)[0], { RUNNER_TEMP: '/tmp/runner', PW_RETRIES: '2' }, '/checkout/web')
  assert.equal(commands.length, 2)
  assert.deepEqual(commands[0].env, { AEON_DESK_SHOTS: '/tmp/runner/aeon-desk-shots', RELEASE_LIST_SHOT_LABEL: 'after' })
  assert.deepEqual(commands[0].args.slice(0, 7), ['--config', 'playwright.ui.config.ts', '--project', 'chromium', '--workers=2', '--trace=retain-on-failure', '--retries=2'])
  assert.ok(commands[0].args.includes('test-results/ci-web/shard-1/a'))
  assert.ok(commands[1].args.includes('test-results/ci-web/shard-1/b'))
  const filter = commands[0].args.at(-1)
  assert.ok(new RegExp(filter).test('/checkout/web/tests/a-2.spec.ts'))
  assert.ok(!new RegExp(filter).test('/checkout/web/tests/a-2XspecXts'))
  assert.ok(!new RegExp(filter).test('/checkout/web/tests/nested/tests/a-2.spec.ts'))
  assert.equal(retryCount({}), 0)
  for (const value of ['-1', '1.5', 'NaN', '9007199254740992']) assert.throws(() => retryCount({ PW_RETRIES: value }), /PW_RETRIES/)
})

test('real manifest covers every non-excluded UI spec once for 1, 12 and 256 shards, including nested specs', () => {
  const expected = discoverSpecs(), manifest = reconcileManifest(loadManifest(), expected).manifest
  assert.ok(expected.includes('tests/quotes/print-receipt.spec.ts'))
  assert.ok(!expected.includes('e2e/smoke.spec.ts'))
  for (const count of [1, 12, 256]) {
    assert.deepEqual(checkCoverage(manifest, balanceShards(manifest, count, { all: true }), expected, { all: true }), {
      specs: expected.length - manifest.exclusions.length, shards: count,
      ...(manifest.exclusions.length ? { excluded: manifest.exclusions.length } : {}),
    })
  }
  const access = manifest.groups.find(g => g.id === 'access-dialogs')
  assert.equal(access.hostedOnly, true)
  assert.ok(access.flags.includes('--trace=retain-on-failure'))
  assert.equal(manifest.groups.find(g => g.id === 'performance').config, 'playwright.perf.config.ts')
  const release = manifest.groups.find(g => g.id === 'release')
  assert.equal(release.env.RELEASE_LIST_SHOTS, '${RUNNER_TEMP}/aeon-release-list-shots')
  const recurrence = manifest.groups.find(g => g.id === 'recurrences')
  assert.notEqual(recurrence.gate, false)
  assert.deepEqual(recurrence.flags, ['--workers=1', '--trace=retain-on-failure'])
  assert.equal(recurrence.env.AEON_RECURRENCE_SHOTS, '${RUNNER_TEMP}/aeon-recurrence-shots')
  assert.deepEqual(recurrence.specs.map(s => s.file), ['tests/recurrences.spec.ts', 'tests/recurrence-marker.spec.ts'])
  assert.equal(balanceShards(manifest, 12).flatMap(s => s.specs).filter(s => s.file === 'tests/recurrences.spec.ts').length, 1)
  assert.equal(files(manifest).filter(file => file === 'tests/access.spec.ts').length, 1)
  for (const e of manifest.ciInventory.filter(e => e.kind === 'browser-test' && e.config === 'playwright.ui.config.ts' && !e.revision)) {
    for (const file of e.specs) assert.ok(files(manifest).includes(file), `${e.id}: ${file}`)
  }
})

test('list/check launch no process and checks are wired into the existing CI unit script', async () => {
  const forbiddenRun = async () => { assert.fail('Browser-free mode launched a process') }
  const output = []
  assert.equal(await main(['--check', '--all', '--shards', '12'], { run: forbiddenRun, out: s => output.push(s), env: {} }), 0)
  const all = JSON.parse(output.pop())
  assert.equal(all.specs + (all.excluded ?? 0), discoverSpecs().length)
  assert.equal(await main(['--check', '--shards', '12'], { run: forbiddenRun, out: s => output.push(s), env: {} }), 0)
  const gate = JSON.parse(output.pop())
  assert.equal(gate.specs + gate.ungated + (gate.excluded ?? 0), discoverSpecs().length)
  assert.equal(await main(['--list', '1/12'], { run: forbiddenRun, out: s => output.push(s), env: {} }), 0)
  assert.equal(JSON.parse(output.pop()).length, 1)
  const scripts = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8')).scripts
  assert.ok(scripts['test:unit'].startsWith('npm run ci:web:shard:test &&'))
  assert.ok(scripts['ci:web:shard:test'].includes('npm run ci:web:shard:check'))
})

test('runner is hosted-only, runs groups sequentially, retains failure and stops on interruption', async () => {
  const env = { CI: 'true', RUNNER_ENVIRONMENT: 'github-hosted', RUNNER_TEMP: '/tmp/runner', PW_RETRIES: '0' }
  await assert.rejects(main(['1/1'], { env: {}, out: () => {} }), /hosted CI/)
  await assert.rejects(main(['1/1'], { env: { CI: '1', RUNNER_ENVIRONMENT: 'self-hosted' }, out: () => {} }), /github-hosted/)
  const calls = [], manifest = effective()
  let active = false
  const run = async (args, options) => {
    assert.equal(active, false)
    active = true
    await Promise.resolve()
    calls.push({ args, options })
    active = false
    return { code: calls.length === 1 ? 1 : 0 }
  }
  assert.equal(await main(['1/1'], { manifest, env, run, out: () => {} }), 1)
  assert.equal(calls.length, manifest.groups.filter(g => g.gate !== false).length)
  assert.equal(calls.find(call => call.options.env.AEON_DESK_SHOTS).options.env.AEON_DESK_SHOTS, '/tmp/runner/aeon-desk-shots')
  let interruptedCalls = 0
  assert.equal(await main(['1/1'], { env, out: () => {}, run: async () => { interruptedCalls++; return { code: 143 } } }), 143)
  assert.equal(interruptedCalls, 1)
})

test('spec-only execution runs exactly changed mapped, ungated and unlisted specs and retains failures', async () => {
  const root = mkdtempSync(resolve(tmpdir(), 'aeon-changed-specs-'))
  mkdirSync(resolve(root, 'tests'))
  const manifest = fixture()
  manifest.groups[1].gate = false
  manifest.groups[0].project = 'chromium'
  manifest.groups[0].flags.push('--trace=retain-on-failure')
  manifest.groups[0].env = { AEON_DESK_SHOTS: '${RUNNER_TEMP}/desk' }
  const discovered = [...files(manifest), 'tests/not-in-map.spec.ts']
  for (const file of discovered) writeFileSync(resolve(root, file), 'fixture')
  writeFileSync(resolve(root, 'playwright.ui.config.ts'), 'fixture')
  const selected = ['web/tests/a-0.spec.ts', 'web/tests/b-1.spec.ts', 'web/tests/not-in-map.spec.ts']
  const env = { CI: '1', RUNNER_ENVIRONMENT: 'github-hosted', RUNNER_TEMP: '/tmp/runner', CI_CHANGED_SPECS: JSON.stringify(selected) }
  const calls = []
  const run = async (args, options) => { calls.push({ args, options }); return { code: calls.length === 1 ? 19 : 0 } }
  assert.equal(await main(['1/1'], { manifest, env, root, run, out: () => {} }), 19)
  assert.equal(calls.length, 3)
  const requested = calls.flatMap(call => call.args.filter(arg => arg.startsWith('^')).map(pattern =>
    discovered.filter(file => new RegExp(pattern).test(resolve(root, file)))))
  assert.deepEqual(requested.flat().sort(), selected.map(file => file.slice(4)).sort())
  assert.ok(calls[0].args.includes('chromium'))
  assert.ok(calls[0].args.includes('--trace=retain-on-failure'))
  assert.equal(calls[0].options.env.AEON_DESK_SHOTS, '/tmp/runner/desk')
  assert.ok(calls.every(call => !call.args.includes('--pass-with-no-tests')))
  await assert.rejects(main(['1/12'], { manifest, env, root, run, out: () => {} }), /require shard 1\/1/)
  assert.equal(calls.length, 3, 'invalid selection launched work')
})

test('changed-spec selector refuses malformed, empty, traversing and missing selections', () => {
  for (const json of ['null', '{}', '[]']) assert.throws(() => selectChangedSpecs(fixture(), json), /nonempty changed spec list/)
  assert.throws(() => selectChangedSpecs(fixture(), '["web/tests/../a.spec.ts"]'), /Invalid repository path/)
  assert.throws(() => selectChangedSpecs(fixture(), '["/web/tests/a.spec.ts"]'), /Invalid repository path/)
  assert.throws(() => selectChangedSpecs(fixture(), '["web/tests/a-0.spec.ts", "web/tests/missing.spec.ts"]'), /missing from checkout/)
  assert.throws(() => selectChangedSpecs(fixture(), '["web/tests/helper.ts"]'), /top-level UI spec/)
})

test('native failure-only selectors are passed to every group without repartitioning', async () => {
  const env = { CI: '1', RUNNER_ENVIRONMENT: 'github-hosted' }, calls = []
  const run = async args => { calls.push(args); return { code: 0 } }
  await main(['1/12', '--last-failed'], { env, run, readLastRun: () => ({ status: 'failed', failedTests: ['id'] }), out: () => {} })
  assert.ok(calls.length > 0)
  for (const args of calls) assert.ok(args.includes('--last-failed') && args.includes('--pass-with-no-tests'))
  calls.length = 0
  await main(['1/12', '--last-failed'], { env, run, readLastRun: () => ({ status: 'passed', failedTests: [] }), out: () => {} })
  assert.equal(calls.length, 0, 'green groups were replayed')
  for (const invalid of [undefined, {}, { status: 'failed', failedTests: [] }, { status: 'failed', failedTests: [123] }]) {
    await assert.rejects(main(['1/12', '--last-failed'], { env, run, readLastRun: () => invalid, out: () => {} }), /last-run state|No selectable/)
  }
  await assert.rejects(main(['1/12', '--last-failed'], { env, run, readLastRun: () => { throw new Error('missing') }, out: () => {} }), /Missing last-run/)
  assert.equal(calls.length, 0, 'invalid state replayed any tests')
  await main(['1/12', '--test-list', '/tmp/failures.txt'], { env, run, out: () => {} })
  for (const args of calls) {
    assert.equal(args[args.indexOf('--test-list') + 1], '/tmp/failures.txt')
    assert.ok(args.includes('--pass-with-no-tests'))
  }
})

test('report merger retains errors, failures, suites and timing from every group', () => {
  const reports = [
    { config: { rootDir: '/web/tests' }, suites: [{ file: 'first.spec.ts' }], errors: [{ message: 'earlier failure' }], stats: { startTime: '2026-10-03T00:00:00Z', duration: 10, unexpected: 1 } },
    { config: { rootDir: '/web/tests' }, suites: [{ file: 'last.spec.ts' }], errors: [], stats: { duration: 20, expected: 3 } },
  ]
  const report = mergeReports(reports)
  assert.deepEqual(report.suites.map(s => s.file), ['first.spec.ts', 'last.spec.ts'])
  assert.equal(report.errors[0].message, 'earlier failure')
  assert.equal(report.stats.unexpected, 1)
  assert.equal(report.stats.expected, 3)
  assert.equal(report.stats.duration, 30)
})

test('wrapper JSON output aggregates every group and cannot reuse a stale green report', async () => {
  const reportDirectory = resolve(webRoot, 'test-results', 'ci-web-unit-reports')
  const env = { CI: '1', RUNNER_ENVIRONMENT: 'github-hosted', PLAYWRIGHT_JSON_OUTPUT_FILE: 'test-results/ci-web-unit-reports/test-aggregate.json' }
  const manifest = effective()
  const shard = balanceShards(manifest, 12).find(candidate => planCommands(manifest, candidate, env).length > 1)
  assert.ok(shard, 'fixture must span several groups')
  const expectedCommands = planCommands(manifest, shard, env)
  const selection = `${shard.index}/12`
  assert.ok(expectedCommands.length > 1, 'fixture must span several groups')
  let invocations = 0
  const run = async (args, options) => {
    invocations++
    assert.equal(readFileSync(options.env.PLAYWRIGHT_JSON_OUTPUT_FILE, 'utf8'), '')
    assert.equal(args[args.indexOf('--reporter') + 1], 'line,json')
    const failed = invocations === 1
    writeFileSync(options.env.PLAYWRIGHT_JSON_OUTPUT_FILE, JSON.stringify({ config: { rootDir: resolve(webRoot, 'tests') },
      suites: [{ file: failed ? 'earlier-failure.spec.ts' : 'later-green.spec.ts' }],
      errors: failed ? [{ message: 'earlier failure' }] : [], stats: { unexpected: failed ? 1 : 0, expected: failed ? 0 : 1 },
    }))
    return { code: failed ? 1 : 0 }
  }
  assert.equal(await main([selection], { env, run, reportDirectory, out: () => {} }), 1)
  const path = resolve(webRoot, env.PLAYWRIGHT_JSON_OUTPUT_FILE)
  const report = JSON.parse(readFileSync(path, 'utf8'))
  assert.equal(report.suites.length, expectedCommands.length)
  assert.equal(report.errors[0].message, 'earlier failure')
  assert.equal(report.stats.unexpected, 1)
  // The same group paths still contain prior green reports: main must clear
  // them before a launch that returns without producing a fresh report.
  assert.equal(await main([selection], { env, run: async () => ({ code: 0 }), reportDirectory, out: () => {} }), 1)
  const missing = JSON.parse(readFileSync(path, 'utf8'))
  assert.equal(missing.suites.length, 0)
  assert.equal(missing.errors.length, expectedCommands.length)
  assert.match(missing.errors[0].message, /Missing or invalid/)
  writeFileSync(path, JSON.stringify(report))
  assert.equal(await main([selection], { env, run: async () => ({ code: 143 }), reportDirectory, out: () => {} }), 143)
  assert.equal(readFileSync(path, 'utf8'), '', 'interruption retained an old green aggregate')
})

test('a flake retry selects exactly one test in its original group and fails closed otherwise', async () => {
  const manifest = effective(), env = { CI: '1', RUNNER_ENVIRONMENT: 'github-hosted', PW_RETRIES: '0' }
  const planned = planCommands(manifest, balanceShards(manifest, 12)[0], env, webRoot)
  const spec = planned[0].files[0]
  const test = { file: spec, line: 12, column: 3, title: 'suite › case', project: '' }
  const withTests = (...tests) => ({ ...env, CI_FLAKE_PLAYWRIGHT_TESTS: JSON.stringify(tests), PW_GREP: '(?:^| )suite case$' })
  const retryEnv = withTests(test)
  const [only] = planFlakeRetry(planned, retryEnv, webRoot)
  assert.deepEqual(only.files, [spec])
  assert.equal(only.args.filter(arg => arg.startsWith('^')).length, 1)
  assert.equal(only.args[only.args.indexOf('--grep') + 1], retryEnv.PW_GREP)
  assert.ok(only.args.includes('--retries=0'))
  const original = planned[0].args[planned[0].args.indexOf('--output') + 1]
  const retryOutput = only.args[only.args.indexOf('--output') + 1]
  assert.ok(retryOutput.startsWith(`${original}-retry-`) && retryOutput !== original, 'retry reuses the first attempt output directory')
  // Reporter paths relative to the config testDir resolve to the same spec.
  assert.deepEqual(planFlakeRetry(planned, withTests({ ...test, file: spec.replace(/^[^/]+\//, '') }), webRoot)[0].files, [spec])
  // A testDir-relative reporter path prefers the exact tests/<path> spec over any longer suffix match.
  const nested = structuredClone(planned)
  nested[0].files = ['tests/a.spec.ts', 'tests/x/a.spec.ts']
  assert.deepEqual(planFlakeRetry(nested, withTests({ ...test, file: 'a.spec.ts' }), webRoot)[0].files, ['tests/a.spec.ts'])
  for (const bad of [
    { ...env, CI_FLAKE_PLAYWRIGHT_TESTS: 'not json', PW_GREP: 'x' },
    withTests(),
    withTests(test, test),
    { ...retryEnv, PW_GREP: '' },
    withTests({ ...test, file: 'tests/not-in-this-shard.spec.ts' }),
    withTests({ ...test, title: '' }),
  ]) assert.throws(() => planFlakeRetry(planned, bad, webRoot))
  const projectCommand = planned.find(command => command.args.includes('--project'))
  if (projectCommand) {
    const other = { ...test, file: projectCommand.files[0], project: 'other-project' }
    assert.throws(() => planFlakeRetry(planned, withTests(other), webRoot), /differs from group project/)
  }
  const calls = []
  const run = async args => { calls.push(args); return { code: 0 } }
  assert.equal(await main(['1/12'], { manifest, env: retryEnv, run, out: () => {} }), 0)
  assert.equal(calls.length, 1, 'retry ran more than one group')
  await assert.rejects(main(['1/12', '--last-failed'], { manifest, env: retryEnv, run, out: () => {} }), /cannot combine/)
})

test('ungated groups stay declared and checked but only run with --all', () => {
  const manifest = fixture()
  manifest.groups[1].gate = false
  const gate = balanceShards(manifest, 2), all = balanceShards(manifest, 2, { all: true })
  assert.deepEqual(gate.flatMap(s => s.specs.map(t => t.groupId)).sort(), ['a', 'a', 'a'])
  assert.equal(all.flatMap(s => s.specs).length, 6)
  assert.deepEqual(checkCoverage(manifest, gate, files(manifest)), { specs: 3, shards: 2, ungated: 3 })
  assert.deepEqual(checkCoverage(manifest, all, files(manifest), { all: true }), { specs: 6, shards: 2 })
  assert.throws(() => checkCoverage(manifest, all, files(manifest)), /Ungated spec in a gate shard/)
  assert.throws(() => checkCoverage(manifest, gate, files(manifest), { all: true }), /Spec missing from shards/)
  assert.throws(() => checkCoverage(manifest, gate, files(manifest).slice(1)), /Stale manifest spec/)
  manifest.groups[1].gate = 'no'
  assert.throws(() => validateManifest(manifest), /Invalid gate/)
  assert.equal(parseArgs(['--all', '1/12']).all, true)
  assert.equal(parseArgs(['1/12']).all, undefined)
  const real = loadManifest()
  assert.equal(real.groups.find(g => g.id === 'remaining-ui').gate, false)
  const gated = balanceShards(real, 12).flatMap(s => s.specs)
  assert.ok(gated.some(spec => spec.file === 'tests/knowledge.spec.ts'), 'Decision Knowledge regressions must gate CI')
  assert.equal(gated.length, files(real).length - real.groups.find(g => g.id === 'remaining-ui').specs.length)
  assert.ok(Math.max(...balanceShards(real, 12).map(s => s.weightSeconds)) < 300, 'gate shard exceeds five minutes of test time')
})

test('runtime reconciliation leaves new specs ungated, drops removed specs and reports drift', async () => {
  const manifest = fixture()
  const discovered = [...files(manifest).filter(file => file !== 'tests/a-0.spec.ts'), 'tests/brand-new.spec.ts', 'tests/quotes/also-new.spec.ts']
  const { manifest: reconciled, unlisted, stale } = reconcileManifest(manifest, discovered)
  assert.deepEqual(unlisted, ['tests/brand-new.spec.ts', 'tests/quotes/also-new.spec.ts'])
  assert.deepEqual(stale, ['tests/a-0.spec.ts'])
  const group = reconciled.groups.find(g => g.id === 'unlisted')
  assert.equal(group.gate, false, 'unlisted specs have no hosted-runner evidence and must not gate')
  assert.deepEqual(group.specs.map(s => s.file), unlisted)
  const shards = balanceShards(reconciled, 3)
  assert.deepEqual(checkCoverage(reconciled, shards, discovered), { specs: discovered.length - unlisted.length, shards: 3, ungated: unlisted.length })
  assert.deepEqual(checkCoverage(reconciled, balanceShards(reconciled, 3, { all: true }), discovered, { all: true }), { specs: discovered.length, shards: 3 })
  assert.ok(!files(reconciled).includes('tests/a-0.spec.ts'))
  // A fully clean tree adds nothing.
  assert.equal(reconcileManifest(manifest, files(manifest)).manifest.groups.length, manifest.groups.length)
  assert.throws(() => reconcileManifest({ ...manifest, groups: [...manifest.groups, { ...group, id: 'unlisted' }] }, [...discovered, 'tests/x.spec.ts']), /reserved/)
  assert.equal(parseArgs(['--check', '--strict']).strict, true)
  // The real manifest keeps main's gates and any additional branch specs.
  const out = []
  assert.equal(await main(['--check', '--shards', '12'], { out: s => out.push(s), env: {} }), 0)
  const declared = loadManifest().groups.filter(group => group.gate !== false).flatMap(group => group.specs)
  assert.equal(JSON.parse(out.pop()).specs, declared.length)
})

test('main --strict rejects unlisted-only, stale-only and combined drift and accepts a clean tree', async () => {
  const root = mkdtempSync(resolve(tmpdir(), 'ci-web-shard-'))
  try {
    mkdirSync(resolve(root, 'tests'))
    writeFileSync(resolve(root, 'playwright.ui.config.ts'), '')
    const manifest = { version: 1, groups: [group('a', [9, 8])] }
    const place = names => { for (const f of readdirSync(resolve(root, 'tests'))) rmSync(resolve(root, 'tests', f)); for (const n of names) writeFileSync(resolve(root, 'tests', n), '') }
    const strict = () => main(['--check', '--strict', '--shards', '2'], { manifest, root, out: () => {}, env: {} })
    place(['a-0.spec.ts', 'a-1.spec.ts'])
    assert.equal(await strict(), 0)
    place(['a-0.spec.ts', 'a-1.spec.ts', 'new.spec.ts'])
    await assert.rejects(strict(), /Manifest drift: 1 unlisted, 0 stale.*new\.spec\.ts/)
    place(['a-0.spec.ts'])
    await assert.rejects(strict(), /Manifest drift: 0 unlisted, 1 stale.*a-1\.spec\.ts/)
    place(['a-0.spec.ts', 'new.spec.ts'])
    await assert.rejects(strict(), /Manifest drift: 1 unlisted, 1 stale/)
    const lines = []
    assert.equal(await main(['--check', '--shards', '2'], { manifest, root, out: s => lines.push(s), env: {} }), 0)
    assert.deepEqual(JSON.parse(lines.pop()).unlisted, ['tests/new.spec.ts'])
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('a source manifest cannot declare the reserved unlisted group', () => {
  const root = mkdtempSync(resolve(tmpdir(), 'ci-web-shard-reserved-'))
  try {
    const file = resolve(root, 'manifest.json')
    writeFileSync(file, JSON.stringify({ version: 1, groups: [group('unlisted', [1])] }))
    assert.throws(() => loadManifest(file), /reserved group id: unlisted/)
  } finally { rmSync(root, { recursive: true, force: true }) }
})
