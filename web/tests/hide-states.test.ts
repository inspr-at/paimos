// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { HIDE_STATES, hiddenStates, parseHiddenStates, hideLabel, hideChoiceCounts, selectionHiddenByPolicy, statusHiddenByPolicy } from '../src/lib/hideStates.ts'
import { apiParams, filtersFromQuery, filtersFromView, filtersToQuery, sameListState, viewShape } from '../src/lib/ticketList.ts'
import { reconcileStatusHide } from '../src/lib/projectStatusCounts.ts'
import type { ProjectSummary } from '../src/lib/api.ts'

const summary: ProjectSummary = { id: 'p', key: 'P', title: 'Project', state: 'active', total: 12, open: 2, in_progress: 0, done: 6, cancelled: 3, archived_count: 1, last_activity: '', status_counts: [
  { state: 'accepted', bucket: 'open', count: 2 }, { state: 'accepted', bucket: 'done', count: 2 },
  { state: 'delivered', bucket: 'done', count: 1 }, { state: 'qa', bucket: 'done', count: 3 },
  { state: 'cancelled', bucket: 'cancelled', count: 3 }, { state: 'archived', bucket: 'archived', count: 1 },
] }
test('bounded choices default to the current rule and labels name their set', () => {
  assert.deepEqual(hiddenStates(), HIDE_STATES)
  assert.deepEqual(hiddenStates([]), HIDE_STATES)
  for (const invalid of ['', 'open', 'done,!accepted', 'x'.repeat(326), 'done,'.repeat(6)]) assert.deepEqual(parseHiddenStates(invalid), HIDE_STATES)
  assert.deepEqual(parseHiddenStates('accepted, DONE ,canceled'), ['done', 'accepted', 'cancelled'])
  assert.equal(hideLabel(), 'Hide closed')
  assert.equal(hideLabel(['accepted', 'done', 'delivered']), 'Hide finished')
  assert.equal(hideLabel(['cancelled', 'archived']), 'Hide')
})
test('choices round-trip with the URL and saved view even while Hide is off', () => {
  const filters = filtersFromQuery({ hide_states: 'archived,accepted', closed: '1', status: 'done', status_scope: 'done', hide_restore: '1' })
  assert.deepEqual(filters.hideStates, ['accepted', 'archived'])
  const saved = viewShape(filters)
  assert.equal(saved.filters.hide_states, 'accepted,archived')
  assert.ok(sameListState(filters, filtersFromView({ id: 'view', ...saved })))
  assert.equal(apiParams('p', filters).hide_closed, false)
  assert.deepEqual(apiParams('p', filters).hide_states, ['accepted', 'archived'])
  assert.equal(apiParams('p', { ...filters, showClosed: false }).hide_closed, true)
  assert.equal(filtersToQuery(filtersFromQuery({ hide_states: HIDE_STATES.join(',') })).hide_states, undefined)
  assert.ok(sameListState(filtersFromQuery({}), filtersFromQuery({ hide_states: HIDE_STATES.join(',') })))
})
test('counts and status visibility use the same kind-aware Hide policy', () => {
  assert.deepEqual(hideChoiceCounts(summary), { done: 3, delivered: 1, accepted: 2, cancelled: 3, archived: 1 })
  assert.equal(hideChoiceCounts({ ...summary, status_counts_truncated: true }), null)
  assert.equal(hideChoiceCounts({ ...summary, status_counts: undefined }), null)
  assert.equal(statusHiddenByPolicy('qa', summary.status_counts, ['done']), true)
  assert.equal(statusHiddenByPolicy('qa', summary.status_counts, ['accepted']), false)
  assert.equal(statusHiddenByPolicy('accepted', [{ state: 'accepted', bucket: 'open', count: 2 }], ['accepted']), false)
  assert.equal(statusHiddenByPolicy('accepted', summary.status_counts, ['accepted']), true)
  assert.equal(statusHiddenByPolicy('canceled', summary.status_counts, ['cancelled']), true)
  assert.equal(selectionHiddenByPolicy(['!done'], undefined, summary.status_counts, ['done'], true), false)
  assert.equal(selectionHiddenByPolicy(['qa'], 'canonical', [], ['accepted'], true), true)
  assert.equal(selectionHiddenByPolicy(['qa'], 'in_progress', [], ['accepted'], true), false)
})
test('temporary Hide restores against the chosen policy instead of all closed work', () => {
  const hidden = selectionHiddenByPolicy(['accepted'], 'canonical', summary.status_counts, ['accepted'])
  assert.equal(hidden, true)
  assert.deepEqual(reconcileStatusHide(false, false, hidden), { showClosed: true, automatic: true, note: 'Hide is off to show the selected statuses.' })
  const nowVisible = selectionHiddenByPolicy(['accepted'], 'canonical', summary.status_counts, ['archived'])
  assert.equal(nowVisible, false)
  assert.deepEqual(reconcileStatusHide(true, true, nowVisible), { showClosed: false, automatic: false, note: 'Hide is on again.' })
  assert.equal(selectionHiddenByPolicy(['done'], 'done', summary.status_counts, ['archived']), false)
  assert.equal(selectionHiddenByPolicy(['cancelled'], 'closed', summary.status_counts, ['archived']), true)
  assert.equal(reconcileStatusHide(true, false, hidden).automatic, false)
})
test('Graph keeps closed topology until the custom Hide membership query decides', async () => {
  const { loadTicketGraphContext } = await import('../src/lib/headerGlimpse.ts')
  const original = globalThis.fetch, calls: URL[] = []
  globalThis.fetch = async url => {
    const parsed = new URL(String(url), 'http://local.test'); calls.push(parsed)
    return Response.json(parsed.pathname === '/api/tickets/graph'
      ? { nodes: [{ id: 'accepted' }, { id: 'archived' }], links: [{ source: 'accepted', target: 'archived' }], truncated: false }
      : { items: [{ id: 'accepted' }], next_cursor: null })
  }
  try {
    const result = await loadTicketGraphContext('p', filtersFromQuery({ hide_states: 'archived' }), new AbortController().signal)
    assert.equal(calls[0].searchParams.get('include_closed'), 'true')
    assert.equal(calls[1].searchParams.get('hide_closed'), 'true')
    assert.equal(calls[1].searchParams.get('hide_states'), 'archived')
    assert.deepEqual(result.visible.nodes.map(node => node.id), ['accepted'])
    assert.deepEqual(result.visible.links, [])
  } finally { globalThis.fetch = original }
})
