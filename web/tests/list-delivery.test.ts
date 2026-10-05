// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { DeliveryOrder, ListItem } from '../src/lib/api.ts'
import { apiParams, clearedFilters, compareRows, effectiveSort, facetOptions, filtersFromQuery, filtersFromView, filtersToQuery, valueLabel, viewShape } from '../src/lib/ticketList.ts'
import { parseSort } from '../src/lib/work.ts'
import { queueFilteredNodes } from '../src/lib/queueFilter.ts'
import type { QueueSnapshot } from '../src/lib/workQueue.ts'

const releaseA = '11111111-1111-4111-8111-111111111111'
const releaseB = '22222222-2222-4222-8222-222222222222'
function row(id: string, order?: DeliveryOrder, extra: Partial<ListItem> = {}): ListItem {
  return { id, key: id, kind_id: 'ticket', kind_slug: 'ticket', kind_label: 'Ticket', title: id, body: '', fields: {}, state: 'open', parent_id: null, position: '0', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-02-01T00:00:00Z', deleted_at: null, assignee: null, parent: null, project: null, children_count: 0, priority: null, ...(order ? { delivery_order: order } : {}), ...extra }
}
const placed = (release_rank: string | null, rank: string | null, expedite = false): DeliveryOrder => ({ release_id: release_rank ? releaseA : null, release_rank, rank, expedite })

test('release tail and journey order retain microseconds in both directions before the UUID tie-break', () => {
  for (const order of [undefined, placed(null, null)]) {
    const older = row(releaseB, order, { created_at: '2026-01-01T00:00:00.123001Z' })
    const newer = row(releaseA, order, { created_at: '2026-01-01T01:00:00.123002+01:00' })
    assert.deepEqual([newer, older].toSorted(compareRows([{ field: 'order', desc: false }])).map(item => item.id), [releaseB, releaseA])
    assert.deepEqual([older, newer].toSorted(compareRows([{ field: 'order', desc: true }])).map(item => item.id), [releaseA, releaseB])
    const sameTime = row(releaseA, order, { created_at: older.created_at })
    assert.deepEqual([older, sameTime].toSorted(compareRows([{ field: 'order', desc: false }])).map(item => item.id), [releaseA, releaseB])
  }
})

test('single release/Backlog scopes survive views and intersect ordinary filters; ambiguous legacy scopes require repair', () => {
  for (const scope of [releaseA, 'none']) {
    const filters = filtersFromQuery({ ships_in: scope, release: 'imported-v1', status: '!done', sort: 'order' })
    assert.deepEqual(apiParams('project', filters).ships_in, [scope])
    assert.deepEqual(apiParams('project', filters).release, ['imported-v1'])
    assert.deepEqual(filtersFromView({ id: releaseB, ...viewShape(filters) }).ships_in, [scope])
    assert.equal(filtersToQuery(filters).ships_in, scope)
    assert.equal(apiParams('project', filters, { omit: 'ships_in' }).ships_in, undefined)
  }
  assert.equal(apiParams('project', filtersFromQuery({})).ships_in, undefined)
  for (const raw of [`${releaseA},${releaseB}`, `!none`, `bad`, `${releaseA},!none`]) {
    const filters = filtersFromQuery({ ships_in: raw, priority: 'high' })
    assert.equal(filtersToQuery(filters).ships_in, raw)
    assert.throws(() => apiParams('project', filters), /Choose a release scope/)
    assert.deepEqual(filters.priority, ['high'])
  }
  assert.deepEqual(clearedFilters().ships_in, [])
  assert.deepEqual(parseSort('-order'), [{ field: 'order', desc: true }])
})

test('client order matches expedite, release position, C rank bytes, backlog, tail age and id', () => {
  const rows = [
    row('release-Z', placed('B', 'Z'), { priority: 'low' }),
    row('release-a', placed('B', 'a'), { priority: 'high' }),
    row('later', placed('D', 'V')),
    row('urgent', placed('D', 'Z', true)),
    row('backlog-Z', placed(null, 'Z')),
    row('backlog-a', placed(null, 'a')),
    row('tail-new', placed(null, null), { created_at: '2026-01-02T00:00:00Z', priority: 'high' }),
    row('tail-old-b', placed(null, null)),
    row('tail-old-a', placed(null, null)),
  ]
  const compare = compareRows([{ field: 'order', desc: false }, { field: 'updated_at', desc: true }])
  assert.deepEqual(rows.toSorted(compare).map(row => row.id), ['urgent', 'release-Z', 'release-a', 'later', 'backlog-Z', 'backlog-a', 'tail-old-a', 'tail-old-b', 'tail-new'])
  // Later multi-sort keys cannot override order's created/id tie-breaks.
  assert.ok(compare(row('a', placed(null, null)), row('b', placed(null, null), { updated_at: '2027-01-01T00:00:00Z' })) < 0)
  assert.deepEqual([row('low', undefined, { priority: 'low' }), row('high', undefined, { priority: 'high', created_at: '2026-01-03T00:00:00Z' }), row('medium', undefined, { priority: 'medium' })].toSorted(compare).map(row => row.id), ['high', 'medium', 'low'])
  assert.deepEqual(rows.slice(0, 3).toSorted(compareRows([{ field: 'order', desc: true }])).map(row => row.id), ['later', 'release-a', 'release-Z'])
})

test('facet choices retain UUID identity and positions while counts change', () => {
  const releases = { [releaseA]: 'Release 122', [releaseB]: 'Audit sweep' }
  const options = facetOptions('ships_in', { [releaseB]: 8, [releaseA]: 2, none: 3 }, [], new Map(), undefined, { releases })
  assert.deepEqual(options.map(option => [option.value, option.label, option.count]), [['none', 'No release (backlog)', 3], [releaseA, 'Release 122', 2], [releaseB, 'Audit sweep', 8]])
  const changed = facetOptions('ships_in', { [releaseA]: 9 }, [releaseA], new Map(), undefined, { releases })
  assert.deepEqual(changed.map(option => option.value), options.map(option => option.value))
  assert.equal(valueLabel('ships_in', releaseB, { releases }), 'Audit sweep')
  assert.equal(valueLabel('ships_in', 'none'), 'Backlog')
  const sameNames = facetOptions('ships_in', { [releaseA]: 2, [releaseB]: 3 }, [], new Map(), undefined, { releases: { [releaseA]: 'Same', [releaseB]: 'Same' } })
  assert.deepEqual(sameNames.map(option => option.value), ['none', releaseA, releaseB])
})

test('Queued composes membership facets and labels without treating journey work as backlog', async () => {
  const items = [row('second', placed('B', 'a')), row('first', placed('B', 'Z')), row('backlog', placed(null, null)), row('journey')]
  const snapshot = { items: items.map(item => ({ ticket_id: item.id })), manual_order: false, capacity: { hours: 5, total: 2 } } as QueueSnapshot
  const queries: import('../src/lib/api.ts').ListQuery[] = []
  const page = await queueFilteredNodes({ state: ['queued'], facets: ['ships_in'], sort: 'order' }, snapshot, async query => {
    queries.push(query)
    return { items: items.filter(item => query.ids?.includes(item.id)), next_cursor: null, facet_labels: { ships_in: { [releaseA]: 'Release 122', [releaseB]: 'Unrelated' } } }
  })
  assert.deepEqual(queries[0].facets, ['ships_in'])
  assert.deepEqual(page.facets?.ships_in, { [releaseA]: 2, none: 1 })
  assert.deepEqual(page.facet_labels?.ships_in, { [releaseA]: 'Release 122' })
  assert.deepEqual(page.items.map(item => item.id), ['first', 'second', 'backlog', 'journey'])
})
