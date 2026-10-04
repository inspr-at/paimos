// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { estimateDisplay, estimateHours } from '../src/lib/estimates.ts'
import { totalFrom } from '../src/lib/ticketList.ts'
import { etaFromTicket, formatEta } from '../src/lib/eta.ts'

const parent = { kind_slug: 'work', fields: { estimate_hours: 40 }, estimate: { hours: 32, planned_hours: 40, is_parent: true, estimated_children: 6, open_children: 9, estimated_leaves: 6, leaf_count: 9 } }
test('parent sums and plans stay distinct even above the leaf estimate limit', () => {
  assert.equal(estimateHours(parent), 32)
  assert.equal(estimateDisplay(parent).text, '~32h from leaves · ~40h planned')
  assert.match(estimateDisplay(parent).tip, /6 of 9 leaves estimated/)
  assert.equal(estimateHours({ ...parent, estimate: { ...parent.estimate, hours: 500 } }), 500)
  assert.equal(estimateHours({ ...parent, estimate: { ...parent.estimate, hours: null } }), null)
  assert.equal(estimateDisplay({ ...parent, estimate: { ...parent.estimate, hours: null } }).text, 'No leaf estimates · ~40h planned')
})
test('partial ETA and progress coverage are explicit in every ETA mode', () => {
  const now = Date.parse('2026-10-04T10:00:00Z')
  const eta = etaFromTicket({ finished: false, progress_pct: 63, leaf_count: 9, estimated_leaves: 6, progress_basis: 'estimate', open_leaves: 5, ready_leaves: 3, ready_partial: true, eta_ready_at: '2026-10-04T11:00:00Z' })
  for (const mode of ['relative', 'clock', 'both'] as const) {
    const view = formatEta(eta, mode, now)!
    assert.match(view.text!, /partial/)
    assert.match(view.tip, /6 of 9 leaves estimated/)
    assert.match(view.tip, /Partial ETA/)
    assert.equal(view.pct, 63)
    assert.equal(view.done, false)
  }
  const fallback = formatEta(etaFromTicket({ finished: false, progress_pct: 33, leaf_count: 3, estimated_leaves: 0, progress_basis: 'leaves' }), 'relative', now)!
  assert.match(fallback.tip, /0 of 3 leaves estimated; progress counts leaves/)
})

test('headline totals use leaf state facets instead of grouping row counts', () => {
 assert.equal(totalFrom({ kind: { work: 6 }, state: { open: 2, done: 1 } }), 3)
 assert.equal(totalFrom({ kind: { work: 6 }, state: {} }), 0)
})
