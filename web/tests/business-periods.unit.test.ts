// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/lib/api'
import { listPeriods, PERIOD_HISTORY_START, type TimePeriod } from '../src/lib/business'

vi.mock('../src/lib/api', async importOriginal => ({ ...await importOriginal<typeof import('../src/lib/api')>(), api: vi.fn() }))
afterEach(() => vi.resetAllMocks())

const old: TimePeriod = { id: 'old-period', principal_id: 'person', starts_at: '2024-09-15T22:00:00.000Z', ends_at: '2024-09-22T22:00:00.000Z', state: 'open', revision: 3, approval: null }
const reply = (rows: TimePeriod[], cursor = '') => new Response(JSON.stringify(rows), { headers: cursor ? { 'X-Next-Cursor': cursor } : {} })

describe('period history bounds and honest paging', () => {
  it('keeps the selected historical week and principal on every keyset page', async () => {
    const paths: string[] = []
    vi.mocked(api).mockImplementation(async path => {
      paths.push(path)
      return paths.length === 1 ? reply([old], old.id) : reply([{ ...old, id: 'second-period' }])
    })
    const result = await listPeriods(old.principal_id, { since: old.starts_at, until: old.ends_at })
    expect(result.map(p => p.id)).toEqual([old.id, 'second-period'])
    for (const path of paths) {
      const query = new URL(path, 'http://localhost').searchParams
      expect(query.get('principal_id')).toBe(old.principal_id)
      expect(query.get('since')).toBe(old.starts_at)
      expect(query.get('until')).toBe(old.ends_at)
      expect(query.get('limit')).toBe('100')
    }
    expect(new URL(paths[1], 'http://localhost').searchParams.get('after_id')).toBe(old.id)
  })

  it('includes the oldest supported open period with the explicit approval bound', async () => {
    const oldest = { ...old, starts_at: '0001-01-01T00:00:00Z', ends_at: '0001-01-08T00:00:00Z' }
    vi.mocked(api).mockImplementation(async path => {
      const since = new URL(path, 'http://localhost').searchParams.get('since') ?? '2025-09-24T00:00:00Z'
      return reply(Date.parse(oldest.ends_at) >= Date.parse(since) ? [oldest] : [])
    })
    expect(await listPeriods(undefined, { since: PERIOD_HISTORY_START })).toEqual([oldest])
  })

  it('accepts exactly 2,000 rows but rejects a continuation rather than returning a partial list', async () => {
    let pages = 0
    const pageRows = () => Array.from({ length: 100 }, (_, i) => ({ ...old, id: `period-${pages}-${i}` }))
    vi.mocked(api).mockImplementation(async () => { pages++; return reply(pageRows(), pages < 20 ? `cursor-${pages}` : '') })
    const complete = await listPeriods(undefined, { since: PERIOD_HISTORY_START })
    expect(complete).toHaveLength(2000)
    expect(new Set(complete.map(p => p.id)).size).toBe(2000)
    expect(api).toHaveBeenCalledTimes(20)
    vi.mocked(api).mockClear()
    pages = 0
    vi.mocked(api).mockImplementation(async () => { pages++; return reply(pageRows(), `cursor-${pages}`) })
    await expect(listPeriods(undefined, { since: PERIOD_HISTORY_START })).rejects.toThrow('More than 2,000 history records')
    expect(api).toHaveBeenCalledTimes(20)
  })
})
