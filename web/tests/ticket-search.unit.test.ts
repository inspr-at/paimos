// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
const sources = vi.hoisted(() => ({ listNodes: vi.fn(), searchNodes: vi.fn(), kinds: vi.fn() }))
vi.mock('../src/lib/api', () => ({ listNodes: sources.listNodes, searchNodes: sources.searchNodes }))
vi.mock('../src/lib/useTicket', () => ({ kinds: sources.kinds }))
import { searchWork, workKindMap } from '../src/lib/ticketSearch'
import { ticketResults } from '../src/lib/palette'

beforeEach(() => {
  vi.resetAllMocks()
  sources.kinds.mockResolvedValue([{ id: 'canonical', slug: 'work' }, { id: 'project', slug: 'project' }])
})
it.each(['PHAROS-29', 'connector'])('shared palette and relation search includes canonical work for %s', async query => {
  const row = { id: 'leaf', key: 'PHAROS-29', title: 'Connector', kind_slug: 'work', kind_id: 'canonical', state: 'open' }
  sources.listNodes.mockResolvedValue({ items: [row] })
  sources.searchNodes.mockResolvedValue({ items: [{ node: { ...row, id: 'semantic', key: 'PHAROS-30' } }] })
  const signal = new AbortController().signal
  const map = await workKindMap(), found = await searchWork(query, { within: 'project', signal })
  expect(map.get('canonical')).toBe('work'); expect(map.has('project')).toBe(false)
  expect(sources.listNodes.mock.calls[0]![0]).toMatchObject({ kind: expect.arrayContaining(['work']), within: 'project', limit: 8 })
  expect(sources.listNodes.mock.calls[0]![1].signal).toBe(signal)
  expect(ticketResults(query, found.listed, found.hits, map, () => 'PHAROS', 'PHAROS').map(row => row.id)).toEqual(query === 'connector' ? ['leaf', 'semantic'] : ['leaf'])
  expect(sources.searchNodes).toHaveBeenCalledTimes(query === 'connector' ? 1 : 0)
})
