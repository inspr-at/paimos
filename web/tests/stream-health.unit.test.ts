// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { STREAM_CHECK_MS, STREAM_SILENCE_MS, watchStreamHealth } from '../src/lib/streamHealth'
import { listFreshness } from '../src/lib/listFreshness'

let health: ReturnType<typeof watchStreamHealth> | undefined
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(100_000)
  vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }))
  vi.stubGlobal('window', new EventTarget())
  vi.stubGlobal('navigator', { onLine: true })
})
afterEach(() => { health?.stop(); health = undefined; vi.unstubAllGlobals(); vi.useRealTimers() })

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

it('freshness is Live only for trusted reads; stale age preserves the last successful update', () => {
  expect(listFreshness(100_000, false, 900_000).text).toBe('Live')
  expect(listFreshness(100_000, true, 100_001).text).toBe('Reconnecting…')
  expect(listFreshness(100_000, true, 820_000)).toMatchObject({ state: 'stale', text: 'Updated 12 min ago' })
  expect(listFreshness(null, true, 900_000).text).toBe('Reconnecting…')
  expect(listFreshness(100_000, true, 1).text).toBe('Reconnecting…')
})
