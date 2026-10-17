// SPDX-License-Identifier: AGPL-3.0-only
// Fixed go-static selector lane: imported by test-tiers.test.mjs.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, existsSync, symlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { EventEmitter } from 'node:events'
import { shadowPlan, registryImpact, ownershipGraph, main as planMain } from './impact-plan.mjs'
import { buildBrowserMap, browserIdentity, dependencyFingerprint, nightlyBrowserMap } from './browser-impact.mjs'
import { captureBrowser } from './browser-coverage.mjs'
import { graphIncomplete, select, fullWebShardCount } from './core.mjs'
import { schedulingDecision, effectiveLane } from './diff.mjs'
import { runnerSelection } from './cli.mjs'
import { classifyPR, requireResults } from '../ci-pr-plan.mjs'
import { decide, decideMergeGroup, main as reuseMain } from '../ci-tree-reuse.mjs'

const tree = 'a'.repeat(40), otherTree = 'b'.repeat(40), base = 'c'.repeat(40), fingerprint = `browser-deps-v1:${'d'.repeat(64)}`
const row = (file, name = file, tier = 'GATED-FULL') => ({ kind: 'browser', file, name, tier, config: 'playwright.ui.config.ts', project: '' })
const tests = [row('tests/essential.spec.ts', 'essential', 'ESSENTIAL'), row('tests/import.spec.ts'), row('tests/navigation.spec.ts'), row('tests/other.spec.ts'), row('tests/ungated.spec.ts', 'ungated', 'NIGHTLY')]
const graph = { 'src/screen.ts': [], 'src/main.ts': ['src/router.ts'], 'src/router.ts': ['src/screen.ts'],
  ...Object.fromEntries(tests.map(test => [test.file, test.file === 'tests/import.spec.ts' ? ['src/screen.ts'] : []])) }
const map = () => buildBrowserMap([{ version: 1, tree: otherTree, fingerprint, complete: true, runId: '42', attempt: '1', cases: tests.map(test => ({ ...test, complete: true, status: 'passed', sources: test.file === 'tests/navigation.spec.ts' ? ['src/screen.ts'] : [] })) }], { tree: otherTree, fingerprint, runId: '42', attempt: '1', catalogue: tests })
const plan = options => shadowPlan({ paths: ['web/src/screen.ts'], tests, graph, browserMap: map(), baseTree: tree, fingerprint, ...options })
const manifest = rows => ({ version: 1, tests: rows })
const policy = () => ({ version: 1, groups: [{ id: 'ui', config: 'playwright.ui.config.ts', project: null, flags: [], env: {}, hostedOnly: true,
  specs: [{ file: 'tests/import.spec.ts', weightSeconds: 10 }] }], exclusions: [] })

test('AEON-1333 shadow selection unions essential, import and observed navigation owners and accounts each selected/skipped case once', () => {
  const result = plan()
  assert.equal(result.version, 1); assert.equal(result.mode, 'shadow'); assert.equal(result.browser_scope, 'affected')
  assert.deepEqual(result.selected_cases, tests.slice(0, 3).map(browserIdentity).sort())
  assert.deepEqual(result.would_have_skipped, { count: 1, cases: [browserIdentity(tests[3])] })
  assert.deepEqual(result.shards.flatMap(shard => shard.cases).sort(), result.selected_cases)
  assert.deepEqual(result, plan(), 'deterministic shard/case evidence')
  assert.deepEqual(result.execution, { mode: 'full', layout: 'full', lane: 'full' })
})

test('AEON-1334 unknown timing fields fail wide and fixture markdown is not documentation', () => {
  const previous = manifest(tests)
  const hidden = { ...previous, timingWeights: { owners: { 'tests/import.spec.ts': { seconds: 9, selectedTests: 1, env: { OMIT: '1' } } } } }
  assert.equal(registryImpact('scripts/ci/web-test-tiers.json', previous, hidden).full, true)
  const browser = policy()
  const measured = structuredClone(browser)
  measured.groups[0].specs[0].tierTiming = { seconds: 1, selectedTests: 1 }
  assert.equal(registryImpact('web/ci-web-shards.json', browser, measured).full, false)
  const nested = structuredClone(browser)
  nested.groups[0].specs[0].tierTiming = { seconds: 1, selectedTests: 1, env: { OMIT: '1' } }
  assert.equal(registryImpact('web/ci-web-shards.json', browser, nested).full, true)
  assert.notEqual(plan({ paths: ['web/tests/fixtures/example.md'] }).browser_scope, 'none')
  assert.equal(plan({ paths: ['docs/features/example.md'] }).browser_scope, 'none')
})

test('AEON-1333 docs and pure timing registries propose no browser shards; promotions execute including formerly nightly cases', () => {
  for (const paths of [['docs/features/example.md'], ['README.md', 'LICENSE']]) {
    const result = plan({ paths }); assert.equal(result.browser_scope, 'none'); assert.equal(result.would_have_skipped.count, 4)
  }
  const before = policy(), after = structuredClone(before); after.groups[0].specs[0].weightSeconds = 12
  let result = plan({ paths: ['web/ci-web-shards.json'], registries: { 'web/ci-web-shards.json': { before, after } } })
  assert.equal(result.browser_scope, 'none')
  const previous = manifest(tests), promoted = structuredClone(previous); promoted.tests[4].tier = 'GATED-FULL'
  result = plan({ paths: ['scripts/ci/web-test-tiers.json'], tests: promoted.tests,
    registries: { 'scripts/ci/web-test-tiers.json': { before: previous, after: promoted } } })
  assert.equal(result.browser_scope, 'affected')
  assert.ok(result.selected_cases.includes(browserIdentity(tests[4])))
  const timing = { ...previous, timingWeights: { owners: { 'tests/import.spec.ts': { seconds: 9, selectedTests: 1 } } } }
  assert.equal(registryImpact('scripts/ci/web-test-tiers.json', previous, timing).full, false)
})

test('AEON-1333 registry exclusions, flags, environment, project, ownership, config, membership and demotions widen', () => {
  const edits = [value => value.groups[0].flags.push('--workers=2'), value => value.groups[0].env.MODE = 'other',
    value => value.groups[0].project = 'other', value => value.groups[0].config = 'playwright.perf.config.ts',
    value => value.groups[0].id = 'new-owner', value => value.groups[0].gate = false,
    value => value.exclusions.push({ file: 'tests/excluded.spec.ts', ticket: 'AEON-1333', reason: 'review' }),
    value => value.groups[0].specs[0].file = 'tests/renamed.spec.ts', value => value.groups[0].specs[0].weightSeconds = -1]
  for (const edit of edits) { const after = policy(); edit(after); assert.equal(registryImpact('web/ci-web-shards.json', policy(), after).full, true) }
  const old = manifest(tests)
  for (const edit of [value => value.tests[0].tier = 'NIGHTLY', value => value.tests.pop(),
    value => value.tests[0].project = 'other', value => value.defaultNewTier = 'ESSENTIAL']) {
    const after = structuredClone(old); edit(after)
    assert.equal(registryImpact('scripts/ci/web-test-tiers.json', old, after).full, true)
  }
  assert.equal(registryImpact('web/ci-web-shards.json', undefined, policy()).full, true)
})

test('AEON-1389 shard records follow the assembled browser policy: timing stays reduced and ownership widens', t => {
  const record = 'web/scripts/ci-web-shard-specs/tests/import.spec.ts.json'
  const before = policy(), timed = structuredClone(before)
  timed.groups[0].specs[0].weightSeconds = 12
  const timing = plan({ paths: [record], registries: { 'web/ci-web-shards.json': { before, after: timed } } })
  assert.equal(timing.browser_scope, 'none')
  assert.equal(timing.fallback_reasons.length, 0)
  const renamed = structuredClone(before)
  renamed.groups[0].specs[0].file = 'tests/renamed.spec.ts'
  const ownership = plan({ paths: [record], registries: { 'web/ci-web-shards.json': { before, after: renamed } } })
  assert.equal(ownership.browser_scope, 'gated-full')
  assert.match(ownership.fallback_reasons.join(), /ownership, exclusion or execution policy/)
  const grouped = structuredClone(before)
  grouped.groups[0].id = 'moved'
  assert.equal(plan({ paths: [record], registries: { 'web/ci-web-shards.json': { before, after: grouped } } }).browser_scope, 'gated-full')
  assert.equal(plan({ paths: [record] }).browser_scope, 'gated-full', 'missing assembled policies fail closed')

  const checkout = mkdtempSync(resolve(tmpdir(), 'aeon-1389-record-'))
  const trusted = mkdtempSync(resolve(tmpdir(), 'aeon-1389-base-'))
  t.after(() => { rmSync(checkout, { recursive: true, force: true }); rmSync(trusted, { recursive: true, force: true }) })
  const write = (file, text) => { mkdirSync(resolve(checkout, file, '..'), { recursive: true }); writeFileSync(resolve(checkout, file), text) }
  const git = args => execFileSync('git', args, { cwd: checkout, encoding: 'utf8', timeout: 10_000 }).trim()
  const recordOf = (file, group, weightSeconds) => JSON.stringify({ group, spec: { file, weightSeconds } })
  const launch = { version: 1, specSource: 'scripts/ci-web-shard-specs', exclusions: [], groups: ['ui', 'other'].map(id => ({
    id, config: 'playwright.ui.config.ts', project: null, flags: [], env: {}, hostedOnly: true, specs: [] })) }
  const catalogue = manifest([row('tests/import.spec.ts'), row('tests/stay.spec.ts'), row('tests/other.spec.ts')])
  write('scripts/ci/web-test-tiers.json', JSON.stringify(catalogue))
  write('scripts/ci/known-flaky.json', JSON.stringify({ version: 1, entries: [] }))
  write('web/ci-web-shards.json', JSON.stringify(launch))
  write('web/scripts/ci-web-shard-specs/tests/import.spec.ts.json', recordOf('tests/import.spec.ts', 'ui', 10))
  write('web/scripts/ci-web-shard-specs/tests/stay.spec.ts.json', recordOf('tests/stay.spec.ts', 'ui', 10))
  write('web/scripts/ci-web-shard-specs/tests/other.spec.ts.json', recordOf('tests/other.spec.ts', 'other', 10))
  for (const dir of ['web/src', 'web/tests', 'web/e2e']) mkdirSync(resolve(checkout, dir), { recursive: true })
  git(['init', '-q']); git(['config', 'user.name', 'Fixture']); git(['config', 'user.email', 'fixture@example.invalid'])
  git(['add', '.']); git(['commit', '-qm', 'base'])
  execFileSync('tar', ['-x', '-C', trusted], { input: execFileSync('git', ['archive', 'HEAD'], { cwd: checkout }), timeout: 10_000 })
  const base = git(['rev-parse', 'HEAD'])
  write('web/scripts/ci-web-shard-specs/tests/import.spec.ts.json', recordOf('tests/import.spec.ts', 'ui', 12))
  git(['add', 'web/scripts/ci-web-shard-specs/tests/import.spec.ts.json']); git(['commit', '-qm', 'weight'])
  const head = git(['rev-parse', 'HEAD'])
  const envFor = sha => ({ AEON_IMPACT_CHECKOUT: checkout, PR_BASE_SHA: base, PR_HEAD_SHA: sha, GITHUB_SHA: sha,
    GITHUB_RUN_ID: '42', GITHUB_RUN_ATTEMPT: '1', GITHUB_EVENT_NAME: 'pull_request', EXECUTION_MODE: 'full', EXECUTION_LAYOUT: 'full', EXECUTION_LANE: 'full' })
  const weighted = planMain(envFor(head), { trustedRoot: trusted, browserCoverage: () => { throw new Error('a weight record must not fetch observations') } })
  assert.equal(weighted.browser_scope, 'none')
  assert.equal(weighted.fallback_reasons.length, 0)
  assert.match(weighted.reasons.join(), /timing-only browser registry/)
  write('web/scripts/ci-web-shard-specs/tests/import.spec.ts.json', recordOf('tests/import.spec.ts', 'other', 12))
  git(['add', 'web/scripts/ci-web-shard-specs/tests/import.spec.ts.json']); git(['commit', '-qm', 'group'])
  const moved = git(['rev-parse', 'HEAD'])
  const regrouped = planMain(envFor(moved), { trustedRoot: trusted, browserCoverage: () => { throw new Error('a group change must not fetch observations') } })
  assert.equal(regrouped.browser_scope, 'gated-full')
  assert.match(regrouped.fallback_reasons.join(), /ownership, exclusion or execution policy/)
})

test('AEON-1333 browser selectors fail wide on missing evidence, bad fingerprints, incomplete graphs, new/deleted inputs, assets and shared inputs', () => {
  const variants = [{ paths: undefined }, { paths: [] }, { paths: ['../escape'] }, { paths: Array(10001).fill('README.md') },
    { paths: ['web/src/new.ts'] }, { paths: ['web/src/style.css'] }, { paths: ['web/src/image.svg'] }, { paths: ['web/src/data.json'] },
    ...['AGENTS.md', 'api/openapi.yaml', 'internal/auth/auth.go', 'internal/db/migrations/0001_x.sql', 'version.json', 'web/vite.config.ts', 'web/package.json', '.github/workflows/ci.yml', 'scripts/ci/go-test-tiers.json'].map(file => ({ paths: [file] })),
    { browserMap: undefined }, { fingerprint: `browser-deps-v1:${'e'.repeat(64)}` }, { browserMap: { ...map(), complete: false } },
    { graph: { ...graph, [graphIncomplete]: 'bounded scan failed' } }, { paths: ['web/tests/no-owner.html'], graph: { ...graph, 'tests/no-owner.html': [] } }]
  for (const variant of variants) { const result = plan(variant); assert.equal(result.browser_scope, 'gated-full', JSON.stringify(variant.paths)); assert.ok(result.fallback_reasons.length); assert.equal(result.would_have_skipped.count, 0); assert.deepEqual(result.selected_cases, tests.slice(0, 4).map(browserIdentity).sort()) }
  for (const event of ['merge_group', 'push', 'schedule', 'workflow_dispatch']) assert.equal(plan({ event }).browser_scope, 'gated-full')
  const many = [...tests, ...Array.from({ length: 301 }, (_, i) => row('tests/import.spec.ts', `extra ${i}`))]
  assert.match(plan({ tests: many }).fallback_reasons.join(), /fan-out/)
  const noOwner = { ...graph, 'src/unobserved.ts': [] }
  assert.equal(plan({ paths: ['web/src/unobserved.ts'], graph: noOwner }).browser_scope, 'gated-full')
})

test('AEON-1333 route and URL harness ownership cover non-importing specs and computed imports widen conservatively', () => {
  const imports = { ...graph, 'tests/page.html': [], 'tests/harness.ts': ['src/screen.ts'] }
  const source = file => file === 'tests/navigation.spec.ts' ? "await page.goto('/'); await page.goto('/tests/page.html')"
    : file === 'tests/page.html' ? '<script src="./harness.ts"></script>' : ''
  const owned = ownershipGraph('.', imports, source)
  assert.ok(owned['tests/page.html'].includes('tests/harness.ts'))
  assert.ok(owned['tests/navigation.spec.ts'].includes('src/router.ts'))
  const selected = plan({ graph: owned, paths: ['web/tests/harness.ts'], browserMap: undefined })
  assert.equal(selected.browser_scope, 'affected'); assert.ok(selected.selected_cases.includes(browserIdentity(tests[2])))
  const computed = ownershipGraph('.', imports, file => file === 'tests/other.spec.ts' ? 'await import(path)' : source(file))
  assert.ok(computed['tests/other.spec.ts'].includes('src/screen.ts'))
  assert.ok(ownershipGraph('.', imports, file => file === 'tests/page.html' ? '<script src="https://outside/a.js"></script>' : '')[graphIncomplete])
  assert.ok(ownershipGraph('.', imports, () => undefined)[graphIncomplete])
  assert.match(ownershipGraph('.', imports, source, { maxEdges: 1 })[graphIncomplete], /edge count bound/)
  const synthetic = ownershipGraph('.', imports, file => file === 'tests/other.spec.ts' ? "await page.goto('/tests/synthetic-inline.html')" : source(file))
  assert.ok(synthetic['tests/other.spec.ts'].includes('src/screen.ts'), 'synthetic URLs retain every possible local dependency')
})

test('AEON-1333 real planner reads the bounded catalogue, binds checkout identity and publishes full case/shard/skip evidence without executing head code', t => {
  const checkout = mkdtempSync(resolve(tmpdir(), 'aeon-impact-main-'))
  t.after(() => rmSync(checkout, { recursive: true, force: true }))
  const write = (file, text) => { mkdirSync(resolve(checkout, file.slice(0, file.lastIndexOf('/'))), { recursive: true }); writeFileSync(resolve(checkout, file), text) }
  const git = args => execFileSync('git', args, { cwd: checkout, encoding: 'utf8', timeout: 10_000 }).trim()
  git(['init', '-q']); git(['config', 'user.name', 'Fixture']); git(['config', 'user.email', 'fixture@example.invalid'])
  // Padding proves the actual current catalogue's >2 MiB size remains usable.
  write('scripts/ci/web-test-tiers.json', JSON.stringify({ ...manifest(tests), padding: 'x'.repeat(2 * 1024 * 1024) }))
  write('scripts/ci/known-flaky.json', JSON.stringify({ version: 1, entries: [] }))
  const launch = policy(); launch.groups[0].specs = tests.map(row => ({ file: row.file, weightSeconds: 1 }))
  write('web/ci-web-shards.json', JSON.stringify(launch))
  for (const file of Object.keys(graph)) write(`web/${file}`, '')
  mkdirSync(resolve(checkout, 'web/e2e'))
  write('docs/example.md', 'base'); git(['add', '.']); git(['commit', '-qm', 'base'])
  const base = git(['rev-parse', 'HEAD'])
  write('docs/example.md', 'head'); git(['add', '.']); git(['commit', '-qm', 'docs'])
  const head = git(['rev-parse', 'HEAD'])
  const env = { AEON_IMPACT_CHECKOUT: checkout, PR_BASE_SHA: base, PR_HEAD_SHA: head, GITHUB_SHA: head,
    GITHUB_RUN_ID: '42', GITHUB_RUN_ATTEMPT: '1', GITHUB_EVENT_NAME: 'pull_request', EXECUTION_MODE: 'full', EXECUTION_LAYOUT: 'full', EXECUTION_LANE: 'full' }
  const noCoverage = () => { throw new Error('documentation must not fetch observations') }
  const result = planMain(env, { trustedRoot: checkout, browserCoverage: noCoverage })
  assert.equal(result.complete, true); assert.equal(result.browser_scope, 'none'); assert.equal(result.would_have_skipped.count, 4)
  assert.deepEqual(JSON.parse(readFileSync(resolve(checkout, 'tmp/test-tiers/impact-plan.json'))), result)
  assert.equal(result.identity.base_sha, base); assert.equal(result.identity.head_sha, head); assert.match(result.identity.checkout_tree, /^[a-f0-9]{40}$/)
  assert.throws(() => planMain({ ...env, GITHUB_SHA: tree }, { trustedRoot: checkout }), /unbound shadow identity/)
  write('scripts/test-tiers/impact-plan.mjs', 'throw new Error("candidate planner must not execute")')
  git(['add', '.']); git(['commit', '-qm', 'candidate code'])
  const candidate = git(['rev-parse', 'HEAD'])
  const broad = planMain({ ...env, GITHUB_SHA: candidate, PR_HEAD_SHA: candidate }, { trustedRoot: checkout, browserCoverage: noCoverage })
  assert.equal(broad.browser_scope, 'gated-full'); assert.equal(broad.selected_cases.length, 4)
  assert.deepEqual(broad.shards.flatMap(shard => shard.cases).sort(), broad.selected_cases)
})

test('AEON-1333 fingerprint changes with source, routes, fixtures, configuration and catalogue but survives unrelated commits', () => {
  const checkout = mkdtempSync(resolve(tmpdir(), 'aeon-impact-fingerprint-'))
  const git = args => execFileSync('git', args, { cwd: checkout, encoding: 'utf8', timeout: 10_000 })
  try {
    git(['init', '-q']); git(['config', 'user.name', 'Fixture']); git(['config', 'user.email', 'fixture@example.invalid'])
    const files = ['web/src/screen.ts', 'web/src/router.ts', 'web/tests/fixture.ts', 'web/playwright.ui.config.ts', 'scripts/ci/web-test-tiers.json']
    for (const file of [...files, 'docs/other.md']) { mkdirSync(dirnameOf(file), { recursive: true }); writeFileSync(resolve(checkout, file), 'base') }
    function dirnameOf(file) { return resolve(checkout, file.slice(0, file.lastIndexOf('/'))) }
    git(['add', '.']); git(['commit', '-qm', 'base']); const original = dependencyFingerprint(checkout)
    assert.match(original, /^browser-deps-v1:[a-f0-9]{64}$/)
    writeFileSync(resolve(checkout, 'docs/other.md'), 'unrelated'); git(['add', '.']); git(['commit', '-qm', 'docs'])
    assert.equal(dependencyFingerprint(checkout), original)
    for (const file of files) { writeFileSync(resolve(checkout, file), 'change'); git(['add', '.']); git(['commit', '-qm', 'dependency']); assert.notEqual(dependencyFingerprint(checkout), original); writeFileSync(resolve(checkout, file), 'base'); git(['add', '.']); git(['commit', '-qm', 'restore']) }
    assert.equal(dependencyFingerprint(checkout), original)
    assert.equal(dependencyFingerprint(checkout, 'bad'), undefined)
    assert.equal(dependencyFingerprint(checkout, 'HEAD', () => '120000 blob '+tree+'\tweb/link\0'), undefined)
  } finally { rmSync(checkout, { recursive: true, force: true }) }
})

test('AEON-1333 loader accepts different-SHA trusted main evidence with matching fingerprint and rejects untrusted, partial and incompatible evidence', () => {
  const checkout = mkdtempSync(resolve(tmpdir(), 'aeon-impact-loader-'))
  const nightly = { head_sha: 'f'.repeat(40), head_branch: 'main', repository: { full_name: 'fixture/aeon' }, head_repository: { full_name: 'fixture/aeon' }, conclusion: 'success', event: 'schedule', id: 42, run_attempt: 1 }
  const inputs = `100644 blob ${tree}\tweb/src/screen.ts\0`, fp = dependencyFingerprint('.', 'HEAD', () => inputs)
  let candidate = nightly, mismatch = false, partial = false, artifactAttempt = '1', artifactFP = fp
  const calls = [], exec = (command, args) => {
    calls.push([command, args])
    if (command === 'git') {
      if (args[0] === 'ls-tree') return mismatch && args[3] === nightly.head_sha ? `100644 blob ${otherTree}\tweb/src/screen.ts\0` : inputs
      if (args[0] === 'fetch') return ''
      return args[1]?.startsWith(nightly.head_sha) ? otherTree+'\n' : tree+'\n'
    }
    if (command === 'unzip') return args[0] === '-Z1' ? 'browser-impact-web-shard-1.json\n' : JSON.stringify({ version: 1, tree: otherTree, fingerprint: artifactFP, runId: '42', attempt: artifactAttempt, complete: true, cases: tests.slice(partial ? 1 : 0).map(test => ({ ...test, complete: true, status: 'passed', sources: ['src/screen.ts'] })) })
    if (args[0] === 'repo') return JSON.stringify({ nameWithOwner: 'fixture/aeon' })
    if (args[1].includes('/workflows/')) return JSON.stringify({ workflow_runs: [candidate] })
    if (args[1].endsWith('/zip')) return Buffer.from('mock zip')
    return JSON.stringify({ total_count: 1, artifacts: [{ id: 12, name: 'web-shard-1-tier-measurements', expired: false, size_in_bytes: 10 }] })
  }
  const load = () => nightlyBrowserMap(checkout, base, tests, { exec, compatible: true })
  try {
    const result = load(); assert.equal(result.fingerprint, fp); assert.ok(result.browserMap); assert.equal(result.browserMap.tree, otherTree)
    assert.ok(calls.some(([, args]) => args[1]?.includes('branch=main')))
    for (const change of [() => { candidate = { ...nightly, event: 'pull_request' } }, () => { candidate = { ...nightly, head_branch: 'pr' } },
      () => { candidate = { ...nightly, head_repository: { full_name: 'fork/aeon' } } }, () => { mismatch = true }, () => { partial = true },
      () => { artifactAttempt = '2' }, () => { artifactFP = fingerprint }]) {
      candidate = nightly; mismatch = false; partial = false; artifactAttempt = '1'; artifactFP = fp; change(); assert.equal(load().browserMap, undefined)
    }
    assert.equal(nightlyBrowserMap(checkout, base, tests, { exec, compatible: true }).browserMap, undefined)
    assert.equal(nightlyBrowserMap(checkout, base, tests, { exec }).browserMap, undefined, 'S1 execution lookup remains exact-SHA')
  } finally { rmSync(checkout, { recursive: true, force: true }) }
})

test('AEON-1333 production bundle ownership is complete only with all-source fallback and healthy observation channels', async () => {
  const run = async (bundleSources, breakChannel = false) => {
    const session = new EventEmitter(), page = new EventEmitter(), context = new EventEmitter()
    session.send = async method => method === 'Profiler.takePreciseCoverage' ? { result: [] } : undefined
    session.detach = async () => {}; page.isClosed = () => false
    Object.assign(context, { pages: () => [], serviceWorkers: () => [], newPage: async () => page, newCDPSession: async () => session })
    const browser = { browserType: () => ({ name: () => 'chromium' }), contexts: () => [], newContext: async () => context }
    const capture = captureBrowser(browser, { bundleSources }); await (await browser.newContext()).newPage()
    session.emit('Debugger.scriptParsed', { url: 'http://localhost/assets/index-123.js' })
    if (breakChannel) page.emit('worker', {})
    return capture.finish()
  }
  assert.equal((await run()).complete, false)
  assert.deepEqual(await run(['src/screen.ts', 'src/main.ts']), { complete: true, sources: ['src/main.ts', 'src/screen.ts'] })
  assert.equal((await run(['../escape'])).complete, false)
  assert.equal((await run(['src/screen.ts'], true)).complete, false)
})

test('AEON-1333 kill switch forces full planner and independent browser scope decisions', () => {
  assert.equal(plan({ impact: 'off', paths: ['README.md'] }).browser_scope, 'gated-full')
  assert.equal(plan({ impact: 'off' }).mode, 'off')
  assert.deepEqual(schedulingDecision('pull_request', ['README.md'], () => true, { impact: 'off' }), { mode: 'full', layout: 'full', reason: 'CI_IMPACT=off' })
  assert.equal(effectiveLane('docs-only', { impact: 'off', affectedLane: 'on', mode: 'essential', event: 'pull_request' }), 'full')
  assert.equal(classifyPR('pull_request', {}, { impact: 'off', git: () => { throw new Error('must not inspect diff') } }).lane, 'full')
})

test('AEON-1333 kill switch forces full execution despite supplied narrow decisions', () => {
  const options = { event: 'pull_request', paths: ['README.md'], impact: 'off', webImports: graph }
  assert.equal(select(tests, options).full, true)
  const selection = runnerSelection(tests, options, { mode: 'essential', layout: 'static' }, { mode: 'essential', layout: 'static' })
  assert.equal(selection.full, true); assert.equal(selection.layout, 'full'); assert.deepEqual(selection.tests, tests.slice(0, 4))
})

test('AEON-1333 kill switch rejects all previous reuse proof without network work including aggregate reconfirmation', async () => {
  const api = () => { throw new Error('must not fetch evidence') }
  assert.equal((await decide({ event: 'push', ref: 'refs/heads/main', impact: 'off', api })).reuse, 'none')
  assert.equal((await decideMergeGroup({ event: 'merge_group', killSwitch: 'on', impact: 'off', api })).reuse, 'none')
  for (const event of ['push', 'merge_group']) assert.equal((await reuseMain({ CI_IMPACT: 'off', CI_MG_REUSE: 'on', GITHUB_EVENT_NAME: event, GITHUB_REF: 'refs/heads/main', CONFIRM_PR_RUN: '42' }, api)).reuse, 'none')
})

test('AEON-1333 kill switch aggregates reject planned skips, stale layouts, prior reuse and every failed or missing dependency', t => {
  const output = mkdtempSync(resolve(tmpdir(), 'aeon-impact-aggregate-'))
  t.after(() => rmSync(output, { recursive: true, force: true }))
  assert.throws(() => requireResults('docs-only', 'success', ['skipped'], 'off'), /requires full execution/)
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  for (const id of ['go', 'web', 'release-check', 'e2e']) {
    const job = new RegExp(`\\n  ${id}:\\n([\\s\\S]*?)(?=\\n  [a-z][\\w-]*:|$)`).exec(workflow)[1]
    const steps = job.split('\n      - ')
    const gates = steps.filter(step => step.includes('test "$CI_PLAN" = success') && step.includes('run: |'))
    for (const gate of gates) {
      const script = gate.split('        run: |\n')[1].split('\n').filter(line => line.startsWith('          ')).map(line => line.slice(10)).join('\n')
      const env = { PATH: process.env.PATH, CI_IMPACT: 'off', CI_LANE: 'full', TIER_LAYOUT: 'full', TIER_PLAN: 'success', CI_PLAN: 'success', REUSE: 'none',
        GO_TEST: 'success', GO_STATIC: 'success', GO_TIMING: 'success', WEB_CATALOGUE: 'success', WEB_SETUP: 'success', WEB_UNIT: 'success', WEB_SHARD: 'success', CHECK_RESULT: 'success', CACHE_PRIME: 'skipped', GITHUB_STEP_SUMMARY: resolve(output, 'summary.md') }
      // The PR-reuse-only Go gate has stricter positive reuse obligations.
      if (gate.includes('PR_CONFIRM_REUSE') && id === 'go') continue
      assert.equal(spawnSync('bash', ['-c', script], { env, encoding: 'utf8' }).status, 0, id)
      for (const override of [{ CI_LANE: 'docs-only' }, { TIER_LAYOUT: 'static' }, { REUSE: 'merge_group' }, { REUSE: 'pull_request' }, { CI_PLAN: 'failure' }, { TIER_PLAN: 'skipped' }]) assert.notEqual(spawnSync('bash', ['-c', script], { env: { ...env, ...override }, encoding: 'utf8' }).status, 0, `${id}/${JSON.stringify(override)}`)
      const inputs = id === 'go' ? ['GO_TEST', 'GO_STATIC', 'GO_TIMING'] : id === 'web' ? ['WEB_CATALOGUE', 'WEB_SETUP', 'WEB_UNIT', 'WEB_SHARD'] : ['CHECK_RESULT']
      for (const input of inputs) for (const result of ['failure', 'cancelled', 'skipped', '']) assert.notEqual(spawnSync('bash', ['-c', script], { env: { ...env, [input]: result }, encoding: 'utf8' }).status, 0, `${id}/${input}/${result}`)
    }
    assert.ok(gates.length, `${id} has a checked aggregate`)
  }
})

test('AEON-1333 workflow publishes shadow evidence from immutable base with Actions read authority and never feeds shadow selection to execution', () => {
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const tier = /\n  tier-plan:\n([\s\S]*?)(?=\n  runner-route:)/.exec(workflow)[1]
  assert.match(tier, /actions: read/); assert.match(tier, /GH_TOKEN: \$\{\{ secrets.GITHUB_TOKEN \}\}/)
  assert.match(tier, /git fetch --no-tags --depth=2 origin "\$GITHUB_SHA"/)
  assert.match(tier, /git archive "\$PR_BASE_SHA"/); assert.match(tier, /AEON_IMPACT_CHECKOUT="\$GITHUB_WORKSPACE" node "\$snapshot\/scripts\/test-tiers\/impact-plan.mjs"/)
  assert.match(tier, /name: impact-ci-shadow-plan/); assert.match(tier, /if-no-files-found: error/)
  assert.equal((workflow.match(/impact-plan\.json/g) ?? []).length, 2, 'only producer/fallback and upload reference the plan')
  assert.ok(!workflow.includes('outputs.browser_scope'))
  const nightly = readFileSync(new URL('../../.github/workflows/nightly-full.yml', import.meta.url), 'utf8')
  assert.match(nightly, /CI_IMPACT: \$\{\{ vars.CI_IMPACT \}\}/); assert.match(nightly, /run web --all --shard/)
  assert.deepEqual(plan({ execution: { mode: 'essential', layout: 'full', lane: 'full' } }).execution, { mode: 'essential', layout: 'full', lane: 'full' })
})

test('AEON-1333 archived base capsule starts with its full dependency set and produces the current gated plan', t => {
  const repository = fileURLToPath(new URL('../../', import.meta.url))
  const directory = mkdtempSync(resolve(tmpdir(), 'aeon-impact-capsule-'))
  const linkDirectory = mkdtempSync(resolve(tmpdir(), 'aeon-impact-link-'))
  t.after(() => { rmSync(directory, { recursive: true, force: true }); rmSync(linkDirectory, { recursive: true, force: true }) })
  const sha = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repository, encoding: 'utf8' }).trim()
  const archive = execFileSync('git', ['archive', sha, 'scripts', 'web/src', 'web/tests', 'web/e2e', 'web/scripts', 'web/ci-web-shards.json'], { cwd: repository, maxBuffer: 64 * 1024 * 1024, timeout: 15_000 })
  execFileSync('tar', ['-x', '-C', directory], { input: archive, timeout: 15_000 })
  // Git metadata stays in the candidate directory; every imported module and
  // policy/ownership input comes from the archived immutable base.
  // A symlink plus a cleared plan fails the old lexical argv guard: Node
  // realpaths the module URL, main never runs, and a stale file would pass.
  const output = resolve(repository, 'tmp/test-tiers/impact-plan.json')
  rmSync(output, { force: true })
  const link = resolve(linkDirectory, 'impact-plan.mjs')
  symlinkSync(resolve(directory, 'scripts/test-tiers/impact-plan.mjs'), link)
  const outcome = spawnSync(process.execPath, [link], {
    cwd: repository, encoding: 'utf8', timeout: 30_000, env: { PATH: process.env.PATH,
      AEON_IMPACT_CHECKOUT: repository, PR_BASE_SHA: sha, PR_HEAD_SHA: sha, GITHUB_SHA: sha,
      GITHUB_EVENT_NAME: 'pull_request', GITHUB_RUN_ID: '42', GITHUB_RUN_ATTEMPT: '1',
      EXECUTION_MODE: 'full', EXECUTION_LAYOUT: 'full', EXECUTION_LANE: 'full' },
  })
  assert.equal(outcome.status, 0, outcome.stderr)
  const result = JSON.parse(readFileSync(output))
  assert.equal(result.complete, true); assert.equal(result.browser_scope, 'gated-full')
  const manifest = JSON.parse(readFileSync(resolve(directory, 'scripts/ci/web-test-tiers.json')))
  assert.equal(result.selected_cases.length, manifest.tests.filter(row => row.kind === 'browser' && row.tier !== 'NIGHTLY' && row.active !== false).length)
  assert.deepEqual(result.shards.flatMap(shard => shard.cases).sort(), result.selected_cases)
  assert.equal(result.would_have_skipped.count, 0)
})

test('AEON-1333 workflow bootstrap publishes an honest adoption fallback and never executes a candidate planner when the base has none', t => {
  const directory = mkdtempSync(resolve(tmpdir(), 'aeon-impact-adoption-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const git = args => execFileSync('git', args, { cwd: directory, encoding: 'utf8', timeout: 10_000 }).trim()
  const write = (file, text) => { mkdirSync(resolve(directory, file.slice(0, file.lastIndexOf('/'))), { recursive: true }); writeFileSync(resolve(directory, file), text) }
  git(['init', '-q']); git(['config', 'user.name', 'Fixture']); git(['config', 'user.email', 'fixture@example.invalid'])
  write('scripts/ci/web-test-tiers.json', JSON.stringify(manifest(tests)))
  for (const file of ['web/src/main.ts', 'web/tests/old.spec.ts', 'web/e2e/.gitkeep', 'web/scripts/.gitkeep', 'web/ci-web-shards.json']) write(file, '')
  git(['add', '.']); git(['commit', '-qm', 'before adoption']); const base = git(['rev-parse', 'HEAD'])
  write('scripts/test-tiers/impact-plan.mjs', 'import {writeFileSync} from "node:fs";writeFileSync("candidate-ran", "bad")')
  git(['add', '.']); git(['commit', '-qm', 'untrusted candidate']); const head = git(['rev-parse', 'HEAD'])
  const realGit = execFileSync('sh', ['-c', 'command -v git'], { encoding: 'utf8' }).trim()
  mkdirSync(resolve(directory, 'bin')); mkdirSync(resolve(directory, 'runtime'))
  writeFileSync(resolve(directory, 'bin/git'), '#!/bin/sh\nif [ "$1" = fetch ]; then exit 0; fi\nexec "$AEON_TEST_REAL_GIT" "$@"\n', { mode: 0o755 })
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const step = workflow.split('      - name: Publish trusted Impact CI shadow plan\n')[1].split('\n      - ')[0]
  const script = step.split('        run: |\n')[1].replace(/^          /gm, '')
  const outcome = spawnSync('bash', ['-c', script], { cwd: directory, encoding: 'utf8', timeout: 15_000,
    env: { PATH: `${resolve(directory, 'bin')}:${process.env.PATH}`, AEON_TEST_REAL_GIT: realGit,
      RUNNER_TEMP: resolve(directory, 'runtime'), GITHUB_WORKSPACE: directory, PR_BASE_SHA: base, PR_HEAD_SHA: head,
      GITHUB_SHA: head, GITHUB_STEP_SUMMARY: resolve(directory, 'summary.md'),
      EXECUTION_MODE: 'full', EXECUTION_LAYOUT: 'full', EXECUTION_LANE: 'full' } })
  assert.equal(outcome.status, 0, outcome.stderr)
  assert.equal(existsSync(resolve(directory, 'candidate-ran')), false)
  const result = JSON.parse(readFileSync(resolve(directory, 'tmp/test-tiers/impact-plan.json')))
  assert.equal(result.complete, false); assert.equal(result.browser_scope, 'gated-full')
  assert.equal(result.selected_cases.length, 4); assert.equal(result.would_have_skipped.count, 0)
  assert.deepEqual(result.shards, []); assert.match(result.fallback_reasons.join(), /accounting unavailable/)
})

test('AEON-1333 depth-1 merge checkout publishes complete would-have-skipped evidence after the shadow step deepens the merge parents', t => {
  const repository = fileURLToPath(new URL('../../', import.meta.url))
  const root = mkdtempSync(resolve(tmpdir(), 'aeon-impact-shallow-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const full = resolve(root, 'full'), shallow = resolve(root, 'shallow')
  mkdirSync(full)
  const git = (cwd, args) => execFileSync('git', args, { cwd, encoding: 'utf8', timeout: 15_000 }).trim()
  const write = (file, text) => { const path = resolve(full, file); mkdirSync(resolve(path, '..'), { recursive: true }); writeFileSync(path, text) }
  git(full, ['init', '-q', '-b', 'main'])
  git(full, ['config', 'user.name', 'Fixture']); git(full, ['config', 'user.email', 'fixture@example.invalid'])
  git(full, ['config', 'uploadpack.allowReachableSHA1InWant', 'true'])
  for (const file of ['scripts/test-tiers/impact-plan.mjs', 'scripts/test-tiers/browser-impact.mjs', 'scripts/test-tiers/inputs.mjs',
    'scripts/test-tiers/core.mjs', 'scripts/test-tiers/drift-obligations.json', 'scripts/test-tiers/migration.mjs', 'scripts/playwright-safe.mjs', 'scripts/playwright-global-setup.mjs',
    'scripts/playwright-lock.mjs', 'scripts/playwright-processes.mjs', 'scripts/ci-pr-plan.mjs', 'web/scripts/ci-web-shard.mjs', 'web/scripts/ci-web-shard-source.mjs'])
    write(file, readFileSync(resolve(repository, file)))
  write('scripts/ci/web-test-tiers.json', JSON.stringify(manifest(tests)))
  write('scripts/ci/known-flaky.json', JSON.stringify({ version: 1, entries: [] }))
  const launch = policy(); launch.groups[0].specs = tests.map(row => ({ file: row.file, weightSeconds: 1 }))
  write('web/ci-web-shards.json', JSON.stringify(launch))
  for (const file of ['web/src/screen.ts', 'web/tests/essential.spec.ts', 'web/e2e/keep.ts']) write(file, '')
  write('docs/example.md', 'base')
  git(full, ['add', '.']); git(full, ['commit', '-qm', 'base']); const base = git(full, ['rev-parse', 'HEAD'])
  git(full, ['checkout', '-q', '-b', 'pr']); write('docs/example.md', 'head')
  git(full, ['add', '.']); git(full, ['commit', '-qm', 'docs']); const head = git(full, ['rev-parse', 'HEAD'])
  git(full, ['checkout', '-q', 'main']); git(full, ['merge', '-q', '--no-ff', 'pr', '-m', 'merge'])
  const merge = git(full, ['rev-parse', 'HEAD'])
  git(root, ['clone', '-q', '--depth', '1', `file://${full}`, shallow])
  assert.equal(git(shallow, ['rev-parse', 'HEAD']), merge)
  assert.throws(() => execFileSync('git', ['merge-base', '--is-ancestor', base, 'HEAD'], { cwd: shallow, encoding: 'utf8', timeout: 15_000, stdio: ['ignore', 'pipe', 'pipe'] }))
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const step = workflow.split('      - name: Publish trusted Impact CI shadow plan\n')[1].split('\n      - ')[0]
  const script = step.split('        run: |\n')[1].replace(/^          /gm, '')
  mkdirSync(resolve(root, 'runtime'))
  const outcome = spawnSync('bash', ['-c', script], { cwd: shallow, encoding: 'utf8', timeout: 30_000,
    env: { PATH: process.env.PATH, GIT_TERMINAL_PROMPT: '0', RUNNER_TEMP: resolve(root, 'runtime'), GITHUB_WORKSPACE: shallow,
      PR_BASE_SHA: base, PR_HEAD_SHA: head, GITHUB_SHA: merge, GITHUB_EVENT_NAME: 'pull_request', GITHUB_RUN_ID: '42', GITHUB_RUN_ATTEMPT: '1',
      GITHUB_STEP_SUMMARY: resolve(root, 'summary.md'), EXECUTION_MODE: 'full', EXECUTION_LAYOUT: 'full', EXECUTION_LANE: 'full' } })
  assert.equal(outcome.status, 0, outcome.stderr)
  const result = JSON.parse(readFileSync(resolve(shallow, 'tmp/test-tiers/impact-plan.json')))
  assert.equal(result.complete, true, JSON.stringify(result.fallback_reasons))
  assert.equal(result.mode, 'shadow'); assert.equal(result.browser_scope, 'none')
  assert.equal(result.would_have_skipped.count, 4); assert.equal(result.fallback_reasons.length, 0)
  assert.equal(result.shards.length, fullWebShardCount); assert.deepEqual(result.shards.flatMap(shard => shard.cases), [])
  assert.equal(result.identity.base_sha, base); assert.equal(result.identity.head_sha, head); assert.equal(result.identity.checkout_sha, merge)
})
