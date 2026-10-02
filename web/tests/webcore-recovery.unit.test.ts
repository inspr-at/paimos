// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { APIError, type ListPage } from '../src/lib/api'
import { getAudit } from '../src/lib/access'
import { QuotePresence } from '../src/lib/quotePresence'
import { QuoteSession } from '../src/lib/quoteSession'
import { useTicketList } from '../src/lib/useTicketList'
import { filtersFromQuery } from '../src/lib/ticketList'
import { parseAmountInput, parsePercentInput, parseQuantityInput } from '../src/components/business/money'

const mocks = vi.hoisted(() => ({ api: vi.fn(), getDraft: vi.fn() }))
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: mocks.api }))
vi.mock('../src/lib/quotes/api', () => ({ getDraft: mocks.getDraft, saveDraft: vi.fn() }))
const snapshot = { sessions: [], draft_revision: 1, quote_revision: 1, state: 'draft' }
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
const turn = () => new Promise<void>(resolve => setImmediate(resolve))
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
class Source extends EventTarget { close = vi.fn() }
class Channel {
  static instances: Channel[] = []
  onmessage: ((event: MessageEvent) => void) | null = null
  constructor() { Channel.instances.push(this) }
  postMessage(message: { kind: string; session: string; nonce: string }) {
    if (message.kind === 'probe') this.onmessage?.({ data: { kind: 'present', session: message.session, to: message.nonce, instance: 'other-tab' } } as MessageEvent)
  }
  close() {}
}
beforeEach(() => {
  mocks.api.mockReset(); mocks.getDraft.mockReset(); Channel.instances = []
  vi.stubGlobal('window', { setTimeout: (...args: Parameters<typeof setTimeout>) => setTimeout(...args), clearTimeout, setInterval: (...args: Parameters<typeof setInterval>) => setInterval(...args), clearInterval, addEventListener() {}, removeEventListener() {} })
  vi.stubGlobal('document', Object.assign(new EventTarget(), { hidden: false }))
  vi.stubGlobal('EventSource', Source)
  vi.stubGlobal('BroadcastChannel', Channel)
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

describe('S8-010: human numeric input', () => {
  it('normalizes leading zeros and decimal separators without throwing', () => {
    for (const input of ['01', '00.5', '01,5']) {
      expect(parseQuantityInput(input)).toBe(input === '01' ? '1' : input === '00.5' ? '0.5' : '1.5')
      expect(parseAmountInput(input)).toBe(input === '01' ? '1.0000' : input === '00.5' ? '0.5000' : '1.5000')
      expect(parsePercentInput(input)).toBe(input === '01' ? '0.01000' : input === '00.5' ? '0.00500' : '0.01500')
    }
    expect(parseQuantityInput('000')).toBeNull()
    expect(parseAmountInput('000')).toBe('0.0000')
    expect(parsePercentInput('000 %')).toBe('0.00000')
    expect(parseQuantityInput('99999999999999.9999')).toBe('99999999999999.9999')
    expect(parsePercentInput('100')).toBe('1.00000')
    expect(parsePercentInput('100.001')).toBeNull()
    for (const parse of [parseQuantityInput, parseAmountInput, parsePercentInput]) {
      for (const input of ['', 'NaN', '-1', '1e3', '1.00001', '1.2.3', '9'.repeat(1000), '\u0000']) expect(parse(input)).toBeNull()
    }
  })
})

describe('S9-003: presence recovery', () => {
  it('retries a failed expired-lease rejoin on a later heartbeat, once at a time', async () => {
    vi.useFakeTimers()
    const recovered = deferred<Response>()
    mocks.api.mockResolvedValueOnce(response({ session_id: 'old', snapshot }))
      .mockResolvedValueOnce(response({}, 404)).mockRejectedValueOnce(new TypeError('offline'))
      .mockImplementationOnce(() => recovered.promise).mockResolvedValue(response(snapshot))
    const presence = new QuotePresence('quote', 'person', vi.fn(), vi.fn())
    try {
      await presence.start(1)
      presence.setSelection(null, 'editing', 2)
      await vi.advanceTimersByTimeAsync(220)
      expect(presence.sessionId).toBeNull()
      expect(mocks.api).toHaveBeenCalledTimes(3)
      await vi.advanceTimersByTimeAsync(15000)
      expect(mocks.api).toHaveBeenCalledTimes(4)
      await vi.advanceTimersByTimeAsync(15000)
      expect(mocks.api).toHaveBeenCalledTimes(4)
      recovered.resolve(response({ session_id: 'new', snapshot }))
      await vi.advanceTimersByTimeAsync(0)
      expect(presence.sessionId).toBe('new')
      await vi.advanceTimersByTimeAsync(15000)
      expect(mocks.api.mock.calls.at(-1)?.[0]).toContain('/presence/new')
    } finally { presence.stop() }
  })
  it.each([401, 403])('stops after a rejoin is refused with %s', async status => {
    vi.useFakeTimers()
    mocks.api.mockResolvedValueOnce(response({ session_id: 'old', snapshot }))
      .mockResolvedValueOnce(response({}, 404)).mockResolvedValueOnce(response({}, status))
    const presence = new QuotePresence('quote', 'person', vi.fn(), vi.fn())
    await presence.start(1); presence.setSelection(null, 'editing', 1)
    await vi.advanceTimersByTimeAsync(220)
    await vi.advanceTimersByTimeAsync(120000)
    expect(mocks.api).toHaveBeenCalledTimes(3)
    expect(presence.sessionId).toBeNull()
    presence.stop()
  })
  it('discards a late rejoin after disposal and does not retry it', async () => {
    vi.useFakeTimers()
    const pending = deferred<Response>(), onSnapshot = vi.fn()
    mocks.api.mockResolvedValueOnce(response({ session_id: 'old', snapshot }))
      .mockResolvedValueOnce(response({}, 404)).mockImplementationOnce(() => pending.promise)
    const presence = new QuotePresence('quote', 'person', onSnapshot, vi.fn())
    await presence.start(1); presence.setSelection(null, 'editing', 1)
    await vi.advanceTimersByTimeAsync(220)
    presence.stop()
    pending.resolve(response({ session_id: 'late', snapshot }))
    await vi.advanceTimersByTimeAsync(120000)
    expect(mocks.api).toHaveBeenCalledTimes(3)
    expect(onSnapshot).toHaveBeenCalledTimes(1)
    expect(presence.sessionId).toBeNull()
  })
})

describe('S9-006: denied tab storage', () => {
  it.each(['get', 'set', 'collision'])('opens the editor when %s storage access throws', async failure => {
    vi.useFakeTimers()
    let writes = 0
    vi.stubGlobal('sessionStorage', {
      getItem() { if (failure === 'get') throw new DOMException('denied', 'SecurityError'); return 'copied-session' },
      setItem() { if (failure === 'set' || failure === 'collision' && ++writes > 1) throw new DOMException('full', 'QuotaExceededError') },
    })
    mocks.getDraft.mockResolvedValue({ draft_revision: 1, document: { title: 'Working quote', sections: [], positions: [] } })
    const session = new QuoteSession({ quoteId: 'quote', tenantId: 'tenant', principalId: 'person' })
    try {
      const initial = session.clientSessionId
      const opening = session.open().then(value => ({ value }), error => ({ error }))
      await vi.advanceTimersByTimeAsync(80)
      expect(await opening).not.toHaveProperty('error')
      expect(session.view.local).toBe('clean')
      expect(session.view.working?.title).toBe('Working quote')
      expect(session.clientSessionId).not.toBe(initial)
      expect(session.view.durableRecovery).toBe(false)
    } finally { session.dispose() }
  })
})

it('S9-007: includes a recent revocation beyond 2000 changes in a bounded newest window', async () => {
  const all = Array.from({ length: 2051 }, (_, i) => ({ id: i + 1, type: i === 2050 ? 'agent_key.revoked' : 'binding.set' }))
  mocks.api.mockImplementation(async (path: string) => {
    const q = new URL(path, 'http://test').searchParams
    if (q.get('order') === 'desc') {
      const before = Number(q.get('before') ?? Infinity)
      const rest = all.filter(e => e.id < before).reverse(), items = rest.slice(0, 50)
      return response({ items, next_after: null, next_before: rest.length > 50 ? items.at(-1)!.id : null })
    }
    const rest = all.filter(e => e.id > Number(q.get('after') ?? 0)), items = rest.slice(0, 50)
    return response({ items, next_after: rest.length > 50 ? items.at(-1)!.id : null })
  })
  const log = await getAudit()
  expect(log.items[0]).toMatchObject({ id: 2051, type: 'agent_key.revoked' })
  expect(log.items).toHaveLength(2000)
  expect(log.items.at(-1)?.id).toBe(52)
  expect(log.complete).toBe(false)
  expect(mocks.api).toHaveBeenCalledTimes(40)
})

it.each([false, true])('S9-009: owns failed optional facets while a primary read is held (superseded=%s)', async superseded => {
  const primary = deferred<ListPage>(), facet = deferred<ListPage>()
  const failure = new APIError(503, 'facet unavailable')
  const unhandled: unknown[] = []
  const listener = (reason: unknown) => unhandled.push(reason)
  process.on('unhandledRejection', listener)
  const fetchList = vi.fn().mockImplementationOnce(() => facet.promise).mockImplementationOnce(() => primary.promise)
    .mockResolvedValue({ items: [], next_cursor: null, facets: { state: { done: 7 } } })
  const list = useTicketList(ref('project'), ref(filtersFromQuery({ status: 'new' })), { fetchList })
  const load = list.load()
  try {
    if (superseded) await list.load()
    facet.reject(failure)
    await turn()
    expect(unhandled).toEqual([])
  } finally {
    primary.resolve({ items: [], next_cursor: null, facets: { state: { new: 1 } } })
    await load
    process.off('unhandledRejection', listener)
  }
  if (superseded) expect(list.facets.value.state).toEqual({ done: 7 })
})
