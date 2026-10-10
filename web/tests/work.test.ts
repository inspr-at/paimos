// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { cycleSort, descriptionLine, highlight, initials, parseSort, priorityLabel, projectRouteKey, relativeTime, serializeSort, statusMeta, statusOptions } from '../src/lib/work.ts'
import { apiParams, compareRows, effectiveSort, epicOf, facetOptions, filtersFromQuery, filtersToQuery, groupRows, orderByStatus, totalFrom } from '../src/lib/ticketList.ts'
import type { ListItem } from '../src/lib/api.ts'

test('statuses read the same in every spelling and carry product labels', () => {
  assert.equal(statusMeta('in-progress').label, 'In progress')
  assert.equal(statusMeta('in_progress').key, 'progress')
  assert.equal(statusMeta('active').key, 'progress')
  assert.equal(statusMeta('qa').label, 'QA')
  assert.equal(statusMeta('done').closed, true)
  assert.equal(statusMeta('on_hold').label, 'On hold')
  assert.equal(statusMeta('open').label, 'Open')
  assert.equal(statusMeta('open').key, 'open')
  assert.equal(statusMeta('open').closed, false)
  assert.equal(statusMeta('blocked').label, 'Blocked')
  assert.equal(statusMeta('blocked').closed, false)
  assert.ok(statusMeta('new').order < statusMeta('backlog').order && statusMeta('backlog').order < statusMeta('open').order)
  assert.ok(statusMeta('blocked').order > statusMeta('backlog').order)
  assert.ok(statusMeta('blocked').order < statusMeta('in_progress').order)
  assert.deepEqual(statusOptions(['in-progress']).map(o => o.value), ['new', 'backlog', 'open', 'blocked', 'in-progress', 'qa', 'done', 'delivered', 'accepted', 'cancelled', 'archived'])
  assert.equal(statusOptions([]).find(o => o.meta.key === 'progress')!.value, 'in_progress')
})

test('priorities, initials and descriptions', () => {
  assert.equal(priorityLabel('high'), 'High')
  assert.equal(priorityLabel(null), 'No priority')
  assert.equal(initials('Markus Barta'), 'MB')
  assert.equal(initials('mba'), 'MB')
  assert.equal(descriptionLine('# GLINT\n\nGlanceable, **truthful** [ticket](https://x) context.\n\nMore.'), 'Glanceable, truthful ticket context.')
  assert.equal(projectRouteKey('PRJ-17', { classic: { key: 'PHAROS' } }), 'PHAROS')
  assert.equal(projectRouteKey('PRJ-26', { classic: {} }), 'PRJ-26')
})

test('relative time is compact in tables and prose elsewhere', () => {
  const now = Date.parse('2026-09-23T12:00:00Z')
  assert.equal(relativeTime('2026-09-23T11:59:30Z', { now }), 'just now')
  assert.equal(relativeTime('2026-09-23T11:15:00Z', { now }), '45m ago')
  assert.equal(relativeTime('2026-09-23T09:00:00Z', { now, long: true }), '3 hours ago')
  assert.equal(relativeTime('2026-09-22T09:00:00Z', { now }), 'yesterday')
  assert.equal(relativeTime('2026-09-19T12:00:00Z', { now }), '4d ago')
  assert.equal(relativeTime('2026-09-01T12:00:00Z', { now }), '1 Sep')
  assert.equal(relativeTime('2025-12-24T12:00:00Z', { now }), '24 Dec 2025')
})

test('highlight splits every case-insensitive match', () => {
  assert.deepEqual(highlight('Hetzner and hetzner', 'HETZ'), [
    { text: 'Hetz', match: true }, { text: 'ner and ', match: false }, { text: 'hetz', match: true }, { text: 'ner', match: false },
  ])
  assert.deepEqual(highlight('abc', ''), [{ text: 'abc', match: false }])
})

test('sort keys cycle ascending, descending, off; shift adds secondary keys', () => {
  assert.deepEqual(parseSort('state,-updated_at,bogus,state'), [{ field: 'state', desc: false }, { field: 'updated_at', desc: true }])
  assert.equal(serializeSort([{ field: 'priority', desc: true }, { field: 'key', desc: false }]), '-priority,key')
  let keys = cycleSort([], 'priority', false)
  assert.deepEqual(keys, [{ field: 'priority', desc: false }])
  keys = cycleSort(keys, 'priority', false)
  assert.deepEqual(keys, [{ field: 'priority', desc: true }])
  assert.deepEqual(cycleSort(keys, 'priority', false), [])
  keys = cycleSort([{ field: 'priority', desc: false }], 'key', true)
  assert.deepEqual(keys, [{ field: 'priority', desc: false }, { field: 'key', desc: false }])
  assert.deepEqual(cycleSort(cycleSort(keys, 'key', true), 'key', true), [{ field: 'priority', desc: false }])
})

test('list state round-trips through the URL and maps onto the list API', () => {
  const filters = filtersFromQuery({ q: ' pill ', status: 'in-progress,qa', priority: 'high', type: 'ticket,bogus', closed: '1', sort: 'priority', group: 'status' })
  assert.deepEqual(filters.type, ['ticket'])
  assert.deepEqual(filtersToQuery(filters), { q: 'pill', status: 'in-progress,qa', priority: 'high', type: 'ticket', closed: '1', sort: 'priority', group: 'status' })
  assert.deepEqual(effectiveSort(filters), [{ field: 'state', desc: false }, { field: 'priority', desc: false }, { field: 'updated_at', desc: true }])
  const params = apiParams('p1', filters, { facets: ['state'] })
  assert.deepEqual(params.state, ['in-progress', 'in_progress', 'qa'])
  assert.equal(params.hide_closed, false)
  assert.equal(params.sort, 'state,priority,-updated_at')
  assert.equal(params.limit, 200)
  assert.deepEqual(apiParams('p1', filters, { omit: 'status' }).state, [])
  const plain = filtersFromQuery({})
  assert.equal(apiParams('p1', plain).sort, '-updated_at')
  assert.equal(apiParams('p1', plain).hide_closed, true)
  assert.deepEqual(apiParams('p1', plain).kind, ['work', 'ticket', 'task', 'epic'])
})

test('facet options merge spellings and count once', () => {
  const status = facetOptions('status', { 'in-progress': 9, new: 1, backlog: 8 })
  assert.deepEqual(status.slice(0, 5).map(o => [o.value, o.count]), [['new', 1], ['backlog', 8], ['open', 0], ['blocked', 0], ['in-progress', 9]])
  const people = facetOptions('assignee', { none: 3, b: 1, a: 5 }, [], new Map([['a', 'Ana'], ['b', 'Ben']]), 'b')
  assert.deepEqual(people.map(o => o.label), ['Unassigned', 'Ben', 'Ana'])
  assert.equal(totalFrom({ kind: { epic: 2, ticket: 19, task: 5 } }), 26)
  assert.equal(totalFrom(undefined), null)
})

function row(id: string, state: string, kind = 'ticket', parent: ListItem['parent'] = null): ListItem {
  return { id, key: id.toUpperCase(), kind_id: kind, title: id, body: '', fields: {}, state, parent_id: parent?.id ?? null, position: '0', created_at: '', updated_at: '', kind_slug: kind, kind_label: kind, priority: null, assignee: null, parent, children_count: 0, project: null }
}

test('status order and grouping follow the workflow, epics collect their tickets', () => {
  const rows = [row('a', 'qa'), row('b', 'in-progress'), row('c', 'new'), row('d', 'backlog'), row('e', 'done'), row('f', 'accepted'), row('g', 'delivered'), row('h', 'in_progress')]
  assert.deepEqual(orderByStatus(rows).map(r => r.id), ['c', 'd', 'b', 'h', 'a', 'e', 'g', 'f'])
  assert.deepEqual(orderByStatus([row('k', 'blocked'), row('d', 'backlog'), row('b', 'in-progress')]).map(r => r.id), ['d', 'k', 'b'])
  assert.deepEqual(groupRows(rows, 'status', { 'in-progress': 4 }).map(g => [g.label, g.total]), [['New', 1], ['Backlog', 1], ['In progress', 4], ['QA', 1], ['Done', 1], ['Delivered', 1], ['Accepted', 1]])
  const epic = row('e', 'backlog', 'epic')
  const ticket = row('t', 'new', 'ticket', { id: 'e', key: 'E', title: 'e', kind_slug: 'epic' })
  const task = row('k', 'qa', 'task', { id: 't', key: 'T', title: 't', kind_slug: 'ticket' })
  const loose = row('l', 'new', 'ticket', { id: 'p', key: 'P', title: 'p', kind_slug: 'project' })
  const byId = new Map([epic, ticket, task, loose].map(r => [r.id, r]))
  assert.equal(epicOf(task, byId)?.id, 'e')
  const groups = groupRows([epic, ticket, task, loose], 'epic')
  assert.deepEqual(groups.map(g => [g.label, g.rows.map(r => r.id)]), [['e', ['t', 'k']], ['No parent', ['l']]])
})

test('descending status order keeps an unknown spelling last, and pages agree', () => {
  const rows = [row('m', 'mystery'), row('a', 'archived'), row('d', 'done'), row('o', 'open'), row('s', ' OPEN '), row('q', ' QA '), row('p', 'in--progress')]
  const asc = ['o', 's', 'p', 'q', 'd', 'a', 'm']
  const desc = ['a', 'd', 'q', 'p', 'o', 's', 'm']
  assert.deepEqual(orderByStatus(rows).map(r => r.id), asc)
  assert.deepEqual(orderByStatus(rows, true).map(r => r.id), desc)
  assert.deepEqual([...rows].sort(compareRows([{ field: 'state', desc: false }])).map(r => r.id), asc)
  assert.deepEqual([...rows].sort(compareRows([{ field: 'state', desc: true }])).map(r => r.id), desc)
  const full = orderByStatus(rows, true)
  const paged: typeof full = []
  for (let i = 0; i < full.length; i += 2) paged.push(...orderByStatus(full.slice(i, i + 2), true))
  assert.deepEqual(paged.map(r => r.id), desc)
})
