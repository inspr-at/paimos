// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { collectBrowserNow, flattenBrowser } from './collect.mjs'
import { browserBatches, browserCaseSeconds } from './web-batches.mjs'
import { plan, run } from './cli.mjs'
import { key } from './core.mjs'

const row = (file, id) => ({ kind: 'browser', file: `tests/${file}.spec.ts`, name: id, id, line: 10,
  config: 'playwright.ui.config.ts', project: '', tier: 'GATED-FULL', active: true })
const group = (id, options = {}) => ({ id, config: 'playwright.ui.config.ts', project: null,
  hostedOnly: true, flags: ['--workers=1'], env: {}, specs: [{ file: `tests/${id}.spec.ts` }], ...options })
const noFlaky = { version: 1, entries: [] }
const native = (rows, status = () => 'passed', duration = 1000) => ({ errors: [], suites: [{ specs: rows.map(row => ({
  id: row.id, title: row.name, tests: [{ projectName: row.project, results: [{ status: status(row), duration }] }],
})) }] })

test('AEON-1023 browser collector invokes only native browser lists and retains config/project registrations', () => {
  const calls = []
  const report = file => ({ errors: [], suites: [{ title: `${file}.spec.ts`, specs: [{
    file: `${file}.spec.ts`, title: 'registered case', id: file, line: 12, tests: [{ projectName: 'chromium' }],
  }] }] })
  const ui = { errors: [], suites: [...report('ui').suites, ...report('performance').suites] }
  const perf = report('performance')
  const rows = collectBrowserNow({ run: (_bin, args) => {
    calls.push(args)
    assert.ok(args.includes('--list'))
    assert.equal(args[0], 'node_modules/@playwright/test/cli.js')
    return JSON.stringify(args.includes('playwright.ui.config.ts') ? ui : perf)
  } })
  assert.deepEqual(calls.map(args => args[args.indexOf('-c') + 1]), ['playwright.ui.config.ts', 'playwright.perf.config.ts'])
  assert.deepEqual(rows, [...flattenBrowser(ui, 'playwright.ui.config.ts').filter(row => row.file !== 'tests/performance.spec.ts'),
    ...flattenBrowser(perf, 'playwright.perf.config.ts')])
  assert.equal(rows.length, 2)
  assert.throws(() => collectBrowserNow({ run: () => JSON.stringify({ errors: [{ message: 'registration failed' }] }) }), /registration failed/)
})

test('AEON-1023 compatible policies share a launch; conflicting configs/projects/env/flags/host rules remain isolated', () => {
  const rows = [row('a', 'a'), row('b', 'b'), row('c', 'c'), row('d', 'd')]
  const policy = { groups: [group('a', { env: { AEON_DESK_SHOTS: '${RUNNER_TEMP}/a' } }),
    group('b', { flags: ['--workers=2', '--trace=retain-on-failure'], env: { RELEASE_LIST_SHOTS: '${RUNNER_TEMP}/b' } }),
    group('c', { env: { AEON_DESK_SHOTS: '${RUNNER_TEMP}/other' } }),
    group('d', { flags: ['--timeout=5000'] })] }
  const batches = browserBatches(rows, policy)
  assert.deepEqual(batches.map(batch => batch.groups), [['a'], ['b'], ['c'], ['d']])
  assert.deepEqual(batches[0].env, { AEON_DESK_SHOTS: '${RUNNER_TEMP}/a' })
  assert.deepEqual(batches[0].flags, [], 'a group that did not request trace stays untraced')
  assert.deepEqual(batches[1].env, { RELEASE_LIST_SHOTS: '${RUNNER_TEMP}/b' })
  assert.deepEqual(batches[1].flags, ['--trace=retain-on-failure'])
  assert.deepEqual(batches.flatMap(batch => batch.rows).map(key).sort(), rows.map(key).sort())
  for (const different of [{ config: 'playwright.perf.config.ts' }, { project: 'chromium' }, { hostedOnly: false },
    { flags: ['--trace=on'] }, { env: { REGISTRATION_MODE: 'enabled' } }]) {
    const second = { ...rows[1], config: different.config ?? rows[1].config, project: different.project ?? '' }
    assert.equal(browserBatches([rows[0], second], { groups: [group('a'), group('b', different)] }).length, 2)
  }
  assert.throws(() => browserBatches(rows, { groups: [group('a')] }), /Missing browser policy owner/)
  assert.throws(() => browserBatches([rows[0]], { groups: [group('a'), group('duplicate', { specs: [{ file: rows[0].file }] })] }), /Duplicate browser policy owner/)
  assert.throws(() => browserBatches([rows[0]], { groups: [group('a', { config: 'playwright.perf.config.ts' })] }), /Browser launch policy mismatch/)
  assert.equal(browserBatches(rows.slice(0, 2), { groups: [group('a', { env: { MODE: 'x', OTHER: 'y' } }),
    group('b', { env: { OTHER: 'y', MODE: 'x' } })] }).length, 1, 'identical execution policy is order-independent')
})

test('AEON-1023 the same trace policy still shares one launch and unions screenshot variables', () => {
  const rows = [row('a', 'a'), row('b', 'b'), row('c', 'c'), row('d', 'd')]
  const traced = { flags: ['--workers=1', '--trace=retain-on-failure'] }
  const batches = browserBatches(rows, { groups: [
    group('a', { ...traced, env: { AEON_DESK_SHOTS: '${RUNNER_TEMP}/a' } }),
    group('b', { ...traced, env: { RELEASE_LIST_SHOTS: '${RUNNER_TEMP}/b' } }),
    group('c', { env: { AEON_DESK_SHOTS: '${RUNNER_TEMP}/c' } }),
    group('d', { env: { RELEASE_LIST_SHOTS: '${RUNNER_TEMP}/d' } }),
  ] })
  assert.deepEqual(batches.map(batch => batch.groups), [['a', 'b'], ['c', 'd']])
  assert.deepEqual(batches[0].flags, ['--trace=retain-on-failure'])
  assert.deepEqual(batches[1].flags, [])
  assert.deepEqual(batches[0].env, { AEON_DESK_SHOTS: '${RUNNER_TEMP}/a', RELEASE_LIST_SHOTS: '${RUNNER_TEMP}/b' })
  assert.deepEqual(batches[1].env, { AEON_DESK_SHOTS: '${RUNNER_TEMP}/c', RELEASE_LIST_SHOTS: '${RUNNER_TEMP}/d' })
})

test('AEON-1023 a neighbour trace request does not trace the routed-panel launch', () => {
  // Shard 6 of run 37923530553 inherited retain-on-failure onto dispatch.
  // The routed panel case then passed 17 of 24 viewports and hit its 120s budget.
  const rows = [row('ticket-panel', 'routed'), row('access', 'dialogs')]
  const batches = browserBatches(rows, { groups: [
    group('dispatch', { specs: [{ file: 'tests/ticket-panel.spec.ts' }] }),
    group('access-dialogs', { flags: ['--workers=2', '--trace=retain-on-failure'], specs: [{ file: 'tests/access.spec.ts' }] }),
  ] })
  assert.equal(batches.length, 2)
  assert.deepEqual(batches.find(batch => batch.groups.includes('dispatch')).flags, [])
  assert.ok(batches.find(batch => batch.groups.includes('access-dialogs')).flags.includes('--trace=retain-on-failure'))
})

test('AEON-1023 browser-only discovery preserves full/catalogue/changed-area decisions and every shard case', async () => {
  const manifest = JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json', import.meta.url)))
  const inventory = { tests: manifest.tests.map((entry, index) => ({ ...entry, id: `native-${index}`, line: index + 1, active: true })) }
  const browser = { scope: 'browser', tests: inventory.tests.filter(row => row.kind === 'browser') }
  const policies = JSON.parse(readFileSync(new URL('../../web/ci-web-shards.json', import.meta.url)))
  const compare = options => {
    const before = plan('web', { ...options, inventory })
    const requested = []
    const after = plan('web', options, { collect: (kind, opts) => { requested.push({ kind, opts }); return browser } })
    assert.deepEqual(requested, [{ kind: 'web', opts: { browserOnly: true } }])
    assert.equal(after.full, before.full)
    assert.equal(after.reason, before.reason)
    assert.equal(after.layout, before.layout)
    assert.equal(after.scope, before.scope)
    assert.equal(after.deferredBrowserCases, before.deferredBrowserCases)
    assert.deepEqual(after.tests.map(key), before.tests.map(key))
    return { before, after }
  }
  // A full twelve-shard execution through the real runner with native-result
  // fixtures proves batching never drops, duplicates, or changes case identity.
  const beforeKeys = [], executed = []
  for (let index = 1; index <= 12; index++) {
    const { before, after } = compare({ full: true, event: 'merge_group', count: 12, index })
    beforeKeys.push(...before.tests.map(key))
    const batches = browserBatches(after.tests, policies)
    const chosen = args => {
      const list = readFileSync(args[args.indexOf('--test-list') + 1], 'utf8')
      return after.tests.filter(row => list.includes(`[${row.project}] › ${row.file.slice(6)}\n`) ||
        list.includes(`[${row.project}] › ${row.file.slice(6)}:${row.line} › ${row.name}\n`))
    }
    let report
    assert.equal(await run('web', after, { job: `aeon1023-proof-${index}`, env: { RUNNER_ENVIRONMENT: 'github-hosted' } }, {
      knownFlaky: noFlaky, loadBrowserPolicy: () => policies, log: () => {}, saveJSON: (_path, value) => { report = value },
      command: (_bin, args) => JSON.stringify(native(chosen(args))),
      runPlaywright: async (args, { env }) => {
        const selected = chosen(args)
        executed.push(...selected.map(key))
        writeFileSync(env.PLAYWRIGHT_JSON_OUTPUT_FILE, JSON.stringify(native(selected)))
        return { code: 0 }
      },
    }), 0)
    assert.equal(report.webShardTiming.launches.length, batches.length)
    assert.equal(report.browserCases.length, after.tests.length)
    assert.ok(report.browserCases.every(row => row.status === 'passed'))
  }
  assert.equal(new Set(executed).size, executed.length)
  assert.deepEqual(executed.sort(), beforeKeys.sort())
  assert.deepEqual([...new Set(executed.map(id => id.split(':')[1]))].sort(),
    [...new Set(inventory.tests.filter(row => row.kind === 'browser' && row.tier !== 'NIGHTLY').map(row => row.file))].sort())
  for (const options of [
    { all: true, event: 'schedule' },
    { event: 'pull_request', paths: ['web/tests/clip-tip.spec.ts'] },
    { event: 'pull_request', paths: ['web/tests/tree.test.ts'], affectedLane: 'on' },
    { event: 'pull_request', paths: ['web/src/tree-drop.ts'], affectedLane: 'on' },
    { event: 'pull_request', paths: ['scripts/ci/web-test-tiers.json'], affectedLane: 'on', plannerMode: 'essential', plannerLayout: 'static' },
  ]) compare(options)
  const unitCalls = []
  plan('web', { unit: true, full: true }, { collect: (_kind, options) => { unitCalls.push(options); return inventory } })
  assert.deepEqual(unitCalls, [{ browserOnly: false }], 'unit collection remains complete')
})

test('AEON-1023 combined retries stay exact and timing separates collection/native lists/run/cases', async () => {
  const rows = [row('a', 'a'), row('b', 'b')], calls = []
  let clock = 0, report
  const chosen = args => {
    const list = readFileSync(args[args.indexOf('--test-list') + 1], 'utf8')
    return rows.filter(row => list.includes(`${row.file.slice(6)}\n`) || list.includes(`${row.file.slice(6)}:${row.line} › ${row.name}\n`))
  }
  assert.equal(await run('web', { tests: rows, all: rows, timing: { collectSeconds: 7, planSeconds: 2 } }, {
    job: 'aeon1023-retry', env: { GITHUB_EVENT_NAME: 'merge_group', RUNNER_ENVIRONMENT: 'github-hosted' },
  }, {
    knownFlaky: noFlaky, loadBrowserPolicy: () => ({ groups: [group('a'), group('b')] }),
    now: () => clock, log: () => {}, saveJSON: (_path, value) => { report = value },
    command: (_bin, args) => { clock += 1000; return JSON.stringify(native(chosen(args))) },
    runPlaywright: async (args, { env }) => {
      clock += 5000
      const selected = chosen(args), retry = calls.length > 0
      calls.push(selected.map(key))
      writeFileSync(env.PLAYWRIGHT_JSON_OUTPUT_FILE, JSON.stringify(native(selected, row => !retry && row.id === 'a' ? 'failed' : 'passed')))
      return { code: retry ? 0 : 1 }
    },
  }), 0)
  assert.deepEqual(calls, [rows.map(key), [key(rows[0])]])
  assert.equal(report.classes['GATED-FULL'].flaky, 1)
  assert.equal(report.webShardTiming.collectSeconds, 7)
  assert.equal(report.webShardTiming.runSeconds, 12)
  assert.equal(report.webShardTiming.caseSeconds, 3)
  assert.equal(report.webShardTiming.nonCaseSeconds, 18)
  assert.deepEqual(report.webShardTiming.launches.map(({ listSeconds, runSeconds, caseSeconds, retry }) => ({ listSeconds, runSeconds, caseSeconds, retry })),
    [{ listSeconds: 1, runSeconds: 5, caseSeconds: 2, retry: false }, { listSeconds: 1, runSeconds: 5, caseSeconds: 1, retry: true }])
  assert.equal(browserCaseSeconds(native(rows)), 2)
  assert.equal(browserCaseSeconds({ suites: [{ specs: [{ tests: [{ results: [{ status: 'passed' }] }] }] }] }), null)
})
