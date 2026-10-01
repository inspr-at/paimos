// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { api, RequestFailure, retryable, retryDelay, sessionEnded } from '../src/lib/api'
import { initialRefreshStatus, refreshStatus } from '../src/lib/usePolledData'

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); sessionEnded.blocked = false })

it('retries only idempotent GET transport failures, with bounded exponential jitter', async () => {
  expect(retryable('GET', new RequestFailure('timeout'))).toBe(true)
  expect(retryable('GET', new RequestFailure('network'))).toBe(true)
  expect(retryable('POST', new RequestFailure('network'))).toBe(false)
  expect(retryable('GET', new Error('server rejected the request'))).toBe(false)
  expect(retryDelay(0, () => 0)).toBe(187.5)
  expect(retryDelay(1, () => 1)).toBe(625)
  const fetch = vi.fn().mockRejectedValueOnce(new TypeError('Failed to fetch')).mockResolvedValue(new Response('{}'))
  vi.stubGlobal('fetch', fetch)
  await expect(api('/health')).resolves.toBeInstanceOf(Response)
  expect(fetch).toHaveBeenCalledTimes(2)
})

it('stops after two GET retries, and never retries a 4xx or a write', async () => {
  const fetch = vi.fn().mockRejectedValue(new TypeError('Failed to fetch'))
  vi.stubGlobal('fetch', fetch)
  await expect(api('/health')).rejects.toMatchObject({ message: 'No connection' })
  expect(fetch).toHaveBeenCalledTimes(3)
  fetch.mockClear()
  await expect(api('/runs', { method: 'POST' })).rejects.toMatchObject({ message: 'No connection' })
  expect(fetch).toHaveBeenCalledTimes(1)
  fetch.mockReset().mockResolvedValue(new Response('{}', { status: 403 }))
  expect((await api('/health')).status).toBe(403)
  expect(fetch).toHaveBeenCalledTimes(1)
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
