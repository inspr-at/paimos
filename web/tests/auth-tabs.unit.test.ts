// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { AUTH_GENERATION_KEY, followAuthentication } from '../src/lib/authTabs'

afterEach(() => vi.unstubAllGlobals())
function browser(disabled = false) {
  const events = new EventTarget(), values = new Map<string, string>()
  const localStorage = {
    getItem: (key: string) => { if (disabled) throw new Error('storage disabled'); return values.get(key) ?? null },
    setItem: (key: string, value: string) => { if (disabled) throw new Error('storage disabled'); values.set(key, value) },
  }
  vi.stubGlobal('window', { localStorage, addEventListener: events.addEventListener.bind(events), removeEventListener: events.removeEventListener.bind(events) })
  vi.stubGlobal('BroadcastChannel', undefined)
  return {
    values,
    dispatch: (key: string | null, newValue: string | null) => events.dispatchEvent(Object.assign(new Event('storage'), { key, newValue })),
  }
}

it('sees an authentication change synchronously before the storage event, and ignores duplicate or unrelated events', () => {
  const window = browser()
  const changed = vi.fn()
  const first = followAuthentication(changed), second = followAuthentication(() => {})
  const generation = second.publish()
  expect(first.current()).toBe(false)
  expect(changed).not.toHaveBeenCalled()
  window.dispatch('theme', 'dark')
  expect(changed).not.toHaveBeenCalled()
  window.dispatch(AUTH_GENERATION_KEY, generation)
  expect(changed).toHaveBeenCalledTimes(1)
  expect(first.current()).toBe(true)
  window.dispatch(AUTH_GENERATION_KEY, generation)
  expect(changed).toHaveBeenCalledTimes(1)
  first.stop()
  const next = second.publish()
  window.dispatch(AUTH_GENERATION_KEY, next)
  expect(changed).toHaveBeenCalledTimes(1)
  second.stop()
})

it('a delayed storage event uses the latest generation instead of rolling back a newer local sign-in', () => {
  const window = browser()
  const changed = vi.fn(), first = followAuthentication(changed), second = followAuthentication(() => {})
  const stale = second.publish(), fresh = first.publish()
  window.dispatch(AUTH_GENERATION_KEY, stale)
  expect(changed).not.toHaveBeenCalled()
  expect(first.owns(fresh)).toBe(true)
  expect(first.owns(stale)).toBe(false)
  window.values.clear()
  window.dispatch(null, null)
  expect(changed).toHaveBeenCalledTimes(1)
  first.stop(); second.stop()
})

it('uses BroadcastChannel to invalidate a peer when browser storage is disabled', async () => {
  browser(true)
  const peers = new Set<Channel>()
  class Channel {
    onmessage: ((event: { data: unknown }) => void) | null = null
    constructor() { peers.add(this) }
    postMessage(data: unknown) { for (const peer of peers) if (peer !== this) queueMicrotask(() => peer.onmessage?.({ data })) }
    close() { peers.delete(this) }
  }
  vi.stubGlobal('BroadcastChannel', Channel)
  const changed = vi.fn(), first = followAuthentication(changed), second = followAuthentication(() => {})
  const nonce = second.publish()
  await Promise.resolve()
  expect(changed).toHaveBeenCalledTimes(1)
  expect(first.owns(nonce)).toBe(true)
  expect(first.current()).toBe(true)
  first.stop(); second.stop()
  expect(peers.size).toBe(0)
})
