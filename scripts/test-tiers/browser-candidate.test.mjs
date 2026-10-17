// SPDX-License-Identifier: AGPL-3.0-only
// Risk: browser harnesses do not import their page, and incomplete graphs or
// NIGHTLY-only failures can turn a seemingly full fallback into a false green.
// Registered in the fixed static lane through test-tiers.test.mjs.
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { candidateGraph, candidateSelection, candidateBounds } from './browser-candidate.mjs'
import { corpusPin, historicalSnapshots, replayCandidate } from './browser-candidate-replay.mjs'
import { browserAreaDecision, loadBrowserAreas } from './browser-areas.mjs'

const source = 'web/src/components/work/Panel.vue'
const row = (file, tier = 'NIGHTLY') => ({ kind: 'browser', file: `tests/${file}.spec.ts`, tier, name: file })
const tests = [row('essential', 'ESSENTIAL'), row('direct'), row('url'), row('navigation'), row('unrelated'), row('gated', 'GATED-FULL')]
const files = () => new Map([
  ['src/main.ts', "import './router'"], ['src/router.ts', "export { default } from './components/work/Panel.vue'"],
  ['src/components/work/Panel.vue', "<script setup>import { value } from '../../lib/value'</script>"],
  ['src/lib/value.ts', "export { value } from './cycle'"], ['src/lib/cycle.ts', "export { value } from './value'"],
  ['src/styles/tokens.css', ':root { --ink: black; }'], ['src/stores/session.ts', 'export const session = {}'],
  ['tests/essential.spec.ts', ''], ['tests/direct.spec.ts', "import Panel from '../src/components/work/Panel.vue'; import './shared-fixture'"],
  ['tests/shared-fixture.ts', 'export const fixture = {}'],
  ['tests/url.spec.ts', "await page.goto('/tests/panel.html')"],
  ['tests/panel.html', '<script type="module" src="./panel-harness.ts"></script>'],
  ['tests/panel-harness.ts', "import Panel from '@/components/work/Panel.vue'"],
  ['tests/navigation.spec.ts', "await page.goto('/work')"], ['tests/unrelated.spec.ts', ''], ['tests/gated.spec.ts', ''],
])
const select = (overrides = {}) => candidateSelection({ paths: [source], tests, graph: candidateGraph(files()),
  impact: 'on', experiment: true, ...overrides })
const names = plan => plan.selected.map(row => row.name)

test('AEON-1485 import closure includes NIGHTLY direct, inline harness, route and fixture owners without selecting unrelated specs', () => {
  const graph = candidateGraph(files())
  assert.equal(graph.complete, true); assert.equal(graph.issues.size, 0)
  const picked = select({ graph })
  assert.equal(picked.scope, 'affected')
  assert.deepEqual(names(picked), ['essential', 'direct', 'url', 'navigation'])
  assert.deepEqual(picked.mappedSpecs, ['tests/direct.spec.ts', 'tests/navigation.spec.ts', 'tests/url.spec.ts'])
  const cycle = select({ paths: ['web/src/lib/cycle.ts'], graph })
  assert.equal(cycle.scope, 'affected'); assert.deepEqual(names(cycle), names(picked))
  const fixture = select({ paths: ['web/tests/shared-fixture.ts'], graph })
  assert.equal(fixture.scope, 'affected'); assert.deepEqual(names(fixture), ['essential', 'direct'])
})

test('AEON-1485 uncertain ownership fails to the entire catalogue, retains NIGHTLY cases and respects both opt-in and kill switch', () => {
  for (const paths of [[source, 'internal/nodes/nodes.go'], ['web/src/styles/tokens.css'], ['web/src/stores/session.ts'],
    ['web/src/router.ts'], ['web/src/components/work/New.vue'], ['web/tests/unknown.ts'], [], undefined, ['../escape.ts']]) {
    const picked = select({ paths })
    assert.equal(picked.scope, 'catalogue', JSON.stringify(paths)); assert.deepEqual(names(picked), tests.map(row => row.name))
    assert.equal(picked.narrowed, false)
  }
  for (const event of ['merge_group', 'push', 'schedule', 'workflow_dispatch']) assert.deepEqual(names(select({ event })), tests.map(row => row.name))
  for (const overrides of [{}, { impact: 'off', experiment: true }, { impact: undefined, experiment: true },
    { impact: 'on', experiment: false }, { impact: 'on', experiment: undefined }]) {
    // No ambient environment can turn the experiment on.
    const picked = candidateSelection({ paths: [source], tests, graph: candidateGraph(files()), ...overrides })
    assert.equal(picked.scope, 'disabled'); assert.deepEqual(names(picked), ['essential', 'gated'])
  }
  for (const graph of [undefined, { complete: false, reason: 'truncated' }, candidateGraph(files(), { ...candidateBounds, files: 1 }),
    candidateGraph(files(), { ...candidateBounds, fileBytes: 2 }), candidateGraph(files(), { ...candidateBounds, totalBytes: 2 }),
    candidateGraph(files(), { ...candidateBounds, edges: 1 })]) {
    assert.equal(graph?.complete ?? false, false)
    assert.deepEqual(names(select({ graph })), tests.map(row => row.name))
  }
  for (const code of ["import('./missing.vue')", 'import(computedPath)', "import.meta.glob('./*.vue')"]) {
    const data = files(); data.set('tests/unrelated.spec.ts', code)
    const picked = select({ graph: candidateGraph(data) })
    assert.equal(picked.scope, 'catalogue', 'an unresolved unselected importer may hide another consumer')
    assert.deepEqual(names(picked), tests.map(row => row.name))
  }
  const unknown = [...tests, row('new')]
  const future = select({ tests: unknown })
  assert.equal(future.scope, 'catalogue')
  assert.equal(future.mappedSpecs.includes('tests/new.spec.ts'), false, 'future spec has no invented base ownership')
  assert.ok(future.selected.some(row => row.name === 'new'), 'catalogue fallback retains a future NIGHTLY case')
  const addedSpec = select({ paths: ['web/tests/new.spec.ts'], tests: unknown })
  assert.equal(addedSpec.scope, 'catalogue')
  assert.ok(addedSpec.mappedSpecs.includes('tests/new.spec.ts'), 'changed spec owns itself without trusting candidate import edges')
  assert.deepEqual(names(select({ bounds: { ...candidateBounds, extraCases: 1 } })), tests.map(row => row.name))
  assert.throws(() => select({ tests: [...tests, { ...row('bad'), file: '../bad.spec.ts' }] }), /invalid or oversized browser catalogue/)
})

test('AEON-1485 replay rejects changed pins and missing history and cannot activate the real rollout', () => {
  const text = readFileSync(new URL('./affected-replay.json', import.meta.url), 'utf8')
  const policy = JSON.parse(readFileSync(new URL('../ci/browser-area-rollout.json', import.meta.url), 'utf8'))
  assert.equal(policy.corpus_sha256, corpusPin)
  const unavailable = () => { throw new Error('historical object unavailable') }
  assert.throws(() => replayCandidate(text + ' ', policy, unavailable), /corpus pin mismatch/)
  assert.throws(() => replayCandidate(text, { ...policy, corpus_sha256: '0'.repeat(64) }, unavailable), /corpus pin mismatch/)
  assert.throws(() => replayCandidate(text, policy, unavailable), /historical object unavailable/)
  const read = historicalSnapshots('.', { exec: unavailable })
  assert.throws(() => read('HEAD'), /immutable SHA/)
  assert.throws(() => read('a'.repeat(40)), /historical object unavailable/)
  const real = loadBrowserAreas(fileURLToPath(new URL('../../', import.meta.url)))
  assert.equal(real.complete, true)
  for (const area of real.areas) {
    assert.equal(area.mode, 'gated-full')
    assert.equal(browserAreaDecision([`${area.prefixes[0]}Panel.vue`], real).full, true)
  }
})

test('AEON-1485 published measurement accounts for every pinned proof row and never qualifies catalogue fallback as narrowing', () => {
  const fixture = JSON.parse(readFileSync(new URL('./affected-replay.json', import.meta.url), 'utf8'))
  const measurement = JSON.parse(readFileSync(new URL('./browser-candidate-measurement.json', import.meta.url), 'utf8'))
  const supported = new Set(['source-supported', 'bisected', 'ticket-confirmed', 'fix-diff-confirmed', 'recorded-failure'])
  const proof = fixture.historicalRegressions.filter(row => row.observed.kind === 'browser' && supported.has(row.causalConfidence))
  assert.equal(measurement.corpus_sha256, corpusPin)
  assert.deepEqual(measurement.cases.map(row => row.id).sort(), proof.map(row => row.id).sort())
  assert.equal(measurement.overall.cases, 294); assert.equal(measurement.overall.beforeMisses, 291)
  assert.equal(measurement.overall.ownershipMisses, 10)
  assert.equal(measurement.cases.filter(row => !row.candidate.mappingSelected).length, 10)
  assert.equal(measurement.cases.filter(row => row.candidate.scope === 'catalogue').length, 294)
  for (const [area, summary] of Object.entries(measurement.areas)) {
    const rows = measurement.cases.filter(row => row.areas.includes(area))
    assert.equal(summary.cases, rows.length)
    assert.equal(summary.beforeMisses, rows.filter(row => !row.s1.selected).length)
    assert.equal(summary.ownershipMisses, rows.filter(row => !row.candidate.mappingSelected).length)
    assert.equal(summary.afterMisses, 0); assert.equal(summary.affectedCases, 0)
    assert.equal(summary.qualified, false, 'zero misses from full catalogue is not affected evidence')
  }
  for (const [file, hash] of Object.entries(measurement.implementation_sha256)) {
    assert.equal(createHash('sha256').update(readFileSync(new URL(file, import.meta.url))).digest('hex'), hash, file)
  }
})
