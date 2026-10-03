// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useTicketList } from '../src/lib/useTicketList'
import { filtersFromQuery } from '../src/lib/ticketList'
import { placeKey } from '../src/lib/useLiveList'
import type { ListPage, ListQuery, ListItem } from '../src/lib/api'

const release = '11111111-1111-4111-8111-111111111111'
afterEach(() => vi.restoreAllMocks())

it('retains labels across filter changes, reports facet failure and retries', async () => {
  const filters = ref(filtersFromQuery({ sort: 'order' }))
  let fail = false
  const fetchList = vi.fn(async (query: ListQuery): Promise<ListPage> => {
    if (query.facets?.includes('ships_in')) {
      if (fail) throw new Error('read failed')
      return { items: [], next_cursor: null, facets: { ships_in: { [release]: 2, none: 1 } }, facet_labels: { ships_in: { [release]: 'Release 122' } } }
    }
    return { items: [], next_cursor: null }
  })
  const list = useTicketList(ref('project'), filters, { fetchList })
  await list.load()
  await list.requestFacet('ships_in')
  expect(list.releaseNames.value[release]).toBe('Release 122')
  filters.value = filtersFromQuery({ ships_in: release, sort: 'order' })
  await list.load()
  expect(list.counts('ships_in')).toEqual({ [release]: 2, none: 1 })
  expect(fetchList.mock.calls.at(-1)?.[0].ships_in).toEqual([release])
  fail = true
  await list.load()
  expect(list.facetErrors.ships_in).toContain('could not be loaded')
  expect(list.releaseNames.value[release]).toBe('Release 122')
  fail = false
  await list.requestFacet('ships_in')
  expect(list.facetErrors.ships_in).toBeUndefined()
  expect(fetchList.mock.calls.at(-1)?.[0].ships_in).toEqual([])
  expect(list.counts('ships_in')[release]).toBe(2)
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

it('handles a failed facet while the main list read is still pending', async () => {
  let complete!: (page: ListPage) => void
  let entered!: () => void
  const enteredMain = new Promise<void>(resolve => { entered = resolve })
  const main = new Promise<ListPage>(resolve => { complete = resolve })
  const list = useTicketList(ref('project'), ref(filtersFromQuery({ ships_in: release })), {
    fetchList: async query => {
      if (query.facets?.includes('ships_in')) throw new Error('facet failed first')
      entered()
      return main
    },
  })
  const pending = list.load()
  await enteredMain
  // Cross an event-loop boundary while the row read is held at the barrier.
  // Vitest rejects an unhandled promise rejection at this point.
  await new Promise<void>(resolve => setImmediate(resolve))
  complete({ items: [], next_cursor: null })
  await pending
  expect(list.facetErrors.ships_in).toContain('could not be loaded')
  expect(list.loading.value).toBe(false)
})
