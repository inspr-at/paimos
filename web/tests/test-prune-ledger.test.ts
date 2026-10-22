// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { checkRepository, requiredRisks, validateLedger, validateVisual } from '../../scripts/test-prune/check.mjs'

const read = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url))
const json = (path: string) => JSON.parse(read(path).toString())
const key = (row: { kind: string; package?: string; file?: string; name: string; occurrence?: number }) =>
  row.kind === 'go' ? `${row.package}:${row.name}` :
    `${row.kind}:${row.file}:${row.name}${row.occurrence === undefined ? '' : `#${row.occurrence}`}`

// Risk: consolidating a protected historical identity or mixed delete candidate
// could silently discard its safety assertion, even by deleting its ledger row.
test('test-prune ledger rejects removed risks without lineage and keeps P01 execution frozen', () => {
  const baseline = json('scripts/test-prune/baseline.json')
  // Generic delete-candidate tags also exist on units outside the browser
  // prune plan. They must not inflate its lineage or hide explicit unit drift.
  for (const kind of ['node', 'vitest']) {
    assert.ok(baseline.catalogue.some(
      (row: { kind: string; tags?: string[] }) => row.kind === kind && row.tags?.includes('delete-candidate')),
    `The regression fixture must retain tagged ${kind} units`)
  }
  const required = requiredRisks(baseline)
  const candidates = required.filter((row: { id: string }) => row.id.startsWith('candidate-case:'))
  assert.equal(candidates.length, 155, 'The audited browser candidate population must remain exact')
  assert.ok(candidates.every((row: { source: { kind: string } }) => row.source.kind === 'browser'),
    'Generic unit candidate tags are outside the browser prune plan')
  const runtimeDrift = baseline.driftObligations.cases.filter((row: { kind: string }) => row.kind !== 'browser')
  assert.equal(runtimeDrift.length, 4, 'The regression fixture must retain all explicit runtime drift obligations')
  for (const source of runtimeDrift) {
    assert.deepEqual(required.find((row: { id: string }) => row.id === `drift:${key(source)}`)?.source, source,
      'Explicit runtime drift remains protected alongside browser candidates')
  }
  const summary = checkRepository()
  assert.deepEqual(summary, { browserCases: 5318, specs: 378, core: 29, protectedRisks: 647,
    driftCases: 455, candidateGroups: 31, candidateCases: 155, visualCases: 80,
    visualApproval: 'pending', phase: 'P01', cuts: false })
  const original = {
    baseline,
    ledger: json('scripts/test-prune/risk-ledger.json'),
    visual: json('scripts/test-prune/visual-manifest.json'),
    currentWeb: json('scripts/ci/web-test-tiers.json'), currentGo: json('scripts/ci/go-test-tiers.json'),
  }
  const sourceHash = (path: string) => createHash('sha256').update(read(path)).digest('hex')
  const fixture = json('scripts/test-prune/testdata/missing-destination.json')
  const missing = structuredClone(original)
  const risk = missing.ledger.risks.find((row: { id: string }) => row.id === fixture.riskId)
  assert.ok(risk, 'The negative control must retain the exact protected person-switch fixture')
  risk.destinations = fixture.destinations
  if (fixture.removeCurrentAnchors) missing.currentWeb.tests = missing.currentWeb.tests.filter(
    (row: Parameters<typeof key>[0]) => !risk.currentKeys.includes(key(row)))
  assert.throws(() => validateLedger({ ...missing, sourceHash }), {
    message: fixture.expectedErrorPrefix + fixture.riskId,
  })
  for (const prefix of ['drift:', 'go-drift:', 'candidate-group:', 'candidate-case:']) {
    const omitted = structuredClone(original)
    const index = omitted.ledger.risks.findIndex((row: { id: string }) => row.id.startsWith(prefix))
    assert.notEqual(index, -1)
    omitted.ledger.risks.splice(index, 1)
    assert.throws(() => validateLedger({ ...omitted, sourceHash }), /Missing\/extra protected risk lineage rows/)
  }
  for (const source of runtimeDrift) {
    const omitted = structuredClone(original)
    const index = omitted.ledger.risks.findIndex((row: { id: string }) => row.id === `drift:${key(source)}`)
    assert.notEqual(index, -1, 'The ledger must retain the exact runtime drift obligation')
    omitted.ledger.risks.splice(index, 1)
    assert.throws(() => validateLedger({ ...omitted, sourceHash }), /Missing\/extra protected risk lineage rows/)
  }
  const unknown = structuredClone(original)
  unknown.ledger.risks[0].destinations = ['runtime:nonexistent-replacement']
  assert.throws(() => validateLedger({ ...unknown, sourceHash }), /Unknown destination runtime:nonexistent-replacement/)
  const cut = structuredClone(original)
  const first = cut.ledger.risks[0].currentKeys[0]
  cut.currentWeb.tests = cut.currentWeb.tests.filter((row: Parameters<typeof key>[0]) => key(row) !== first)
  assert.throws(() => validateLedger({ ...cut, sourceHash }), /Removed risk has no verified destination/)
  const demoted = structuredClone(original)
  const protectedKey = demoted.ledger.risks.flatMap((row: { currentKeys: string[] }) => row.currentKeys).find(
    (id: string) => demoted.baseline.catalogue.some((row: { key: string; tier: string }) => row.key === id && row.tier !== 'NIGHTLY'))
  assert.ok(protectedKey)
  const protectedRow = demoted.currentWeb.tests.find((row: Parameters<typeof key>[0]) => key(row) === protectedKey)
  assert.notEqual(protectedRow.tier, 'NIGHTLY', 'The demotion counterexample must start in the full gate')
  protectedRow.tier = 'NIGHTLY'
  assert.throws(() => validateLedger({ ...demoted, sourceHash }), /Removed risk has no verified destination/)
  assert.throws(() => validateLedger({ ...original, sourceHash: () => '0'.repeat(64) }), /Frozen historical-evidence changed/)
  // Snapshot enforcement is explicit: future assertion additions and refreshed
  // timings must not be blocked by P01's one-time no-execution-change check.
  const frozen = structuredClone(original)
  frozen.currentWeb = { ...frozen.baseline.cataloguePolicy, tests: frozen.baseline.catalogue.map((row: { key: string }) => {
    const { key: ignored, ...entry } = row
    return entry
  }) }
  const frozenHash = (path: string) => original.baseline.sourcePins.find((pin: { path: string }) => pin.path === path).sha256
  assert.equal(validateLedger({ ...frozen, sourceHash: frozenHash, p01: true }).cuts, false)
  assert.throws(() => validateLedger({ ...frozen, sourceHash: () => '0'.repeat(64), p01: true }), /Frozen launch-policy changed/)
  frozen.currentWeb.defaultNewTier = 'ESSENTIAL'
  assert.throws(() => validateLedger({ ...frozen, sourceHash: frozenHash, p01: true }), /P01 changed catalogue selection\/tagging\/timing policy/)
  assert.throws(() => validateVisual(original.visual, { cuts: true }), /Unpinned visual reference/)
  const missingCheckpoint = structuredClone(original.visual)
  missingCheckpoint.states[0].checkpoints.pop()
  assert.throws(() => validateVisual(missingCheckpoint), /Missing\/duplicate visual checkpoint/)
})
