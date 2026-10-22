// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../src/lib/api'
import { listNodePages, type ListItem } from '../src/lib/api'
import { listPeriods, PERIOD_HISTORY_START, type TimePeriod } from '../src/lib/business'
import * as Pinia from 'pinia'
import { reactive } from 'vue'
import { deferred, flush, sourceModule } from './record-source'

vi.mock('../src/lib/api', async importOriginal => ({ ...await importOriginal<typeof import('../src/lib/api')>(), api: vi.fn() }))
afterEach(() => { vi.resetAllMocks(); vi.unstubAllGlobals(); const pinia = Pinia.getActivePinia(); if (pinia) Pinia.disposePinia(pinia) })

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

function costStore(nodes: ListItem[]) {
  Pinia.setActivePinia(Pinia.createPinia())
  const session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person' } } })
  const list = vi.fn(async () => ({ items: nodes, partial: false, next_cursor: null }))
  const rates = vi.fn<(id: string) => Promise<import('../src/lib/business').CostRate[]>>()
  const { useBusiness } = sourceModule<typeof import('../src/stores/business')>('stores/business.ts', {
    pinia: Pinia, '../lib/api': { listNodes: list, listNodePages: list },
    '../lib/business': { listRates: rates }, '../components/business/catalog': {},
    '../lib/authz': { can: () => true }, '../components/business/areas': { businessSections: [] },
    './session': { useSession: () => session },
  })
  return { store: useBusiness(), list, rates, session }
}
const unitNode = (i: number) => ({ id: `cu-${i}`, title: `Unit ${i}`, state: 'open' }) as ListItem
const hourlyRate = (id: string): import('../src/lib/business').CostRate => ({ id: `rate-${id}`, cost_unit_node_id: id, unit: 'hour', currency: 'EUR', effective_from: '2020-01-01', effective_until: null, bill_amount: '90', internal_amount: '60', created_by_principal_id: 'person', created_at: '2020-01-01T00:00:00Z' })

it('AEON-1417: 200 units use at most six rate reads and a rejected unit remains visible until retry succeeds', async () => {
  const { store, list, rates } = costStore(Array.from({ length: 200 }, (_, i) => unitNode(i)))
  let active = 0, peak = 0
  const pending: { id: string; gate: ReturnType<typeof deferred<import('../src/lib/business').CostRate[]>> }[] = []
  rates.mockImplementation(id => {
    active++; peak = Math.max(peak, active)
    const gate = deferred<import('../src/lib/business').CostRate[]>()
    pending.push({ id, gate })
    return gate.promise.finally(() => { active-- })
  })
  const loading = store.loadCostUnits()
  await flush()
  // All six reads are held by explicit barriers; a seventh cannot start yet.
  expect(rates).toHaveBeenCalledTimes(6)
  expect(active).toBe(6)
  expect(store.costUnitsLoaded).toBe(false)
  expect(store.costUnitsLoading).toBe(true)
  expect(store.costUnitsPartial).toBe(false)
  expect(store.costUnitsError).toBe('')
  expect(store.costUnits.every(unit => unit.ratesPending && !unit.ratesError)).toBe(true)
  expect(store.usable('hour', 'EUR', '2026-10-21')).toHaveLength(200)
  let rejected = false
  while (pending.length) {
    const wave = pending.splice(0)
    for (const { id, gate } of wave) {
      if (id === 'cu-0' && !rejected) { rejected = true; gate.reject(new Error('rate read failed')) }
      else gate.resolve([hourlyRate(id)])
    }
    await flush()
  }
  await loading
  expect(rates).toHaveBeenCalledTimes(200)
  expect(peak).toBe(6)
  expect(store.costUnitsPartial).toBe(true)
  expect(store.costUnitsError).toContain('Some rates could not be loaded')
  expect(store.costUnit('cu-0')?.ratesError).toBe('rate read failed')
  expect(store.usable('hour', 'EUR', '2026-10-21').map(unit => unit.node.id)).toContain('cu-0')
  const retry = store.loadCostUnits()
  await flush()
  expect(list).toHaveBeenCalledTimes(1)
  expect(rates).toHaveBeenCalledTimes(201)
  expect(pending.map(item => item.id)).toEqual(['cu-0'])
  expect(store.costUnit('cu-0')?.ratesPending).toBe(true)
  expect(store.costUnit('cu-0')?.ratesError).toBeUndefined()
  expect(store.costUnitsError).toBe('')
  pending.shift()!.gate.resolve([hourlyRate('cu-0')])
  await retry
  expect(store.costUnitsPartial).toBe(false)
  expect(store.costUnitsError).toBe('')
  expect(store.costUnit('cu-0')?.ratesError).toBeUndefined()
  expect(store.rateOn('cu-0', 'hour', 'EUR', '2026-10-21')?.bill_amount).toBe('90')
  const refresh = deferred<import('../src/lib/business').CostRate[]>()
  rates.mockImplementation(id => id === 'cu-0' ? refresh.promise : Promise.resolve([hourlyRate(id)]))
  const refreshing = store.loadCostUnits(true)
  await flush()
  expect(store.costUnitsLoaded).toBe(true)
  expect(store.costUnit('cu-0')?.ratesPending).toBe(true)
  expect(store.costUnit('cu-0')?.ratesError).toBeUndefined()
  expect(store.costUnitsError).toBe('')
  expect(store.rateOn('cu-0', 'hour', 'EUR', '2026-10-21')?.bill_amount).toBe('90')
  refresh.reject(new Error('refresh failed'))
  await refreshing
  expect(store.costUnit('cu-0')?.rates).toEqual([])
  expect(store.costUnit('cu-0')?.ratesError).toBe('refresh failed')
  expect(store.usable('hour', 'EUR', '2026-10-21').map(unit => unit.node.id)).toContain('cu-0')
  expect(store.rateOn('cu-1', 'hour', 'EUR', '2026-10-21')?.bill_amount).toBe('90')
})

it('AEON-1417: changing the rate-cache owner retires pending reads and stops queued requests', async () => {
  const { store, rates, session } = costStore(Array.from({ length: 200 }, (_, i) => unitNode(i)))
  const held = deferred<import('../src/lib/business').CostRate[]>()
  rates.mockReturnValue(held.promise)
  const loading = store.loadCostUnits()
  await flush()
  expect(rates).toHaveBeenCalledTimes(6)
  session.identity.principal.id = 'other-person'
  expect(store.costUnits).toEqual([])
  held.resolve([hourlyRate('cu-0')])
  await loading
  expect(rates).toHaveBeenCalledTimes(6)
  expect(store.costUnits).toEqual([])
  expect(store.costUnitsLoaded).toBe(false)
  expect(store.costUnitsLoading).toBe(false)
})

it('AEON-1417: a late rate response or failure cannot replace a newer confirmed rate update', async () => {
  const { store, rates } = costStore([unitNode(0)])
  for (const fail of [false, true]) {
    const pending = deferred<import('../src/lib/business').CostRate[]>()
    rates.mockReturnValue(pending.promise)
    const loading = store.loadCostUnits(true)
    await flush()
    const confirmed = { ...hourlyRate('cu-0'), bill_amount: fail ? '125' : '120' }
    store.setRates('cu-0', [confirmed])
    if (fail) pending.reject(new Error('older read failed'))
    else pending.resolve([hourlyRate('cu-0')])
    await loading
    expect(store.costUnit('cu-0')?.rates).toEqual([confirmed])
    expect(store.costUnit('cu-0')?.ratesError).toBeUndefined()
    expect(store.costUnitsPartial).toBe(false)
  }
})

it('AEON-1417: a cursor at every directory cap returns typed partial rows and the final complete page stays complete', async () => {
  const CRM = sourceModule<typeof import('../src/lib/crm')>('lib/crm.ts', {
    './api.ts': { api, listNodePages }, './toast.ts': { captureToastOwner: () => () => true },
  })
  const Quotes = sourceModule<typeof import('../src/lib/quotes/list')>('lib/quotes/list.ts', {
    '../api': { api }, '../crm': {},
  })
  for (const continuation of [true, false]) {
    let customers = 0, quotes = 0
    vi.mocked(api).mockImplementation(async path => {
      if (path.startsWith('/crm/organisations?')) {
        customers++
        return new Response(JSON.stringify({ items: [{ id: `customer-${customers}` }], next_offset: customers < 50 || continuation ? customers * 100 : null }))
      }
      quotes++
      return new Response(JSON.stringify([{ quote_node_id: `quote-${quotes}` }]), { headers: quotes < 50 || continuation ? { 'X-Next-Cursor': `quote-${quotes}` } : {} })
    })
    const customerList = await CRM.listCustomers(), quoteList = await Quotes.fetchQuotes()
    expect(customerList.partial).toBe(continuation); expect(customerList.items).toHaveLength(50)
    expect(quoteList.partial).toBe(continuation); expect(quoteList.items).toHaveLength(50)
    expect(customers).toBe(50); expect(quotes).toBe(50)
    for (const kind of ['cost_unit', 'contact', 'project']) {
      let pages = 0
      vi.stubGlobal('fetch', vi.fn(async () => {
        pages++
        return new Response(JSON.stringify({ items: [{ ...unitNode(pages), fields: {} }], next_cursor: pages < 20 || continuation ? `cursor-${pages}` : null }))
      }))
      const result = kind === 'contact' ? await CRM.contactDirectory() : await listNodePages({ kind: [kind], limit: kind === 'cost_unit' ? 200 : 500 })
      expect(result.partial, kind).toBe(continuation)
      expect(Array.isArray(result.items) ? result.items.length : result.items.size).toBe(20)
      expect(pages).toBe(20)
    }
  }
})
