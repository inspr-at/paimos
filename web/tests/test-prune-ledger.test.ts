// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { checkRepository, validateLedger, validateVisual } from '../../scripts/test-prune/check.mjs'

const read = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url))
const json = (path: string) => JSON.parse(read(path).toString())

// Risk: consolidating a protected historical identity or mixed delete candidate
// could silently discard its safety assertion, even by deleting its ledger row.
test('test-prune ledger rejects removed risks without lineage and keeps P01 execution frozen', () => {
  const summary = checkRepository()
  assert.deepEqual(summary, { browserCases: 5318, specs: 378, core: 29, protectedRisks: 647,
    driftCases: 455, candidateGroups: 31, candidateCases: 155, visualCases: 80,
    visualApproval: 'pending', phase: 'P01', cuts: false })
  const original = {
    baseline: json('scripts/test-prune/baseline.json'),
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
    (row: { kind: string; file: string; name: string }) => !risk.currentKeys.includes(`${row.kind}:${row.file}:${row.name}`))
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
  const unknown = structuredClone(original)
  unknown.ledger.risks[0].destinations = ['runtime:nonexistent-replacement']
  assert.throws(() => validateLedger({ ...unknown, sourceHash }), /Unknown destination runtime:nonexistent-replacement/)
  const cut = structuredClone(original)
  const first = cut.ledger.risks[0].currentKeys[0]
  cut.currentWeb.tests = cut.currentWeb.tests.filter((row: { kind: string; file: string; name: string }) =>
    `${row.kind}:${row.file}:${row.name}` !== first)
  assert.throws(() => validateLedger({ ...cut, sourceHash }), /Removed risk has no verified destination/)
  const demoted = structuredClone(original)
  demoted.currentWeb.tests.find((row: { kind: string; tier: string }) => row.kind === 'browser' && row.tier === 'ESSENTIAL').tier = 'NIGHTLY'
  assert.throws(() => validateLedger({ ...demoted, sourceHash }), /Removed risk has no verified destination|P01 cannot remove or demote/)
  assert.throws(() => validateLedger({ ...original, sourceHash: () => '0'.repeat(64) }), /Frozen launch-policy changed/)
  assert.throws(() => validateVisual(original.visual, { cuts: true }), /Unpinned visual reference/)
  const missingCheckpoint = structuredClone(original.visual)
  missingCheckpoint.states[0].checkpoints.pop()
  assert.throws(() => validateVisual(missingCheckpoint), /Missing\/duplicate visual checkpoint/)
})
