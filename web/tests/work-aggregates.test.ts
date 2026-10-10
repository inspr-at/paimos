// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { estimateDisplay, estimateHours } from '../src/lib/estimates.ts'
import { apiParams, filtersFromQuery, totalFrom } from '../src/lib/ticketList.ts'
import { etaFromTicket, formatEta, progressAccessibleName, progressReportedAt } from '../src/lib/eta.ts'

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

test('migrated work is included by the default list and live filter', () => {
  const filters = filtersFromQuery({})
  assert.ok(apiParams('project', filters).kind?.includes('work'))
  // Legacy type=work was every kind, so it no longer narrows (AEON-974): migrated work stays in.
  const typed = filtersFromQuery({ type: 'work' })
  assert.deepEqual(typed.type, [])
  assert.deepEqual(apiParams('project', typed).kind, ['work', 'ticket', 'task', 'epic'])
  assert.deepEqual(apiParams('project', typed).level, [])
})

for (const scenario of [
  { name: 'weighted 100% with an unestimated open leaf', progress_pct: 100, estimated_leaves: 1, open_leaves: 1, ready_partial: true },
  { name: 'weighted progress rounded by the server to 100%', progress_pct: 100, estimated_leaves: 2, open_leaves: 1, ready_partial: false },
  { name: 'fractional progress rounded by the formatter to 100%', progress_pct: 99.6, estimated_leaves: 2, open_leaves: 1, ready_partial: false },
]) {
  test(`${scenario.name} retains ETA and staleness in every mode`, () => {
    const now = Date.parse('2026-10-04T10:00:00Z')
    const input = etaFromTicket({ finished: false, leaf_count: 2, progress_basis: 'estimate',
      ...scenario, eta_ready_at: '2026-10-04T09:45:00Z', ready_reported_at: '2026-10-04T09:00:00Z', ready_stale: true })
    for (const stale of [false, true]) {
      const label = progressAccessibleName(100, stale, stale ? progressReportedAt({ finished: false, ready_stale: true, ready_reported_at: '2026-10-04T09:00:00Z' }) : null, now, 'UTC', input)
      assert.equal(label, `100% progress; work remains${stale ? ', estimate stale since 09:00' : ''}`)
      assert.doesNotMatch(label, /100% done/)
    }
    for (const mode of ['relative', 'clock', 'both'] as const) {
      const view = formatEta(input, mode, now)!
      assert.equal(view.kind, 'Ready')
      assert.equal(view.pct, 100)
      assert.equal(view.done, false)
      assert.equal(view.stale, true)
      assert.equal(view.overdue, true)
      assert.equal(view.text, (mode === 'clock' ? '09:45' : 'overdue 15 min') + (scenario.ready_partial ? ' · partial' : ''))
      assert.equal(view.hover, mode === 'both' ? '09:45' : null)
      assert.match(view.tip, /Estimate from 09:00 is 60 min old/)
      assert.match(view.tip, /work remains/)
      assert.doesNotMatch(view.tip, /100% done/)
    }
  })
}

test('only closed aggregate leaves or explicit completion suppress a parent ETA at 100%', () => {
  const now = Date.parse('2026-10-04T10:00:00Z')
  const eta = { finished: false, progress_pct: 100, leaf_count: 2, estimated_leaves: 2,
    progress_basis: 'estimate' as const, open_leaves: 0, eta_ready_at: '2026-10-04T09:45:00Z', ready_stale: true }
  const closed = formatEta(etaFromTicket(eta), 'relative', now)!
  assert.deepEqual([closed.text, closed.stale, closed.overdue, closed.done], [null, false, false, false])
  assert.equal(progressAccessibleName(100, false, null, now, 'UTC', etaFromTicket(eta)), '100% done')
  const finished = formatEta(etaFromTicket({ ...eta, finished: true, open_leaves: 1 }), 'relative', now)!
  assert.deepEqual([finished.text, finished.stale, finished.overdue, finished.done], [null, false, false, true])
  assert.equal(progressAccessibleName(100, false, null, now, 'UTC', etaFromTicket({ ...eta, finished: true, open_leaves: 1 })), '100% done')
})

test('unknown leaf completion cannot declare 100% done even without an ETA column', () => {
  const now = Date.parse('2026-10-04T10:00:00Z')
  const input = etaFromTicket({ finished: false, progress_pct: 100, leaf_count: 2, estimated_leaves: 1, progress_basis: 'estimate' })
  assert.equal(progressAccessibleName(100, true, null, now, 'UTC', input), '100% progress; work remains, estimate stale')
  assert.match(formatEta(input, 'relative', now)!.tip, /100% progress; work remains/)
})
