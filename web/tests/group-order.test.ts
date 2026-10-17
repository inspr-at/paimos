// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1390: sorting in grouped lists. Risk: clicking the grouped-by column does nothing visible (it sorts rows that
// share one value), "No …" groups jump to the top when reversed, rows inside groups fall into an arbitrary order, the
// group order is lost on reload or in a saved view, or a second sort model disagrees with the header.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { ListItem } from '../src/lib/api.ts'
import { effectiveSort, filtersFromQuery, filtersFromView, filtersToQuery, groupOrderTip, groupRows, rowOrderTip, sameListState, viewShape, withGrouping, withRowSort } from '../src/lib/ticketList.ts'

const row = (id: string, extra: Partial<ListItem> = {}) => ({ id, key: `P-${id}`, title: id, state: 'open', kind_slug: 'work', priority: null, assignee: null, updated_at: '2026-10-01T00:00:00Z', created_at: '2026-10-01T00:00:00Z', fields: {}, ...extra }) as unknown as ListItem
const labels = (groups: { label: string }[]) => groups.map(entry => entry.label)

test('the grouped-by column orders the groups: natural or reversed, never a row sort key', () => {
  const natural = filtersFromQuery({ group: 'status', sort: 'estimate' })
  assert.deepEqual(effectiveSort(natural), [{ field: 'state', desc: false }, { field: 'estimate', desc: false }, { field: 'updated_at', desc: true }])
  const reversed = filtersFromQuery({ group: 'status', group_order: 'reversed', sort: '-estimate' })
  assert.equal(reversed.groupDesc, true)
  assert.deepEqual(effectiveSort(reversed), [{ field: 'state', desc: true }, { field: 'estimate', desc: true }, { field: 'updated_at', desc: true }])
  // Only the group order set: the rows inside use the list's default order, never an arbitrary one.
  assert.deepEqual(effectiveSort(filtersFromQuery({ group: 'priority', group_order: 'reversed' })), [{ field: 'priority', desc: true }, { field: 'updated_at', desc: true }])
  // An older link sorted by the grouped-by column: its direction becomes the group order and it leaves the row sort.
  const legacy = filtersFromQuery({ group: 'status', sort: '-state,priority' })
  assert.equal(legacy.groupDesc, true)
  assert.deepEqual(legacy.sort, [{ field: 'priority', desc: false }])
  assert.deepEqual(filtersToQuery(legacy), { sort: 'priority', group: 'status', group_order: 'reversed' })
  // No grouping, no group order.
  assert.equal(filtersFromQuery({ group_order: 'reversed' }).groupDesc, undefined)
  assert.equal(filtersToQuery({ ...filtersFromQuery({}), groupDesc: true }).group_order, undefined)
})

test('switching the grouping or adding the grouped-by column as a row sort moves it to the group order', () => {
  const sorted = filtersFromQuery({ sort: '-priority,estimate' })
  assert.deepEqual(withGrouping(sorted, 'priority'), { group: 'priority', sort: [{ field: 'estimate', desc: false }], groupDesc: true })
  assert.deepEqual(withGrouping(filtersFromQuery({ group: 'status', group_order: 'reversed', sort: 'estimate' }), 'assignee'), { group: 'assignee', sort: [{ field: 'estimate', desc: false }], groupDesc: undefined })
  assert.deepEqual(withRowSort(filtersFromQuery({ group: 'status' }), [{ field: 'state', desc: true }, { field: 'key', desc: false }]), { sort: [{ field: 'key', desc: false }], groupDesc: true })
  assert.deepEqual(withRowSort(filtersFromQuery({ group: 'status', group_order: 'reversed' }), [{ field: 'key', desc: false }]), { sort: [{ field: 'key', desc: false }], groupDesc: true })
})

test('reversed groups keep the "No …" groups and unknown statuses last, rows keep their order inside', () => {
  const people = [row('a', { assignee: { id: 'u2', name: 'Bea' } }), row('b'), row('c', { assignee: { id: 'u1', name: 'Al' } }), row('d', { assignee: { id: 'me', name: 'Zoe' } })]
  assert.deepEqual(labels(groupRows(people, 'assignee', {}, { me: 'me' })), ['Zoe', 'Al', 'Bea', 'Unassigned'])
  assert.deepEqual(labels(groupRows(people, 'assignee', {}, { me: 'me', reversed: true })), ['Bea', 'Al', 'Zoe', 'Unassigned'])
  const prio = [row('a', { priority: 'low' }), row('b', { priority: 'high' }), row('c'), row('d', { priority: 'medium' })]
  assert.deepEqual(labels(groupRows(prio, 'priority', {}, { reversed: true })), ['Low', 'Medium', 'High', 'No priority'])
  const states = [row('a', { state: 'triage' }), row('b', { state: 'qa' }), row('c', { state: 'new' }), row('d', { state: 'in_progress' }), row('e', { state: 'qa' })]
  assert.deepEqual(labels(groupRows(states, 'status', {}, { reversed: true })), ['QA', 'In progress', 'New', 'Triage'])
  assert.deepEqual(groupRows(states, 'status', {}, { reversed: true })[0].rows.map(entry => entry.id), ['b', 'e'], 'rows keep the row sort inside a group')
  const tags = [row('a', { fields: { tags: ['ops'] } }), row('b', { fields: { tags: ['bug', 'docs'] } }), row('c')]
  assert.deepEqual(labels(groupRows(tags, 'tag', {}, { reversed: true })), ['ops', 'docs', 'bug', 'No labels'])
})

test('a saved view keeps the group order with the grouping, and a changed order makes the view differ', () => {
  const filters = filtersFromQuery({ group: 'priority', group_order: 'reversed', sort: 'key' })
  const shape = viewShape(filters)
  assert.equal(shape.group_by, 'priority')
  assert.equal(shape.filters.group_order, 'reversed')
  const view = { id: '00000000-0000-4000-8000-000000000001', filters: shape.filters, sort_keys: shape.sort_keys, group_by: shape.group_by, columns: shape.columns, mode: shape.mode }
  const back = filtersFromView(view)
  assert.equal(back.groupDesc, true)
  assert.deepEqual(back.sort, [{ field: 'key', desc: false }])
  assert.ok(sameListState(back, filters))
  assert.ok(!sameListState({ ...back, groupDesc: undefined }, filters))
})

test('the arrows’ tooltips name the group order and the order inside each group', () => {
  assert.equal(groupOrderTip('status', false), 'Groups: workflow order')
  assert.equal(groupOrderTip('status', true), 'Groups: reversed')
  assert.equal(groupOrderTip('priority', false), 'Groups: high to low')
  assert.equal(rowOrderTip({ field: 'estimate', desc: true }), 'Tickets in each group: highest estimate first')
})
