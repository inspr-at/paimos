// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { apiParams, clearedFilters, filtersFromQuery, filtersFromView, filtersToQuery, viewShape } from '../src/lib/ticketList.ts'
import { canonicalStatus, groupStates, groupTotal, HEADER_STATUS_GROUPS, reconcileStatusHide, selectionIsHidden, statusCount, statusIsHidden } from '../src/lib/projectStatusCounts.ts'
import type { ProjectSummary } from '../src/lib/api.ts'
const summary: ProjectSummary = { id: 'project', key: 'AEON', title: 'Aeon', state: 'active', open: 3, in_progress: 2, done: 4, cancelled: 1, archived_count: 2, total: 12, last_activity: '', status_counts: [
  { state: 'qa', bucket: 'open', count: 2 }, { state: 'qa', bucket: 'done', count: 1 },
  { state: 'custom', bucket: 'in_progress', count: 2 }, { state: 'done', bucket: 'done', count: 3 },
] }
test('the grid includes all eleven statuses in the approved four groups', () => {
  assert.deepEqual(HEADER_STATUS_GROUPS.map(group => group.states), [
    ['new', 'backlog', 'open', 'blocked'], ['in_progress', 'qa'], ['done', 'delivered', 'accepted'], ['cancelled', 'archived'],
  ])
  assert.deepEqual(HEADER_STATUS_GROUPS.map(group => groupTotal(summary, group.id)), [3, 2, 4, 3])
  assert.equal(statusCount(summary, 'qa'), 3)
  assert.equal(statusCount(summary, 'new'), 0)
  assert.equal(statusCount({ ...summary, status_counts: undefined }, 'new'), null)
  assert.equal(statusCount({ ...summary, status_counts_truncated: true }, 'qa'), null)
})
test('all legacy variants map to one canonical status', () => {
  for (const state of ['active', 'inprogress', 'in--progress', ' IN progress ']) assert.equal(canonicalStatus(state), 'in_progress')
  assert.equal(canonicalStatus('canceled'), 'cancelled')
})
test('Hide reads kind-aware buckets and supports a configurable hidden set', () => {
  assert.equal(statusIsHidden('qa', summary.status_counts), true)
  assert.equal(statusIsHidden('custom', summary.status_counts), false)
  assert.equal(statusIsHidden('qa', [{ state: 'qa', bucket: 'open', count: 2 }]), false)
  assert.equal(statusIsHidden('done', summary.status_counts, ['archived']), false)
  assert.equal(selectionIsHidden(['done'], 'done', summary.status_counts), true)
  assert.equal(selectionIsHidden(['!done'], undefined, summary.status_counts), false)
  assert.equal(selectionIsHidden([], 'done', summary.status_counts), false)
  assert.equal(selectionIsHidden(['cancelled'], 'closed', summary.status_counts, ['archived']), true)
})
test('automatic Hide restores on clear or visible status; manual off remains off', () => {
  const selected = reconcileStatusHide(false, false, true)
  assert.equal(selected.showClosed, true); assert.equal(selected.automatic, true); assert.ok(selected.note)
  assert.deepEqual(reconcileStatusHide(true, true, false), { showClosed: false, automatic: false, note: 'Hide is on again.' })
  assert.deepEqual(reconcileStatusHide(true, false, false), { showClosed: true, automatic: false, note: '' })
  assert.deepEqual(reconcileStatusHide(true, true, true), { showClosed: true, automatic: true, note: '' })
})
test('truncated custom categories cannot prove a selected status is visible under Hide', () => {
  // QA may be configured as done and omitted, or its hidden bucket may be
  // omitted while an open bucket for another kind is still present.
  for (const counts of [[], [{ state: 'qa', bucket: 'open' as const, count: 2 }]]) {
    const hidden = selectionIsHidden(['qa'], 'canonical', counts, undefined, true)
    assert.equal(hidden, true)
    assert.deepEqual(reconcileStatusHide(false, false, hidden), {
      showClosed: true, automatic: true, note: 'Hide is off to show the selected statuses.',
    })
    assert.equal(selectionIsHidden([], 'canonical', counts, undefined, true), false)
    assert.equal(selectionIsHidden(['!qa'], 'canonical', counts, undefined, true), false)
    assert.equal(selectionIsHidden(['qa'], 'canonical', counts, [], true), false)
    // Group scopes carry complete bucket information even in a partial summary.
    assert.equal(selectionIsHidden(['qa'], 'in_progress', counts, undefined, true), false)
  }
  assert.equal(selectionIsHidden(['qa'], 'canonical', [{ state: 'qa', bucket: 'open', count: 2 }], undefined, false), false)
})
test('group links and saved views use exact server buckets even with custom states', () => {
  const group = HEADER_STATUS_GROUPS[1]
  const filters = filtersFromQuery({ status: groupStates(summary, group).join(','), status_scope: group.id, priority: 'high', closed: '1' })
  const params = apiParams('project', filters)
  assert.deepEqual(params.state, [])
  assert.deepEqual(params.work_bucket, ['in_progress'])
  assert.deepEqual(params.priority, ['high'])
  assert.equal(params.hide_closed, false)
  const saved = viewShape(filters)
  const restored = filtersFromView({ id: 'view', ...saved })
  assert.deepEqual(apiParams('project', restored).work_bucket, params.work_bucket)
  assert.equal(filtersToQuery(restored).status_scope, 'in_progress')
  assert.equal(apiParams('project', filters, { omit: 'status' }).work_bucket, undefined)
})
test('single-status header links use canonical selectors; ordinary state filters stay exact', () => {
  const filters = filtersFromQuery({ status: 'in_progress', status_scope: 'canonical' })
  assert.deepEqual(apiParams('project', filters).work_state, ['in_progress'])
  assert.deepEqual(apiParams('project', filters).state, [])
  assert.equal(apiParams('project', filters, { omit: 'status' }).work_state, undefined)
  assert.deepEqual(apiParams('project', filtersFromQuery({ status: 'in_progress' })).state, ['in_progress', 'in-progress'])
  assert.equal(filtersToQuery({ ...filters, ...clearedFilters() }).status_scope, undefined)
  assert.equal(filtersFromQuery({ status_scope: 'done' }).statusScope, undefined)
  assert.equal(filtersFromQuery({ status: 'done', status_scope: 'invalid' }).statusScope, undefined)
})

test('automatic Hide restoration travels with links and saved views; manual shown work does not gain an override', () => {
  const filters = filtersFromQuery({ status: 'done', status_scope: 'done', closed: '1', hide_restore: '1' })
  assert.equal(filters.hideRestore, true)
  const saved = viewShape(filters)
  assert.equal(filtersFromView({ id: 'view', ...saved }).hideRestore, true)
  assert.equal(filtersToQuery(filters).hide_restore, '1')
  assert.equal(filtersFromQuery({ status: 'done', closed: '1' }).hideRestore, undefined)
  assert.equal(filtersFromQuery({ closed: '1', hide_restore: '1' }).hideRestore, undefined)
  assert.equal(filtersFromQuery({ status: 'done', hide_restore: '1' }).hideRestore, undefined)
  assert.equal(filtersToQuery({ ...filters, ...clearedFilters() }).hide_restore, undefined)
})
