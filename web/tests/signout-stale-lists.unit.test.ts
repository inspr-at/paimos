// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1424: a customers or quotes read that is already in flight must not
// refill the store after reset(). The next load sends a new request.
import { afterEach, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, getActivePinia, setActivePinia } from 'pinia'
import type { Customer } from '../src/lib/crm'
import type { QuoteRow } from '../src/lib/quotes/list'

vi.mock('../src/lib/crm', async original => {
  const mod = await original<typeof import('../src/lib/crm')>()
  return { ...mod, listCustomers: vi.fn(), contactDirectory: vi.fn() }
})
vi.mock('../src/lib/quotes/list', async original => {
  const mod = await original<typeof import('../src/lib/quotes/list')>()
  return { ...mod, fetchQuotes: vi.fn() }
})

import { contactDirectory, listCustomers } from '../src/lib/crm'
import { fetchQuotes } from '../src/lib/quotes/list'
import { useCustomers } from '../src/stores/customers'
import { useQuotes } from '../src/stores/quotes'

const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((ok, bad) => { resolve = ok; reject = bad })
  return { promise, resolve, reject }
}
const customer = { id: 'org-1' } as Customer
const quote = { quote_node_id: 'quote-1' } as QuoteRow
const card = { name: 'Ada', email: 'ada@b.cm', role: 'Lead' }

afterEach(() => { const pinia = getActivePinia(); if (pinia) disposePinia(pinia) })

it('AEON-1424: a reset drops an in-flight customers or quotes list and the next load sends a new request', async () => {
  const pinia = createPinia()
  setActivePinia(pinia)
  const customers = useCustomers()
  const quotes = useQuotes()

  const staleCustomers = deferred<Awaited<ReturnType<typeof listCustomers>>>()
  const freshCustomers = deferred<Awaited<ReturnType<typeof listCustomers>>>()
  const staleContacts = new Map([['old', card]])
  const freshContacts = new Map([['new', card]])
  const contactReads = [staleContacts, freshContacts]
  vi.mocked(listCustomers).mockReturnValueOnce(staleCustomers.promise).mockReturnValueOnce(freshCustomers.promise)
  vi.mocked(contactDirectory).mockImplementation(() => Promise.resolve({ items: contactReads.shift() ?? new Map(), partial: false }))
  const firstCustomers = customers.load()
  customers.load()
  expect(listCustomers).toHaveBeenCalledTimes(1)
  customers.reset()
  expect(customers.items).toBeNull()
  expect(customers.directory.size).toBe(0)
  const secondCustomers = customers.load()
  expect(listCustomers).toHaveBeenCalledTimes(2)
  expect(contactDirectory).toHaveBeenCalledTimes(2)
  staleCustomers.resolve({ items: [customer], partial: false })
  await firstCustomers
  expect(customers.items).toBeNull()
  expect(customers.error).toBe('')
  expect(customers.directory.size).toBe(0)
  expect(customers.loading).toBe(true)
  const freshRows = [customer]
  freshCustomers.resolve({ items: freshRows, partial: false })
  await secondCustomers
  expect(customers.items).toEqual(freshRows)
  expect(customers.directory).toBe(freshContacts)
  expect(customers.loading).toBe(false)
  expect(customers.error).toBe('')

  const staleQuotes = deferred<Awaited<ReturnType<typeof fetchQuotes>>>()
  const freshQuotes = deferred<Awaited<ReturnType<typeof fetchQuotes>>>()
  vi.mocked(fetchQuotes).mockReturnValueOnce(staleQuotes.promise).mockReturnValueOnce(freshQuotes.promise)
  const firstQuotes = quotes.load()
  quotes.load()
  expect(fetchQuotes).toHaveBeenCalledTimes(1)
  quotes.reset()
  expect(quotes.items).toBeNull()
  expect(quotes.error).toBe('')
  expect(quotes.status).toBe(0)
  const secondQuotes = quotes.load()
  expect(fetchQuotes).toHaveBeenCalledTimes(2)
  staleQuotes.resolve({ items: [quote], partial: false })
  await firstQuotes
  expect(quotes.items).toBeNull()
  expect(quotes.error).toBe('')
  expect(quotes.status).toBe(0)
  expect(quotes.loading).toBe(true)
  const freshQuoteRows = [quote]
  freshQuotes.resolve({ items: freshQuoteRows, partial: false })
  await secondQuotes
  expect(quotes.items).toEqual(freshQuoteRows)
  expect(quotes.loading).toBe(false)
  expect(quotes.error).toBe('')
  expect(quotes.status).toBe(0)

  customers.reset()
  const offlineCustomers = deferred<Awaited<ReturnType<typeof listCustomers>>>()
  vi.mocked(listCustomers).mockReturnValueOnce(offlineCustomers.promise)
  const offlineCustomerFlight = customers.load()
  offlineCustomers.reject(new Error('offline'))
  await offlineCustomerFlight
  expect(customers.error).toBe('offline')
  expect(customers.items).toBeNull()
  expect(customers.loading).toBe(false)

  quotes.reset()
  const offlineQuotes = deferred<Awaited<ReturnType<typeof fetchQuotes>>>()
  vi.mocked(fetchQuotes).mockReturnValueOnce(offlineQuotes.promise)
  const offlineQuoteFlight = quotes.load()
  offlineQuotes.reject(Object.assign(new Error('offline'), { status: 503 }))
  await offlineQuoteFlight
  expect(quotes.error).toBe('offline')
  expect(quotes.status).toBe(503)
  expect(quotes.items).toBeNull()
  expect(quotes.loading).toBe(false)

  customers.reset()
  const failedCustomers = deferred<Awaited<ReturnType<typeof listCustomers>>>()
  vi.mocked(listCustomers).mockReturnValueOnce(failedCustomers.promise)
  const customerFlight = customers.load()
  customers.reset()
  failedCustomers.reject(new Error('signed out'))
  await customerFlight
  expect(customers.items).toBeNull()
  expect(customers.error).toBe('')
  expect(customers.loading).toBe(false)

  quotes.reset()
  const failedQuotes = deferred<Awaited<ReturnType<typeof fetchQuotes>>>()
  vi.mocked(fetchQuotes).mockReturnValueOnce(failedQuotes.promise)
  const quoteFlight = quotes.load()
  quotes.reset()
  failedQuotes.reject(Object.assign(new Error('signed out'), { status: 503 }))
  await quoteFlight
  expect(quotes.items).toBeNull()
  expect(quotes.error).toBe('')
  expect(quotes.status).toBe(0)
  expect(quotes.loading).toBe(false)
})
