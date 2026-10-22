// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api, RequestFailure, retryable, retryDelay, sessionEnded } from '../src/lib/api'
import { initialRefreshStatus, refreshStatus, usePolledData, usePoller } from '../src/lib/usePolledData'
import { createSharedRequests, REFRESH_BATCH_MS } from '../src/lib/sharedRequests'

const stops: (() => void)[] = []
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(100_000) })
afterEach(() => { for (const stop of stops.splice(0)) stop(); vi.unstubAllGlobals(); vi.restoreAllMocks(); vi.useRealTimers(); sessionEnded.blocked = false })

it('retries only idempotent GET transport failures, with bounded exponential jitter', async () => {
  expect(retryable('GET', new RequestFailure('timeout'))).toBe(true)
  expect(retryable('GET', new RequestFailure('network'))).toBe(true)
  expect(retryable('POST', new RequestFailure('network'))).toBe(false)
  expect(retryable('GET', new Error('server rejected the request'))).toBe(false)
  expect(retryDelay(0, () => 0)).toBe(187.5)
  expect(retryDelay(1, () => 1)).toBe(625)
  const fetch = vi.fn().mockRejectedValueOnce(new TypeError('Failed to fetch')).mockResolvedValue(new Response('{}'))
  vi.stubGlobal('fetch', fetch)
  const result = expect(api('/health')).resolves.toBeInstanceOf(Response)
  await vi.runAllTimersAsync()
  await result
  expect(fetch).toHaveBeenCalledTimes(2)
})

it('stops after two GET retries, and never retries a 4xx or a write', async () => {
  const fetch = vi.fn().mockRejectedValue(new TypeError('Failed to fetch'))
  vi.stubGlobal('fetch', fetch)
  const result = expect(api('/health')).rejects.toMatchObject({ message: 'No connection' })
  await vi.runAllTimersAsync()
  await result
  expect(fetch).toHaveBeenCalledTimes(3)
  fetch.mockClear()
  await expect(api('/runs', { method: 'POST' })).rejects.toMatchObject({ message: 'No connection' })
  expect(fetch).toHaveBeenCalledTimes(1)
  fetch.mockReset().mockResolvedValue(new Response('{}', { status: 403 }))
  expect((await api('/health')).status).toBe(403)
  expect(fetch).toHaveBeenCalledTimes(1)
})

// A held request establishes overlap; exact promise identity proves sharing.
// The late result carries different data so a lost generation fence is visible.
it('shares a current read but never joins or adopts a response from the previous screen', async () => {
  let release!: (value: number[]) => void
  const adopted = vi.fn()
  const read = vi.fn().mockImplementationOnce(() => new Promise<number[]>(resolve => { release = resolve })).mockResolvedValueOnce([2])
  const polled = usePolledData(read, [0], adopted)
  const old = polled.refresh()
  expect(polled.refresh()).toBe(old)
  expect(read).toHaveBeenCalledOnce()
  polled.invalidate()
  const current = polled.refresh()
  expect(current).not.toBe(old)
  await expect(current).resolves.toEqual({ ok: true })
  release([999])
  await expect(old).resolves.toEqual({ ok: false, dropped: true })
  expect(polled.data.value).toEqual([2])
  expect(adopted).toHaveBeenCalledExactlyOnceWith([2])
  expect(polled.status.value).toEqual({ state: 'ready', failures: 0, updatedAt: 100_000, error: '' })
})

it('hidden, offline and stopped polling cannot read or commit late data; visible recovery reads afresh', async () => {
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }))
  vi.stubGlobal('window', new EventTarget())
  vi.stubGlobal('navigator', { onLine: true })
  let release!: (value: number[]) => void
  const read = vi.fn().mockImplementationOnce(() => new Promise<number[]>(resolve => { release = resolve })).mockResolvedValue([2])
  const adopted = vi.fn(), polled = usePolledData(read, [0], adopted)
  const poller = usePoller(() => polled.refresh(), 1000, { invalidate: polled.invalidate })
  stops.push(poller.stop)
  poller.start(true)
  await vi.advanceTimersByTimeAsync(0)
  expect(read).toHaveBeenCalledOnce()
  Object.assign(document, { visibilityState: 'hidden' })
  document.dispatchEvent(new Event('visibilitychange'))
  release([999])
  await vi.advanceTimersByTimeAsync(5000)
  expect(polled.data.value).toEqual([0])
  expect(read).toHaveBeenCalledOnce()
  expect(adopted).not.toHaveBeenCalled()
  Object.assign(document, { visibilityState: 'visible' })
  document.dispatchEvent(new Event('visibilitychange'))
  await vi.advanceTimersByTimeAsync(0)
  expect(polled.data.value).toEqual([2])
  expect(read).toHaveBeenCalledTimes(2)
  Object.assign(navigator, { onLine: false })
  await vi.advanceTimersByTimeAsync(5000)
  expect(read).toHaveBeenCalledTimes(2)
  Object.assign(navigator, { onLine: true })
  window.dispatchEvent(new Event('online'))
  await vi.advanceTimersByTimeAsync(0)
  expect(read).toHaveBeenCalledTimes(3)
  poller.stop()
  document.dispatchEvent(new Event('visibilitychange'))
  window.dispatchEvent(new Event('online'))
  await vi.advanceTimersByTimeAsync(5000)
  expect(read).toHaveBeenCalledTimes(3)
})

it('a hung read cannot block another awake period or overwrite its recovered snapshot', async () => {
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }))
  vi.stubGlobal('window', new EventTarget())
  vi.stubGlobal('navigator', { onLine: true })
  let release!: (value: number[]) => void
  const read = vi.fn().mockImplementationOnce(() => new Promise<number[]>(resolve => { release = resolve })).mockResolvedValue([2])
  const polled = usePolledData(read, [0])
  const poller = usePoller(() => polled.refresh(), 1000, { invalidate: polled.invalidate })
  stops.push(poller.stop)
  poller.start(true)
  await vi.advanceTimersByTimeAsync(0)
  expect(read).toHaveBeenCalledOnce()
  // A timer suspension uses the same injected clock as the data owner.
  vi.setSystemTime(Date.now() + 60_000)
  await vi.advanceTimersByTimeAsync(1000)
  expect(read).toHaveBeenCalledTimes(2)
  expect(polled.data.value).toEqual([2])
  release([999])
  await vi.advanceTimersByTimeAsync(0)
  expect(polled.data.value).toEqual([2])
})

it('mounted consumers share one keyed read and stream; event bursts fence held data and retain one catch-up', async () => {
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }))
  vi.stubGlobal('window', new EventTarget())
  vi.stubGlobal('navigator', { onLine: true })
  const sources: EventTarget[] = [], close = vi.fn()
  vi.stubGlobal('EventSource', class extends EventTarget {
    constructor() { super(); sources.push(this) }
    close = close
  })
  let release!: (value: number[]) => void, held = false
  const read = vi.fn((key: string) => key === 'a' && held
    ? new Promise<number[]>(resolve => { held = false; release = resolve }) : Promise.resolve(key === 'a' ? [2] : [3]))
  const seen: number[][] = []
  const readers = { a: usePolledData(() => read('a'), [] as number[], value => seen.push(value)), b: usePolledData(() => read('b'), [] as number[]) }
  const shared = createSharedRequests(key => readers[key as 'a' | 'b'].refresh(), {
    events: ['queue.changed'], invalidate: key => readers[key as 'a' | 'b'].invalidate(),
    relevant: (key, _type, data) => JSON.parse(String(data)).node_id === key,
  })
  stops.push(shared.stop)
  const first = shared.watch('a'), duplicate = shared.watch('a'), other = shared.watch('b')
  await vi.advanceTimersByTimeAsync(0)
  expect(read.mock.calls).toEqual([['a'], ['b']])
  expect(sources).toHaveLength(1)
  const hint = () => sources[0]!.dispatchEvent(new MessageEvent('queue.changed', { data: JSON.stringify({ node_id: 'a' }) }))
  held = true
  hint()
  await vi.advanceTimersByTimeAsync(REFRESH_BATCH_MS)
  expect(read.mock.calls).toEqual([['a'], ['b'], ['a']])
  for (let n = 0; n < 20; n++) hint()
  await vi.advanceTimersByTimeAsync(REFRESH_BATCH_MS)
  expect(read).toHaveBeenCalledTimes(3)
  release([999])
  await vi.advanceTimersByTimeAsync(0)
  expect(read.mock.calls).toEqual([['a'], ['b'], ['a'], ['a']])
  expect(seen).toEqual([[2], [2]])
  expect(readers.b.data.value).toEqual([3])
  duplicate()
  hint()
  await vi.advanceTimersByTimeAsync(REFRESH_BATCH_MS)
  expect(read).toHaveBeenCalledTimes(5)
  first()
  hint()
  await vi.advanceTimersByTimeAsync(REFRESH_BATCH_MS)
  expect(read).toHaveBeenCalledTimes(5)
  expect(close).not.toHaveBeenCalled()
  other()
  expect(close).toHaveBeenCalledOnce()
})

it('keeps a good snapshot through two failures and flags the third without losing its timestamp', () => {
  const first = refreshStatus(initialRefreshStatus(), { ok: true, at: 100 })
  const one = refreshStatus(first, { ok: false, error: 'No connection' })
  const two = refreshStatus(one, { ok: false, error: 'No connection' })
  const three = refreshStatus(two, { ok: false, error: 'No connection' })
  expect([one.state, two.state, three.state]).toEqual(['ready', 'ready', 'error'])
  expect(three.updatedAt).toBe(100)
  expect(three.failures).toBe(3)
  expect(refreshStatus(three, { ok: true, at: 200 })).toEqual({ state: 'ready', failures: 0, updatedAt: 200, error: '' })
  expect(refreshStatus(initialRefreshStatus(), { ok: false, error: 'No connection' }).state).toBe('error')
})

// AEON-499 review: Check now awaited refresh() for a rejection that never came, so a
// 500 read as "no reading yet". The refresh now says what it did.
it('refresh reports a kept answer, a failure with its reason, and a dropped read', async () => {
  const { APIError } = await import('../src/lib/api')
  const { usePolledData } = await import('../src/lib/usePolledData')
  const ok = usePolledData(async () => [1], [] as number[])
  await expect(ok.refresh()).resolves.toEqual({ ok: true })
  expect(ok.data.value).toEqual([1])
  const empty = usePolledData(async () => [] as number[], [9])
  await expect(empty.refresh()).resolves.toEqual({ ok: true })
  expect(empty.data.value).toEqual([])
  const failed = usePolledData(async () => { throw new APIError(500, 'internal error') }, [] as number[])
  await expect(failed.refresh()).resolves.toEqual({ ok: false, error: 'The server answered “internal error” (500).' })
  expect(failed.status.value.state).toBe('error')
  let release: (v: number[]) => void = () => {}
  const slow = usePolledData(() => new Promise<number[]>(resolve => { release = resolve }), [] as number[])
  const pending = slow.refresh()
  slow.invalidate()
  release([2])
  await expect(pending).resolves.toEqual({ ok: false, dropped: true })
  expect(slow.data.value).toEqual([])
})
