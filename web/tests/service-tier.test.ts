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

test('run cost names its frozen multiplier and Default cost, independently of the catalog', async () => {
  const { tierRunCostLabel } = await import('../src/lib/serviceTier.ts')
  const frozen = { model: 'fixture-model', run_id: 'run', cost_usd: '4.800000000000', default_cost_usd: '2.400000000000', provisional: false, segments: [{ tier: 'fast' as const, price_multiplier: 2 }] }
  assert.equal(tierRunCostLabel(frozen), 'Fast ×2 · $2.40 at Default')
  assert.equal(tierRunCostLabel({ ...frozen, segments: [{ tier: 'default', price_multiplier: 1 }, ...frozen.segments] }), 'Default ×1 + Fast ×2 · $2.40 at Default')
  assert.equal(tierRunCostLabel(undefined), 'Tier cost unavailable')
})
test('estimates carry n and basis, with independent unknown time and zero-run cases', async () => {
  const { tierEstimate, estimateCostText, estimateTimeText } = await import('../src/lib/serviceTier.ts')
  const confirmed: TierState = { session_id: 's', revision: 1, active_tier: 'default', pending: null, reports: [report], requests: [], read_only: false, estimates: [{ tier: 'fast', n: 0, basis: 'No estimate yet · 0 runs.', run_id: null, cost_usd: null, duration_ms: null }] }
  const empty = tierEstimate(confirmed, 'fast')!
  assert.equal(empty.n, 0); assert.match(empty.basis, /0 runs/)
  assert.equal(estimateCostText(empty), 'no estimate yet')
  assert.equal(estimateTimeText(empty), 'time: no estimate yet')
  const sample = { tier: 'fast' as const, n: 1, basis: '1 run · frozen price version 4', run_id: 'run', cost_usd: '4.800000000000', duration_ms: 600000 }
  assert.equal(estimateCostText(sample), '≈ $4.80')
  assert.equal(estimateTimeText(sample), '≈ 10 min')
  assert.equal(estimateTimeText({ ...sample, duration_ms: null }), 'time: no estimate yet')
  assert.equal(estimateCostText({ ...sample, n: 0 }), 'no estimate yet')
})
test('history renders request, approval, decline, confirmation and both Undo forms with attribution', async () => {
  const { tierHistoryText } = await import('../src/lib/serviceTier.ts')
  const row = { id: 1, from_tier: 'default' as const, to_tier: 'fast' as const, actor_id: 'person', actor_name: 'Markus', asked_by_name: 'agent-fixture', at: '2026-10-02T00:00:00Z' }
  assert.equal(tierHistoryText({ ...row, action: 'changed' }), 'Tier Default to Fast · by Markus, asked by agent-fixture')
  assert.match(tierHistoryText({ ...row, action: 'requested' }), /Asked for Fast from Default · by Markus/)
  assert.match(tierHistoryText({ ...row, action: 'approved' }), /waiting for confirmation/)
  assert.match(tierHistoryText({ ...row, action: 'declined' }), /Declined Fast for agent-fixture · kept Default · by Markus/)
  assert.match(tierHistoryText({ ...row, action: 'cancelled' }), /Undo: cancelled switch/)
  assert.match(tierHistoryText({ ...row, action: 'undo_requested' }), /Undo requested/)
  assert.match(tierHistoryText({ ...row, action: 'undone', from_tier: 'fast', to_tier: 'default' }), /Tier Fast to Default · by Markus \(undo\)/)
})

test('unread or failed evidence never claims a confirmed zero-run sample', async () => {
  const { tierEstimate, estimateCostText, estimateTimeText } = await import('../src/lib/serviceTier.ts')
  const unread = tierEstimate(undefined, 'fast')
  assert.equal(unread, undefined)
  assert.equal(estimateCostText(unread), '—')
  assert.equal(estimateTimeText(unread), '—')
  const state: TierState = { session_id: 's', revision: 1, active_tier: 'default', pending: null, reports: [report], requests: [], read_only: false }
  assert.equal(tierEstimate(state, 'fast'), undefined)
})
test('the measured tier shows actual figures and other tiers remain projections', async () => {
  const { estimateCostText, estimateTimeText } = await import('../src/lib/serviceTier.ts')
  const measured = { tier: 'fast' as const, actual: true, n: 1, basis: 'Last run at Fast', run_id: 'run', cost_usd: '4.80', duration_ms: 600000 }
  assert.equal(estimateCostText(measured), '$4.80')
  assert.equal(estimateTimeText(measured), '10 min')
  assert.equal(estimateCostText({ ...measured, actual: false }), '≈ $4.80')
  assert.equal(estimateTimeText({ ...measured, actual: false }), '≈ 10 min')
})
