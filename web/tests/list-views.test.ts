// SPDX-License-Identifier: AGPL-3.0-only
// U22 (AEON-128): the list state model behind filters, saved views and grouping.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  apiParams, compareRows, dateBounds, dateLabel, effectiveSort, facetOptions, filtersFromQuery, filtersFromView, filtersToQuery, groupRows,
  hasFilters, localInstant, sameListState, suggestName, toggleIn, toggleOut, valueLabel, valueState, viewShape,
} from '../src/lib/ticketList.ts'
import type { ListItem } from '../src/lib/api.ts'

test('exclusions and every filter round-trip through the URL', () => {
  const query = { status: 'backlog,!done', tag: 'BUG,!docs', epic: 'e1,none', cost: 'Support', release: '!none', date: 'updated:7d', group: 'assignee', cols: 'status,bogus,key,updated', v: '11111111-aaaa-4aaa-8aaa-000000000001', type: '!epic,nope' }
  const filters = filtersFromQuery(query)
  assert.deepEqual(filters.status, ['backlog', '!done'])
  // A legacy kind reads as its closest Type level (AEON-974).
  assert.deepEqual(filters.type, ['!1'])
  assert.deepEqual(filters.cols, ['status', 'updated'])
  assert.deepEqual(filters.date, { field: 'updated', preset: '7d', from: null, to: null })
  assert.equal(filters.view, '11111111-aaaa-4aaa-8aaa-000000000001')
  assert.deepEqual(filtersToQuery(filters), { status: 'backlog,!done', tag: 'BUG,!docs', epic: 'e1,none', cost: 'Support', release: '!none', type: '!1', date: 'updated:7d', group: 'assignee', cols: 'status,updated', v: '11111111-aaaa-4aaa-8aaa-000000000001' })
  assert.equal(filtersFromQuery({ date: 'start:2026-09-01..' }).date?.from, '2026-09-01')
  assert.equal(filtersFromQuery({ date: 'soon:7d' }).date, null)
  assert.equal(filtersFromQuery({ v: 'not-a-view' }).view, null)
  assert.equal(hasFilters(filtersFromQuery({ date: 'created:today' })), true)
})

test('a value is included, excluded or neither; toggles switch between them', () => {
  assert.deepEqual(toggleIn([], 'qa'), ['qa'])
  assert.deepEqual(toggleIn(['qa'], 'qa'), [])
  assert.deepEqual(toggleIn(['!qa'], 'qa'), ['qa'])
  assert.deepEqual(toggleOut(['qa', 'new'], 'qa'), ['new', '!qa'])
  assert.deepEqual(toggleOut(['!qa'], 'qa'), [])
  assert.equal(valueState(['!qa'], 'qa'), 'out')
  assert.equal(valueState(['qa'], 'new'), null)
})

test('the list API gets exclusions with their spellings, Type as levels over every work kind, and day bounds', () => {
  const now = new Date(2026, 8, 23, 15, 30)
  const filters = filtersFromQuery({ status: 'new,!in-progress', type: '!epic', tag: 'BUG,!none', cost: 'Support', release: 'v4', epic: '!e1', date: 'created:30d', priority: '!none', assignee: 'none' })
  const params = apiParams('p1', filters, { now })
  assert.deepEqual(params.state, ['new', '!in-progress', '!in_progress'])
  assert.deepEqual(params.kind, ['work', 'ticket', 'task', 'epic'])
  assert.deepEqual(params.level, ['!1'])
  assert.deepEqual(params.tag, ['BUG', '!none'])
  assert.deepEqual(params.cost_unit, ['Support'])
  assert.deepEqual(params.release, ['v4'])
  assert.deepEqual(params.epic, ['!e1'])
  assert.deepEqual(params.priority, ['!none'])
  assert.equal(params.date_field, 'created')
  assert.equal(params.date_from, localInstant(new Date(2026, 7, 25)))
  assert.equal(params.date_to, localInstant(new Date(2026, 8, 24)))
  assert.deepEqual(apiParams('p1', filtersFromQuery({ type: '!work,!epic,!ticket,!task' })).level, ['!1', '!leaf'])
  assert.deepEqual(apiParams('p1', filters, { omit: 'tag' }).tag, [])
  assert.equal(apiParams('p1', filtersFromQuery({})).date_field, undefined)
})

test('dates: presets stay relative, custom ranges include their last day, labels read naturally', () => {
  const now = new Date(2026, 8, 23, 9)
  const day = (y: number, m: number, d: number) => localInstant(new Date(y, m - 1, d))
  assert.deepEqual(dateBounds({ field: 'updated', preset: 'today', from: null, to: null }, now), { from: day(2026, 9, 23), to: day(2026, 9, 24) })
  assert.deepEqual(dateBounds({ field: 'updated', preset: 'month', from: null, to: null }, now), { from: day(2026, 9, 1), to: day(2026, 10, 1) })
  assert.deepEqual(dateBounds({ field: 'updated', preset: 'year', from: null, to: null }, now), { from: day(2026, 1, 1), to: day(2027, 1, 1) })
  assert.deepEqual(dateBounds({ field: 'start', preset: null, from: '2026-09-01', to: '2026-09-15' }, now), { from: day(2026, 9, 1), to: day(2026, 9, 16) })
  assert.deepEqual(dateBounds({ field: 'start', preset: null, from: null, to: '2026-09-15' }, now), { from: null, to: day(2026, 9, 16) })
  assert.match(localInstant(new Date(2026, 8, 1)), /^2026-09-01T00:00:00[+-]\d{2}:\d{2}$/)
  assert.equal(dateLabel({ field: 'updated', preset: '7d', from: null, to: null }, now), 'last 7 days')
  assert.equal(dateLabel({ field: 'start', preset: null, from: '2026-09-01', to: '2026-09-15' }, now), '1 Sep – 15 Sep')
  assert.equal(dateLabel({ field: 'start', preset: null, from: '2025-12-30', to: null }, now), 'since 30 Dec 2025')
  assert.equal(dateLabel({ field: 'end', preset: null, from: '2026-09-05', to: '2026-09-05' }, now), 'on 5 Sep')
})

test('a saved view is the list state with a name; order of values is no change', () => {
  const filters = filtersFromQuery({ status: 'new,backlog', q: 'fleet', sort: 'priority,-updated_at', group: 'status', cols: 'status,assignee', closed: '1' })
  const shape = viewShape(filters)
  assert.deepEqual(shape, { filters: { q: 'fleet', status: 'new,backlog', closed: '1' }, sort_keys: ['priority', '-updated_at'], group_by: 'status', columns: ['key', 'title', 'status', 'assignee'], mode: 'list' })
  const back = filtersFromView({ id: '11111111-aaaa-4aaa-8aaa-000000000001', ...shape })
  assert.equal(back.view, '11111111-aaaa-4aaa-8aaa-000000000001')
  assert.ok(sameListState(back, filters))
  assert.ok(sameListState(filtersFromQuery({ status: 'backlog,new', q: 'fleet', sort: 'priority,-updated_at', group: 'status', cols: 'status,assignee', closed: '1' }), back))
  assert.ok(!sameListState(filtersFromQuery({ status: 'backlog' }), back))
  // Column order is part of the view.
  assert.ok(!sameListState(filtersFromQuery({ ...filtersToQuery(filters), cols: 'assignee,status' }), back))
  assert.equal(suggestName(filtersFromQuery({ priority: 'high', status: '!done' }), (_d, v) => v === 'high' ? 'High' : 'Done'), 'Not Done · High')
  assert.equal(suggestName(filtersFromQuery({ group: 'assignee' }), () => ''), 'By assignee')
  assert.equal(suggestName(filtersFromQuery({}), () => ''), 'My view')
})

function row(id: string, extra: Partial<ListItem> = {}): ListItem {
  return { id, key: id.toUpperCase(), kind_id: 'k', title: id, body: '', fields: {}, state: 'new', parent_id: null, position: '0', created_at: '', updated_at: '', deleted_at: null, kind_slug: 'ticket', kind_label: 'Ticket', priority: null, assignee: null, parent: null, children_count: 0, project: null, ...extra }
}

test('groups by assignee (you first), priority, type and label, with totals from the counts', () => {
  const me = { id: 'me', name: 'Zoe' }, ana = { id: 'ana', name: 'Ana' }
  const rows = [
    row('a', { assignee: ana, priority: 'low', fields: { tags: ['BUG', { name: 'ops', color: 'teal' }] } }),
    row('b', { assignee: me, priority: 'high', kind_slug: 'task', fields: { tags: ['bug'] } }),
    row('c', { priority: 'none', kind_slug: 'epic' }),
  ]
  assert.deepEqual(groupRows(rows, 'assignee', { none: 4 }, { me: 'me' }).map(g => [g.label, g.total]), [['Zoe', 1], ['Ana', 1], ['Unassigned', 4]])
  assert.deepEqual(groupRows(rows, 'priority').map(g => g.label), ['High', 'Low', 'No priority'])
  assert.deepEqual(groupRows(rows, 'type').map(g => g.label), ['Epic', 'Ticket', 'Task'])
  const tags = groupRows(rows, 'tag', { BUG: 3, bug: 1, ops: 1, none: 1 })
  assert.deepEqual(tags.map(g => [g.label, g.rows.map(r => r.id), g.total]), [['BUG', ['a', 'b'], 4], ['ops', ['a'], 1], ['No labels', ['c'], 1]])
  assert.equal(tags[1].tag?.color, 'teal')
})

test('options and chip words for labels, epics and people', () => {
  const labels = facetOptions('tag', { BUG: 2, bug: 1, docs: 1, none: 3 }, ['!ops'], new Map(), undefined, { colors: new Map([['bug', 'red']]) })
  assert.deepEqual(labels.map(o => [o.value, o.count]), [['none', 3], ['BUG', 3], ['docs', 1], ['ops', 0]])
  assert.equal(labels[1].color, 'red')
  const epics = facetOptions('epic', {}, ['e9'], new Map(), undefined, { epics: [{ id: 'e1', key: 'P-1', title: 'Fleet' }] })
  assert.deepEqual(epics.map(o => o.label), ['No parent', 'Fleet', 'Parent'])
  assert.equal(valueLabel('assignee', 'me', { me: 'me' }), 'Me')
  assert.equal(valueLabel('epic', 'e1', { epics: [{ id: 'e1', key: 'P-1', title: 'Fleet' }] }), 'Fleet')
  assert.equal(valueLabel('tag', 'none'), 'No labels')
  assert.equal(valueLabel('status', 'in-progress'), 'In progress')
})

test('grouping leads the sort so groups stay together; unassigned work sorts last both ways', () => {
  assert.deepEqual(effectiveSort(filtersFromQuery({ group: 'assignee', sort: 'priority' })), [{ field: 'assignee', desc: false }, { field: 'priority', desc: false }, { field: 'updated_at', desc: true }])
  assert.deepEqual(effectiveSort(filtersFromQuery({ group: 'type' }))[0], { field: 'kind', desc: false })
  assert.equal(effectiveSort(filtersFromQuery({ group: 'tag' }))[0].field, 'updated_at')
  const people = [row('x'), row('y', { assignee: { id: '1', name: 'Bea' } }), row('z', { assignee: { id: '2', name: 'Al' } })]
  assert.deepEqual([...people].sort(compareRows([{ field: 'assignee', desc: false }])).map(r => r.id), ['z', 'y', 'x'])
  assert.deepEqual([...people].sort(compareRows([{ field: 'assignee', desc: true }])).map(r => r.id), ['y', 'z', 'x'])
})

test('the palette offers a project’s views by name, after recent work and before actions', async () => {
  const { assemble, viewResults } = await import('../src/lib/palette.ts')
  const views = [{ id: 'v1', name: 'Bugs to fix', shared: true, mine: false, isDefault: false }, { id: 'v2', name: 'My week', shared: false, mine: true, isDefault: true }]
  const all = viewResults('', views)
  assert.deepEqual(all.map(v => [v.id, v.hint]), [['view:v1', 'Shared view'], ['view:v2', 'Your view · opens first']])
  assert.deepEqual(viewResults('bugs', views).map(v => v.label), ['Bugs to fix'])
  assert.deepEqual(viewResults('view week', views).map(v => v.label), ['My week'])
  const empty = assemble('', { recent: [], tickets: [], projects: [], actions: [{ type: 'action', id: 'a', label: 'Act', icon: 'plus' }], views: all })
  assert.deepEqual(empty.map(g => g.id), ['views', 'actions'])
  const typed = assemble('bugs', { recent: [], tickets: [], projects: [], actions: [], views: viewResults('bugs', views) })
  assert.deepEqual(typed.map(g => g.id), ['views'])
})

// AEON-718 regression: modes are saved even for List, and a pinned-only choice
// remains explicit rather than falling back to the person's columns.
test('saved views retain each mode and an explicit pinned-only column choice', () => {
  for (const mode of ['list', 'outline', 'graph'] as const) {
    const filters = filtersFromQuery({ view: mode, cols: 'key,title', priority: 'high' })
    const shape = viewShape(filters)
    assert.equal(shape.mode, mode)
    assert.deepEqual(shape.columns, ['key', 'title'])
    assert.equal(shape.filters.view, undefined)
    const reopened = filtersFromView({ id: '11111111-aaaa-4aaa-8aaa-000000000001', ...shape })
    assert.equal(reopened.mode, mode)
    assert.deepEqual(reopened.cols, [])
    assert.equal(sameListState(filters, reopened), true)
    assert.equal(sameListState({ ...reopened, mode: mode === 'list' ? 'graph' : 'list' }, reopened), false)
    assert.equal(sameListState({ ...reopened, cols: ['updated'] }, reopened), false)
  }
})
