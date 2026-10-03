// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { adjacentTier, offeredTiers, tierOptions, tierReport, undoTierAllowed, type TierReport, type TierState } from '../src/lib/serviceTier.ts'

// Synthetic pricing exercises controls; these are never catalog/vendor facts.
const report: TierReport = { harness: 'codex', model: 'fixture-model', harness_version: 'fixture', adapter_version: 'fixture', checked_at: '2026-10-02T00:00:00Z', source: 'https://example.invalid/fixture', applies: 'next_run', change_instructions: 'fixture', tiers: [
  { tier: 'default', name: 'Default', offered: true, price_multiplier: 1, speed_factor: 1, usage_multiplier: 1, mechanism: 'none' },
  { tier: 'fast', name: 'Fast', offered: true, price_multiplier: 2, speed_factor: 2, usage_multiplier: 3, mechanism: 'fixture' },
  { tier: 'fastest', name: 'Fastest', offered: true, price_multiplier: null, speed_factor: 8, usage_multiplier: null, mechanism: 'fixture' },
] }
test('unpriced and absent tiers stay unavailable, even when offered is asserted', () => {
  assert.deepEqual(offeredTiers(report).map(t => t.tier), ['default', 'fast'])
  assert.equal(tierOptions(report)[2]!.reason, 'not offered: no published price')
  assert.equal(tierOptions()[0]!.offered, false)
  assert.equal(tierReport({ harness: 'claude', model: 'fixture-model' }, [report]), undefined)
  assert.equal(tierReport({ harness: 'codex', model: 'other-model' }, [report]), undefined)
})
test('steps traverse offered tiers only and cannot guess an unreported active tier', () => {
  assert.equal(adjacentTier('default', report, 1)?.tier, 'fast')
  assert.equal(adjacentTier('fast', report, 1), undefined)
  assert.equal(adjacentTier('default', report, -1), undefined)
  assert.equal(adjacentTier(null, report, 1), undefined)
})
test('Undo belongs to the accepted control and exactly its confirming revision', () => {
  const receipt = { session: 's', revision: 4, control: 'c', from: 'default', to: 'fast' } as const
  const state: TierState = { session_id: 's', revision: 4, active_tier: 'default', read_only: false, reports: [report], requests: [], pending: { id: 'c', session_id: 's', kind: 'tier', value: 'fast', state: 'pending', outcome: null, reason: null } }
  assert.equal(undoTierAllowed(state, receipt), true)
  assert.equal(undoTierAllowed({ ...state, pending: { ...state.pending!, state: 'claimed' } }, receipt), false)
  assert.equal(undoTierAllowed({ ...state, pending: { ...state.pending!, id: 'other' } }, receipt), false)
  assert.equal(undoTierAllowed({ ...state, session_id: 'other' }, receipt), false)
  assert.equal(undoTierAllowed({ ...state, read_only: true }, receipt), false)
  const confirmed = { ...state, revision: 5, active_tier: 'fast' as const, pending: null }
  assert.equal(undoTierAllowed(confirmed, receipt), true)
  assert.equal(undoTierAllowed({ ...confirmed, revision: 7 }, receipt), false)
})
