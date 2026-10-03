// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { DeliveryOrder, ListItem } from '../src/lib/api.ts'
import { apiParams, clearedFilters, compareRows, effectiveSort, facetOptions, filtersFromQuery, filtersFromView, filtersToQuery, valueLabel, viewShape } from '../src/lib/ticketList.ts'
import { parseSort } from '../src/lib/work.ts'

const releaseA = '11111111-1111-4111-8111-111111111111'
const releaseB = '22222222-2222-4222-8222-222222222222'
function row(id: string, order?: DeliveryOrder, extra: Partial<ListItem> = {}): ListItem {
  return { id, key: id, kind_id: 'ticket', kind_slug: 'ticket', kind_label: 'Ticket', title: id, body: '', fields: {}, state: 'open', parent_id: null, position: '0', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-02-01T00:00:00Z', deleted_at: null, assignee: null, parent: null, project: null, children_count: 0, priority: null, ...(order ? { delivery_order: order } : {}), ...extra }
}
const placed = (release_rank: string | null, rank: string | null, expedite = false): DeliveryOrder => ({ release_id: release_rank ? releaseA : null, release_rank, rank, expedite })

test('ships_in is distinct from imported release and survives URL, API, exclusions and saved views', () => {
  const filters = filtersFromQuery({ ships_in: `${releaseA},!none`, release: 'imported-v1', sort: 'order' })
  assert.deepEqual(filters.ships_in, [releaseA, '!none'])
  assert.deepEqual(filtersToQuery(filters), { ships_in: `${releaseA},!none`, release: 'imported-v1', sort: 'order' })
  const params = apiParams('project', filters)
  assert.deepEqual(params.ships_in, [releaseA, '!none'])
  assert.deepEqual(params.release, ['imported-v1'])
  assert.equal(params.sort, 'order')
  assert.deepEqual(apiParams('project', filters, { omit: 'ships_in' }).ships_in, [])
  assert.deepEqual(apiParams('project', filters, { omit: 'ships_in' }).release, ['imported-v1'])
  assert.deepEqual(filtersFromView({ id: releaseB, ...viewShape(filters) }).ships_in, filters.ships_in)
  assert.deepEqual(clearedFilters().ships_in, [])
  assert.deepEqual(filtersFromQuery({ ships_in: `bad,!bad,${releaseA},none` }).ships_in, [releaseA, 'none'])
  assert.deepEqual(effectiveSort(filters), [{ field: 'order', desc: false }])
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
  assert.equal(valueLabel('ships_in', 'none'), 'No release (backlog)')
  const sameNames = facetOptions('ships_in', { [releaseA]: 2, [releaseB]: 3 }, [], new Map(), undefined, { releases: { [releaseA]: 'Same', [releaseB]: 'Same' } })
  assert.deepEqual(sameNames.map(option => option.value), ['none', releaseA, releaseB])
})
