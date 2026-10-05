// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import type { TimeEntry, TimePeriod } from '../src/lib/business'
import * as API from '../src/lib/api'
import * as Avatar from '../src/lib/avatar'
import * as Money from '../src/components/business/money'
import { sourceModule } from './record-source'
const { getPeriodSnapshot } = sourceModule<typeof import('../src/lib/business')>('lib/business.ts', { './api': API, './avatar': Avatar, '../components/business/money': Money })

it('S8-005: a mutation between rows and digest retries the entire snapshot', async () => {
  const period = { id: 'p', revision: 1 } as TimePeriod
  const old = [{ id: 'seen', period_id: 'p' }] as TimeEntry[], fresh = [...old, { id: 'added', period_id: 'p' }] as TimeEntry[]
  let digest = 'old'
  const getPeriod = vi.fn(async () => ({ period, digest }))
  const listEntries = vi.fn(async () => { const rows = digest === 'old' ? old : fresh; digest = 'new'; return rows })
  const result = await getPeriodSnapshot('p', { getPeriod, listEntries })
  expect(result.entries).toEqual(fresh)
  expect(result.digest).toBe('new')
  expect(listEntries).toHaveBeenCalledTimes(2)
})
it('S8-005: repeated mutation stops after three complete reads', async () => {
  let n = 0
  const io = { getPeriod: async () => ({ period: { id: 'p', revision: 1 } as TimePeriod, digest: String(++n) }), listEntries: vi.fn(async () => []) }
  await expect(getPeriodSnapshot('p', io)).rejects.toThrow('Entries are changing')
  expect(io.listEntries).toHaveBeenCalledTimes(3)
})
