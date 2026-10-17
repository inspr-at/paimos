// SPDX-License-Identifier: AGPL-3.0-only
// Risk: incomplete area evidence or candidate graph edits omit a regression.
// Fixed go-static lane, imported by test-tiers.test.mjs; no native browsers.
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'
import { browserAreaEvidence, loadBrowserAreas, browserAreaDecision } from './browser-areas.mjs'
import { browserPlanSelection, makeBrowserPlan } from './browser-plan.mjs'
import { ownershipGraph } from './browser-ownership.mjs'
import { browserIdentity, buildBrowserMap } from './browser-impact.mjs'
import { select, fullGateTests, graphIncomplete } from './core.mjs'
import { schedulingDecision, sizeWebDecision, main as plannerMain } from './diff.mjs'
import { runnerSelection, plan, load } from './cli.mjs'
import { checkFull } from './check-full.mjs'
import { verifyJobs, requiredJobs, executionSteps } from '../ci-tree-reuse.mjs'
import { parseWorkflow } from '../ci-static.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const base = 'a'.repeat(40), sha = 'b'.repeat(40), tree = 'c'.repeat(40)
const source = 'web/src/components/work/Screen.vue', paths = [source]
const area = () => ({ id: 'work', mode: 'affected', prefixes: ['web/src/components/work/'] })
const regression = () => ({ id: 'proof-1', causalConfidence: 'source-supported', treeBeforeFix: sha,
  observed: { kind: 'browser', testedSHA: sha }, causingChange: { base, paths },
  replay: { trustedGraphSHA: base, s1: { scope: 'affected', selected: true } } })
const corpus = () => JSON.stringify({ historicalRegressions: [regression()] })
const policy = text => ({ version: 1, selector: 'impact-ci-s1', corpus_sha256: createHash('sha256').update(text).digest('hex'), areas: [area()] })
const qualification = () => {
  const text = corpus()
  return loadBrowserAreas('.', { read: (_root, file) => file.endsWith('browser-area-rollout.json') ? JSON.stringify(policy(text)) : text })
}
const row = (file, tier = 'GATED-FULL', name = file) => ({ kind: 'browser', file, tier, name, config: 'playwright.ui.config.ts', project: '' })
const rows = [row('tests/essential.spec.ts', 'ESSENTIAL'), row('tests/import.spec.ts'), row('tests/navigation.spec.ts'), row('tests/other.spec.ts')]
const imports = { [source.slice(4)]: [], 'src/main.ts': ['src/router.ts'], 'src/router.ts': [source.slice(4)],
  ...Object.fromEntries(rows.map(row => [row.file, row.file === 'tests/import.spec.ts' ? [source.slice(4)] : []])) }
const graph = () => ownershipGraph('.', imports, file => file === 'tests/navigation.spec.ts' ? "await page.goto('/')" : '')
const map = () => buildBrowserMap([{ version: 1, tree, runId: '42', attempt: '1', complete: true,
  cases: rows.map(row => ({ ...row, complete: true, status: 'passed', sources: row.file === 'tests/import.spec.ts' ? [source.slice(4)] : [] })) }],
{ tree, runId: '42', attempt: '1', catalogue: rows })
const options = () => ({ event: 'pull_request', paths, impact: 'on', affectedLane: 'on', browserAreas: qualification(),
  webImports: graph(), browserMap: map(), baseTree: tree, tree: { complete: true, go: new Map(), webTests: new Map() } })
const scheduling = value => schedulingDecision(value.event, value.paths, () => true, { ...value, graph: value.webImports, tests: rows })

test('AEON-1336 bounded G7 read-state cases retain all viewport and theme coverage on every PR', () => {
  // Risk: splitting the timed-out screenshot matrix leaves new cases NIGHTLY,
  // so a green PR proves only the original viewport instead of all eight.
  const catalogue = load('web').tests
  const title = 'AEON-1209 G7: the chip and each tile tell loading, unreadable, stale, forbidden and authorized empty apart'
  const expected = [1440, 1024, 400, 390].flatMap(width => ['light', 'dark'].map(theme =>
    width === 1440 && theme === 'light' ? title : `${title} (${width} ${theme})`))
  const cases = catalogue.filter(row => row.file === 'tests/aeon-1209-agents-head.spec.ts' && row.name.startsWith(title))
  assert.deepEqual(cases.map(row => row.name).sort(), expected.sort())
  assert.ok(cases.every(row => row.tier === 'ESSENTIAL'), 'every split case stays on every PR')
  for (const options of [{ event: 'pull_request', paths: ['docs/features/impact-ci-shadow.md'] },
    { event: 'pull_request', paths: [], impact: 'off' }, { event: 'merge_group' }]) {
    const selected = select(catalogue, options).tests
    for (const name of expected) assert.ok(selected.some(row => row.file === cases[0].file && row.name === name), name)
  }
})

test('AEON-1336 historical shadow qualification requires a narrowed proof and rolls back on any confirmed miss', () => {
  const good = browserAreaEvidence(area(), [regression()])
  assert.deepEqual(good, { cases: 1, affected: 1, rejected: 0, misses: [], complete: true })
  const check = evidence => browserAreaDecision(paths, { complete: true, areas: [{ ...area(), evidence }] })
  assert.equal(check(good).full, false)
  for (const change of [row => { row.replay.s1.selected = false }, row => { row.replay.s1.scope = 'gated-full' },
    row => { row.replay.trustedGraphSHA = sha }, row => { row.observed.testedSHA = base },
    row => { row.causalConfidence = 'unresolved' }, row => { row.causalConfidence = 'inherited-source-attribution' },
    row => { row.causalConfidence = 'unknown' }]) {
    const changed = regression(); change(changed)
    assert.equal(check(browserAreaEvidence(area(), [changed])).full, true)
  }
  assert.equal(check(browserAreaEvidence(area(), [])).full, true)
  assert.equal(check(browserAreaEvidence(area(), [regression(), regression()])).full, true)
  const miss = regression(); miss.id = 'new-miss'; miss.replay.s1.selected = false
  const widened = check(browserAreaEvidence(area(), [regression(), miss]))
  assert.equal(widened.full, true); assert.match(widened.reason, /1 historical shadow misses/)
  const multiple = { complete: true, areas: [{ ...area(), evidence: good },
    { id: 'agents', prefixes: ['web/src/components/agents/'], mode: 'affected', evidence: good }] }
  const mixed = [...paths, 'web/src/components/agents/Screen.vue']
  assert.deepEqual(browserAreaDecision(mixed, multiple).areas, ['agents', 'work'])
  multiple.areas[1].mode = 'gated-full'
  assert.equal(browserAreaDecision(mixed, multiple).full, true, 'one rolled-back area widens the whole diff')
})

test('AEON-1336 area policy is bounded, pins the corpus, rejects ambiguous ownership and leaves unproven real areas full', () => {
  const text = corpus(), good = policy(text)
  const readPolicy = value => loadBrowserAreas('.', { read: (_root, file) => file.endsWith('browser-area-rollout.json') ? JSON.stringify(value) : text })
  for (const change of [p => { p.version = 2 }, p => { p.corpus_sha256 = 'd'.repeat(64) },
    p => { p.areas[0].prefixes = ['../work/'] }, p => { p.areas[0].prefixes = ['web/src/'] },
    p => { p.areas.push({ ...area(), id: 'nested', prefixes: ['web/src/components/work/nested/'] }) },
    p => { p.areas.push(area()) }, p => { p.areas[0].mode = 'yes' }]) {
    const changed = structuredClone(good); change(changed)
    assert.equal(readPolicy(changed).complete, false)
  }
  const limits = []
  assert.equal(loadBrowserAreas('.', { read: (_root, file, limit) => { limits.push(limit); return file.endsWith('browser-area-rollout.json') ? JSON.stringify(good) : text } }).complete, true)
  assert.deepEqual(limits, [1024 * 1024, 16 * 1024 * 1024])
  assert.equal(loadBrowserAreas('.', { read: () => undefined }).complete, false)
  const real = loadBrowserAreas(root)
  assert.equal(real.complete, true)
  for (const item of real.areas) {
    assert.equal(item.mode, 'gated-full')
    assert.ok(item.evidence.cases > 0)
    assert.ok(item.evidence.misses.length > 0, item.id)
    const enabled = { ...real, areas: real.areas.map(value => ({ ...value, mode: 'affected' })) }
    assert.equal(browserAreaDecision([`${item.prefixes[0]}Screen.vue`], enabled).full, true, 'mode alone cannot override misses')
  }
})

test('AEON-1336 planner and selector narrow only qualified areas and preserve route owners, essentials and promotions', () => {
  const opts = options(), selection = select(rows, opts)
  assert.equal(selection.full, false); assert.equal(selection.scope, 'affected')
  assert.deepEqual(selection.tests, rows.slice(0, 3), 'non-importing route owner must execute')
  assert.equal(scheduling(opts).mode, 'essential')
  const launch = { version: 1, exclusions: [], groups: [{ id: 'ui', config: 'playwright.ui.config.ts', project: null, flags: [], env: {},
    specs: rows.map(row => ({ file: row.file, weightSeconds: 1 })) }] }
  const sized = sizeWebDecision(scheduling(opts), paths, { ...opts, graph: opts.webImports, tests: rows, policy: launch, base, sha })
  assert.equal(sized.browser_scope, 'affected')
  assert.deepEqual(sized.browserPlan.cases, rows.slice(0, 3).map(browserIdentity).sort())
  assert.ok(sized.webShardSizing.fitsBudget)
  const promoted = { ...rows[3], tier: 'NIGHTLY' }, all = [...rows.slice(0, 3), promoted]
  const selectionWithPromotion = select(all, { ...opts, paths: [...paths, 'scripts/ci/web-test-tiers.json'],
    registryChanges: new Map([['scripts/ci/web-test-tiers.json', { full: false, promoted: [promoted], reason: 'promotion' }]]) })
  assert.ok(selectionWithPromotion.tests.includes(promoted))
  // Losing candidate import edges cannot shrink the immutable execution plan.
  const pinned = runnerSelection(rows, { ...opts, browserPlan: JSON.stringify(sized.browserPlan), base, sha },
    { mode: 'full', layout: 'full', reason: 'candidate lost dependencies' }, { mode: 'essential', layout: 'full' })
  assert.deepEqual(pinned.tests, rows.slice(0, 3)); assert.equal(pinned.scope, 'affected')
})

test('AEON-1336 source rollout fails wide on rollback, mixed or unknown areas, bad coverage, new inputs, graph bounds and fan-out', () => {
  for (const override of [{ browserAreas: undefined }, { browserAreas: { complete: false } },
    { browserAreas: { complete: true, areas: [{ ...area(), mode: 'gated-full', evidence: qualification().areas[0].evidence }] } },
    { paths: ['web/src/components/agents/Screen.vue'] }, { paths: [...paths, 'internal/auth/auth.go'] },
    { paths: [...paths, 'web/src/lib/shared.ts'] }, { browserMap: undefined }, { baseTree: sha },
    { webImports: { [graphIncomplete]: 'truncated' } }, { paths: ['web/src/components/work/New.vue'] },
    { paths: ['web/src/components/work/style.css'] }, { affectedLane: 'off' }, { impact: 'off' }]) {
    const opts = { ...options(), ...override }
    assert.equal(select(rows, opts).full, true, JSON.stringify(override))
    assert.equal(scheduling(opts).mode, 'full', JSON.stringify(override))
  }
  for (const event of ['merge_group', 'push', 'schedule', 'workflow_dispatch']) {
    const opts = { ...options(), event }
    assert.equal(select(rows, opts).full, true); assert.equal(scheduling(opts).mode, 'full')
  }
  const many = [...rows, ...Array.from({ length: 301 }, (_, i) => row('tests/import.spec.ts', 'GATED-FULL', `case ${i}`))]
  const selected = select(many, options())
  assert.equal(selected.full, true); assert.deepEqual(selected.tests, fullGateTests(many))
  const opts = options()
  assert.equal(schedulingDecision(opts.event, paths, () => true, { ...opts, graph: opts.webImports, tests: many }).mode, 'full')
})

test('AEON-1336 execution rejects stale, partial, duplicate and drifted plans and every stronger full decision', () => {
  const good = makeBrowserPlan(rows, rows.slice(0, 3), { base, sha, areas: ['work'] })
  const read = (value, tests = rows, overrides = {}) => browserPlanSelection(tests, value, { event: 'pull_request', base, sha, impact: 'on', affectedLane: 'on', ...overrides })
  assert.deepEqual(read(JSON.stringify(good)).tests, rows.slice(0, 3))
  for (const change of [p => { p.base = sha }, p => { p.sha = base }, p => { p.scope = 'gated-full' },
    p => { p.cases = p.cases.filter(id => id !== browserIdentity(rows[0])) }, p => { p.cases.push(p.cases[0]) },
    p => { p.cases.push('unknown') }, p => { p.areas = [] }, p => { p.version = 2 }]) {
    const changed = structuredClone(good); change(changed)
    assert.equal(read(JSON.stringify(changed)).full, true)
  }
  for (const text of ['', '{', undefined, ' '.repeat(512 * 1024)]) assert.equal(read(text).full, true)
  for (const overrides of [{ event: 'merge_group' }, { event: 'schedule' }, { impact: 'off' }, { impact: '' }, { affectedLane: 'off' }, { base: '' }])
    assert.equal(read(JSON.stringify(good), rows, overrides).full, true)
  for (const changed of [[...rows, row('tests/new.spec.ts')], rows.map(row => ({ ...row, tier: 'ESSENTIAL' })), rows.slice(1)])
    assert.equal(read(JSON.stringify(good), changed).full, true)
  assert.equal(read(JSON.stringify(good), Array(20_001).fill(rows[0])).full, true)
  for (const stronger of [{ mode: 'full', layout: 'full' }, { mode: 'essential', layout: 'static' }, {}])
    assert.equal(runnerSelection(rows, { ...options(), browserPlan: JSON.stringify(good), base, sha }, { mode: 'essential', layout: 'full' }, stronger).full, true)
  assert.equal(runnerSelection(rows, { ...options(), forceFull: true, browserPlan: JSON.stringify(good), base, sha },
    { mode: 'essential', layout: 'full' }, { mode: 'essential', layout: 'full' }).full, true)
})

test('AEON-1336 native runner uses the trusted plan without another map fetch and emits affected scope that cannot prove full reuse', () => {
  const catalogue = load('web').tests
  const selected = catalogue.filter(row => row.kind === 'browser' && row.active !== false && row.tier === 'ESSENTIAL')
  const text = JSON.stringify(makeBrowserPlan(catalogue, selected, { base, sha, areas: ['work'] }))
  const result = plan('web', { env: { CI_IMPACT: 'on', GITHUB_SHA: sha, AEON_BROWSER_PLAN: text }, event: 'pull_request', paths,
    base, affectedLane: 'on', plannerMode: 'essential', plannerLayout: 'full', inventory: { tests: catalogue } },
  { browserCoverage: () => { throw new Error('execution must not fetch another impact map') } })
  assert.equal(result.full, false); assert.equal(result.scope, 'affected')
  assert.deepEqual(result.tests.map(browserIdentity).sort(), selected.map(browserIdentity).sort())
  assert.throws(() => checkFull({ version: 1, full: true, scope: result.scope, exitCode: 0 }), /scope is not proven/)
  const jobs = requiredJobs.map(name => ({ name, run_attempt: 1, status: 'completed', conclusion: 'success', steps: [
    { name: executionSteps[name] ?? (name.startsWith('go-test (') ? 'Test this shard (essential plus changed area, or full on main)' : name.startsWith('web-unit (') ? 'Run selected web units without retries' : 'Run selected UI cases without retries'), status: 'completed', conclusion: 'success' },
    { name: 'Confirm full tier execution', status: 'completed', conclusion: name.startsWith('web-shard (') ? 'skipped' : 'success' }] }))
  assert.throws(() => verifyJobs({ total_count: jobs.length, jobs }, 1), /missing full tier execution evidence/)
})

test('AEON-1336 browser aggregate accepts only explicit planned scopes and rejects failed, missing, cancelled and unexpected skipped execution', t => {
  const directory = mkdtempSync(resolve(tmpdir(), 'aeon-1336-aggregate-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const steps = parseWorkflow(workflow).get('web')
  assert.deepEqual(steps.map(step => step.name), ['Validate browser execution scope', null, null,
    'Revalidate PR execution proof', 'Require catalogue, setup, units and every web shard'],
  'scope validation must precede the unchanged proof sequence and its early reuse exits')
  const script = [steps[0].run, steps.at(-1).run].join('\n')
  const env = { PATH: process.env.PATH, BROWSER_SCOPE: 'affected', CI_IMPACT: 'on', CI_AFFECTED_LANE: 'on',
    GITHUB_EVENT_NAME: 'pull_request', TIER_MODE: 'essential', TIER_LAYOUT: 'full', CI_LANE: 'full', WEB_SHARD: 'success',
    CI_PLAN: 'success', TIER_PLAN: 'success', REUSE: 'none', REUSE_PROOF: 'skipped', SOURCE_RUN: '', CACHE_PRIME: 'skipped',
    WEB_CATALOGUE: 'success', WEB_SETUP: 'success', WEB_UNIT: 'success', GITHUB_STEP_SUMMARY: resolve(directory, 'summary') }
  const run = overrides => spawnSync('bash', ['-c', script], { cwd: directory, env: { ...env, ...overrides }, encoding: 'utf8' }).status
  assert.equal(run({}), 0)
  for (const override of [{ BROWSER_SCOPE: '' }, { BROWSER_SCOPE: 'typo' }, { CI_IMPACT: 'off' }, { CI_AFFECTED_LANE: 'off' },
    { GITHUB_EVENT_NAME: 'merge_group' }, { TIER_MODE: 'full' }, { TIER_LAYOUT: 'static' }, { CI_LANE: 'spec-only' },
    ...['failure', 'cancelled', 'skipped', ''].map(WEB_SHARD => ({ WEB_SHARD }))]) assert.notEqual(run(override), 0, JSON.stringify(override))
  assert.equal(run({ BROWSER_SCOPE: 'none', TIER_LAYOUT: 'static', WEB_SHARD: 'skipped' }), 0)
  assert.equal(run({ BROWSER_SCOPE: 'none', CI_LANE: 'docs-only', WEB_CATALOGUE: 'skipped', WEB_SETUP: 'skipped', WEB_UNIT: 'skipped', WEB_SHARD: 'skipped' }), 0)
  assert.notEqual(run({ BROWSER_SCOPE: 'none', TIER_LAYOUT: 'full', WEB_SHARD: 'skipped' }), 0)
  assert.equal(run({ BROWSER_SCOPE: 'gated-full', CI_IMPACT: 'off' }), 0)
  for (const dependency of ['CI_PLAN', 'TIER_PLAN', 'WEB_CATALOGUE', 'WEB_SETUP', 'WEB_UNIT']) {
    for (const result of ['failure', 'cancelled', 'skipped', '']) assert.notEqual(run({ [dependency]: result }), 0, `${dependency}=${result}`)
  }
  for (const reuse of [{ REUSE: 'pull_request', GITHUB_EVENT_NAME: 'merge_group', CACHE_PRIME: 'skipped' },
    { REUSE: 'merge_group', GITHUB_EVENT_NAME: 'push', GITHUB_REF: 'refs/heads/main', CACHE_PRIME: 'success' }]) {
    const proof = { ...reuse, BROWSER_SCOPE: 'gated-full', TIER_MODE: 'full', REUSE_PROOF: 'success', SOURCE_RUN: '42',
      PR_CONFIRM_REUSE: 'pull_request', PR_CONFIRM_RUN: '42', WEB_CATALOGUE: 'skipped', WEB_SETUP: 'skipped', WEB_UNIT: 'skipped', WEB_SHARD: 'skipped' }
    assert.equal(run(proof), 0, `${reuse.REUSE} full proof must remain reusable`)
    assert.notEqual(run({ ...proof, BROWSER_SCOPE: 'affected' }), 0, 'reuse must not bypass scope validation')
  }
  assert.ok(workflow.includes("AEON_BROWSER_PLAN: ${{ github.event_name == 'pull_request' && needs.tier-plan.outputs.browser_scope == 'affected' && needs.tier-plan.outputs.browser_plan || '' }}"),
    'full and queue execution must receive an empty browser plan')
  const nightly = readFileSync(new URL('../../.github/workflows/nightly-full.yml', import.meta.url), 'utf8')
  assert.match(nightly, /run web --all --shard/); assert.ok(!nightly.includes('AEON_BROWSER_PLAN'))
  const output = resolve(directory, 'outputs')
  plannerMain({ CI_IMPACT: 'off', GITHUB_EVENT_NAME: 'merge_group', GITHUB_OUTPUT: output })
  assert.match(readFileSync(output, 'utf8'), /browser_scope=gated-full\nbrowser_plan=\n/)
})
