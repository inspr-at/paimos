// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { STREAM_CHECK_MS, STREAM_SILENCE_MS, watchStreamHealth } from '../src/lib/streamHealth'
import { listFreshness } from '../src/lib/listFreshness'
import { subscribeBrowserEvents } from '../src/lib/sharedRequests'
import { resetPositions } from '../src/lib/position'

let health: ReturnType<typeof watchStreamHealth> | undefined
const stops: (() => void)[] = []
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(100_000)
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }))
  vi.stubGlobal('window', new EventTarget())
  vi.stubGlobal('navigator', { onLine: true })
})
afterEach(() => { for (const stop of stops.splice(0)) stop(); health?.stop(); health = undefined; vi.unstubAllGlobals(); vi.useRealTimers() })

it('detects suspended timers with no browser event, and a backwards clock jump', async () => {
  const recover = vi.fn()
  health = watchStreamHealth(recover)
  vi.setSystemTime(Date.now() + 12 * 60_000)
  await vi.advanceTimersByTimeAsync(STREAM_CHECK_MS)
  expect(recover).toHaveBeenCalledOnce()
  vi.setSystemTime(Date.now() - 60_000)
  await vi.advanceTimersByTimeAsync(STREAM_CHECK_MS)
  expect(recover).toHaveBeenCalledTimes(2)
})

it('resyncs when visible, pageshow or online, without polling a hidden or offline tab', async () => {
  const recover = vi.fn()
  health = watchStreamHealth(recover)
  Object.assign(document, { visibilityState: 'hidden' })
  document.dispatchEvent(new Event('visibilitychange'))
  window.dispatchEvent(new Event('pageshow'))
  await vi.advanceTimersByTimeAsync(60_000)
  expect(recover).not.toHaveBeenCalled()
  Object.assign(document, { visibilityState: 'visible' })
  document.dispatchEvent(new Event('visibilitychange'))
  window.dispatchEvent(new Event('pageshow'))
  window.dispatchEvent(new Event('online'))
  expect(recover).toHaveBeenCalledTimes(3)
  Object.assign(navigator, { onLine: false })
  await vi.advanceTimersByTimeAsync(60_000)
  expect(recover).toHaveBeenCalledTimes(3)
  Object.assign(navigator, { onLine: true })
  window.dispatchEvent(new Event('online'))
  expect(recover).toHaveBeenCalledTimes(4)
})

it('a silent stream recovers at 45 seconds; events and pings extend that deadline', async () => {
  const recover = vi.fn()
  health = watchStreamHealth(recover)
  await vi.advanceTimersByTimeAsync(STREAM_SILENCE_MS - STREAM_CHECK_MS)
  expect(recover).not.toHaveBeenCalled()
  health.heard()
  await vi.advanceTimersByTimeAsync(STREAM_SILENCE_MS - STREAM_CHECK_MS)
  expect(recover).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(STREAM_CHECK_MS)
  expect(recover).toHaveBeenCalledOnce()
})

it('removes timers and wake listeners when the stream owner stops', async () => {
  const recover = vi.fn()
  health = watchStreamHealth(recover)
  health.stop()
  document.dispatchEvent(new Event('visibilitychange'))
  window.dispatchEvent(new Event('pageshow'))
  window.dispatchEvent(new Event('online'))
  await vi.advanceTimersByTimeAsync(5 * STREAM_SILENCE_MS)
  expect(recover).not.toHaveBeenCalled()
})

it('person reset replaces the shared stream and ignores every callback retained by its old source', async () => {
  const sources: Source[] = []
  class Source extends EventTarget {
    onopen: (() => void) | null = null
    onerror: (() => void) | null = null
    closed = false
    constructor(readonly url: string) { super(); sources.push(this) }
    close() { this.closed = true }
  }
  vi.stubGlobal('EventSource', Source)
  const changed = vi.fn(), connected = vi.fn()
  const stop = subscribeBrowserEvents(['session.changed'], changed, connected)
  stops.push(stop)
  const old = sources[0]!
  old.onopen!()
  expect(changed).toHaveBeenCalledOnce()
  resetPositions()
  await vi.advanceTimersByTimeAsync(0)
  expect(old.closed).toBe(true)
  expect(sources).toHaveLength(2)
  changed.mockClear(); connected.mockClear()
  old.onopen!(); old.onerror!()
  old.dispatchEvent(new MessageEvent('session.changed', { data: 'old-person-evidence' }))
  expect(changed).not.toHaveBeenCalled()
  expect(connected).not.toHaveBeenCalled()
  const current = sources[1]!
  current.onopen!()
  current.dispatchEvent(new MessageEvent('session.changed', { data: 'current-person-evidence' }))
  expect(changed.mock.calls).toEqual([[], ['session.changed', 'current-person-evidence']])
  stop()
  changed.mockClear()
  current.onopen!()
  current.dispatchEvent(new MessageEvent('session.changed', { data: 'after-disposal' }))
  expect(changed).not.toHaveBeenCalled()
  expect(current.closed).toBe(true)
})

it('freshness is Live only for trusted reads; stale age preserves the last successful update', () => {
  expect(listFreshness(100_000, false, 900_000).text).toBe('Live')
  expect(listFreshness(100_000, true, 100_001).text).toBe('Reconnecting…')
  expect(listFreshness(100_000, true, 820_000)).toMatchObject({ state: 'stale', text: 'Updated 12 min ago' })
  expect(listFreshness(null, true, 900_000).text).toBe('Reconnecting…')
  expect(listFreshness(100_000, true, 1).text).toBe('Reconnecting…')
})
