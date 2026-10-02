// SPDX-License-Identifier: AGPL-3.0-only
import { listNodes, type ListItem, type ListPage, type ListQuery } from './api.ts'
import { compareRows, rowTags } from './ticketList.ts'
import { parseSort } from './work.ts'
import type { QueueSnapshot } from './workQueue.ts'

/** Queued is membership, never a node status. Read the run projection's IDs in
 * bounded node requests, so a queued ticket outside the loaded page still shows.
 * No ordering or membership is kept here: each read uses the server snapshot. */
export async function queueFilteredNodes(query: ListQuery, snapshot: QueueSnapshot, read = listNodes): Promise<ListPage> {
  const states = query.state ?? []
  const wanted = states.includes('queued'), excluded = states.includes('!queued')
  if (!wanted && !excluded) return read(query)
  const members = new Set(snapshot.items.map(item => item.ticket_id))
  const rows = new Map<string, ListItem>()
  const realStates = states.filter(state => state !== 'queued' && state !== '!queued')
  const actual = { ...query, state: realStates, cursor: undefined, limit: 200, facets: undefined }
  async function all(params: ListQuery) {
    let cursor: string | undefined
    do {
      const page = await read({ ...params, cursor })
      for (const row of page.items) if (!excluded || !members.has(row.id)) rows.set(row.id, row)
      cursor = page.next_cursor ?? undefined
    } while (cursor)
  }
  if (wanted && !excluded) {
    const ids = [...members].filter(id => !query.ids || query.ids.includes(id))
    // Real positive states are alternatives to Queued; real exclusions still apply.
    for (let at = 0; at < ids.length; at += 200) await all({ ...actual, ids: ids.slice(at, at + 200), state: realStates.filter(state => state.startsWith('!')) })
  }
  if (!wanted || realStates.some(state => !state.startsWith('!'))) await all(actual)
  let items = [...rows.values()]
  if (wanted && realStates.every(state => state.startsWith('!')) && query.sort === '-updated_at') {
    const positions = new Map(snapshot.items.map((item, i) => [item.ticket_id, i]))
    items.sort((a, b) => (positions.get(a.id) ?? 0) - (positions.get(b.id) ?? 0))
  } else items.sort(compareRows(parseSort(query.sort ?? '')))
  const facets: NonNullable<ListPage['facets']> = {}
  for (const facet of query.facets ?? []) {
    const counts: Record<string, number> = {}
    for (const row of items) {
      const values = facet === 'kind' ? [row.kind_slug] : facet === 'state' ? [row.state] : facet === 'assignee' ? [row.assignee?.id ?? 'none'] : facet === 'priority' ? [row.priority ?? 'none'] : facet === 'tag' ? rowTags(row).map(tag => tag.name) : [String(row.fields[facet] ?? 'none')]
      for (const value of new Set(values.length ? values : ['none'])) counts[value] = (counts[value] ?? 0) + 1
    }
    facets[facet] = counts
  }
  const offset = /^queue:\d+$/.test(query.cursor ?? '') ? Number(query.cursor!.slice(6)) : 0
  const limit = query.limit ?? 200
  return { items: items.slice(offset, offset + limit), next_cursor: offset + limit < items.length ? `queue:${offset + limit}` : null, ...(query.facets ? { facets } : {}) }
}
