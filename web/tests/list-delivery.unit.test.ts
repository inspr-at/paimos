// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useTicketList } from '../src/lib/useTicketList'
import { filtersFromQuery } from '../src/lib/ticketList'
import { placeKey } from '../src/lib/useLiveList'
import { rowStore } from '../src/lib/rowStore'
import type { ListPage, ListQuery, ListItem } from '../src/lib/api'

const release = '11111111-1111-4111-8111-111111111111'
afterEach(() => { vi.restoreAllMocks(); rowStore.clear() })

it('release scope reads do not fan out to a selectable membership facet', async () => {
  const fetchList = vi.fn(async (_query: ListQuery): Promise<ListPage> => ({ items: [], next_cursor: null }))
  const list = useTicketList(ref('project'), ref(filtersFromQuery({ ships_in: release, sort: 'order' })), { fetchList })
  await list.load()
  expect(fetchList).toHaveBeenCalledTimes(1)
  expect(fetchList.mock.calls[0]?.[0].ships_in).toEqual([release])
})

it('discards stale facet labels and counts after navigating projects', async () => {
  let finish!: (value: ListPage) => void
  const old = new Promise<ListPage>(resolve => { finish = resolve })
  const project = ref<string | null>('a')
  const list = useTicketList(project, ref(filtersFromQuery({})), { fetchList: async query => query.within === 'a' && query.facets?.includes('ships_in') ? old : { items: [], next_cursor: null } })
  await list.load()
  const pending = list.requestFacet('ships_in')
  project.value = 'b'
  await list.load()
  finish({ items: [], next_cursor: null, facets: { ships_in: { [release]: 2 } }, facet_labels: { ships_in: { [release]: 'Private A title' } } })
  await pending
  expect(list.releaseNames.value).toEqual({})
  expect(list.counts('ships_in')).toEqual({})
})

it('delivery placement changes are structural for the held live-list layout', () => {
  const row = { id: 'ticket', delivery_order: { release_id: release, release_rank: 'B', rank: 'V', expedite: false } } as ListItem
  const filters = filtersFromQuery({ sort: 'order' })
  expect(placeKey(row, filters)).not.toBe(placeKey({ ...row, delivery_order: { ...row.delivery_order!, rank: 'a' } }, filters))
  expect(placeKey(row, filters)).not.toBe(placeKey({ ...row, delivery_order: { ...row.delivery_order!, release_rank: 'D' } }, filters))
})

it('ambiguous saved scopes require visible repair without a read', async () => {
  const fetchList = vi.fn(async (_query: ListQuery): Promise<ListPage> => ({ items: [], next_cursor: null }))
  const list = useTicketList(ref('project'), ref(filtersFromQuery({ ships_in: '!none' })), { fetchList })
  await list.load()
  expect(fetchList).not.toHaveBeenCalled()
  expect(list.error.value).toBe('Choose a release scope')
})

for (const change of ['scope', 'person', 'project'] as const) it(`discards late reads after ${change} changes`, async () => {
  let complete!: (page: ListPage) => void
  const held = new Promise<ListPage>(resolve => { complete = resolve })
  const project = ref('project'), person = ref('person-a'), filters = ref(filtersFromQuery({ ships_in: release }))
  const list = useTicketList(project, filters, { identity: () => person.value, fetchList: () => held })
  const pending = list.load()
  if (change === 'scope') filters.value = filtersFromQuery({ ships_in: 'none' })
  if (change === 'person') person.value = 'person-b'
  if (change === 'project') project.value = 'other'
  const obsolete = { id: 'old-work', key: 'OLD-1', title: 'Previous scope', state: 'open', updated_at: '2026-10-03T12:00:00Z', fields: {} } as ListItem
  complete({ items: [obsolete], next_cursor: 'old-cursor', facets: { state: { done: 99 } } })
  await pending
  expect(list.cursor.value).toBeNull()
  expect(list.facets.value).toEqual({})
  expect(list.reads.value).toBeNull()
  expect(rowStore.row(obsolete.id)).toBeUndefined()
})

it('own receipts update scope membership and Undo immediately, while a late read cannot restore the old row', async () => {
  const row = { id: 'owned', key: 'OWN-1', title: 'Owned work', kind_slug: 'ticket', state: 'open', updated_at: '2026-10-03T12:00:00Z', fields: {}, delivery_order: { release_id: release, release_rank: 'V', rank: 'V', expedite: false } } as ListItem
  let finish!: (page: ListPage) => void
  const fetchList = vi.fn().mockResolvedValueOnce({ items: [row], next_cursor: null, facets: { kind: { ticket: 1 }, state: { open: 1 } } }).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const list = useTicketList(ref('project'), ref(filtersFromQuery({ ships_in: release })), { fetchList })
  await list.load(); const old = list.load()
  list.committedDelivery({ items: [{ item_id: row.id, project_id: 'project', rank: 'W', revision: 2, expedite: false, due_on: null }], undo_event_id: 42 })
  expect(list.rows.value).toEqual([]); expect(list.facets.value.kind.ticket).toBe(0)
  finish({ items: [row], next_cursor: 'old' }); await old
  expect(list.rows.value).toEqual([]); expect(list.cursor.value).toBeNull()
  list.committedDelivery({ items: [{ item_id: row.id, project_id: 'project', release_id: release, rank: 'V', revision: 3, expedite: false, due_on: null }], release_ranks: { [release]: 'V' }, undo_event_id: null })
  expect(list.rows.value.map(it => it.id)).toEqual([row.id]); expect(list.facets.value.kind.ticket).toBe(1)
  expect(list.rows.value[0]?.delivery_order?.release_id).toBe(release)
})
