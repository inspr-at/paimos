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
