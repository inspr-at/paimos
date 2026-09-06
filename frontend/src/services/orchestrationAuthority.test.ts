/* PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only. */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import fixture from '../../../backend/contracts/fixtures/orchestration-v1.json'
import { permissionsEpoch, resetPermissionsEpoch } from '@/api/client'
import { loadOrchestration } from './orchestration'

const response = (epoch: string | null) =>
  new Response(JSON.stringify(fixture), {
    status: 200,
    headers: {
      'Content-Type': 'application/json',
      ...(epoch === null ? {} : { 'X-Permissions-Epoch': epoch }),
    },
  })
const load = (signal?: AbortSignal) => loadOrchestration({ zoom: fixture.fleet.zoom, signal })

describe('orchestration response authority through the real API client', () => {
  beforeEach(() => resetPermissionsEpoch())
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    resetPermissionsEpoch()
  })

  it.each([null, '01', '-1', '9223372036854775808'])(
    'rejects absent or invalid response-local epoch %s',
    async (epoch) => {
      permissionsEpoch.value = '10'
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(epoch)))
      await expect(load()).rejects.toThrow()
    },
  )

  it('accepts an exact int64 response-local epoch', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response('9223372036854775807')))
    await expect(load()).resolves.toEqual(fixture)
  })

  it.each(['permission', 'principal'])(
    'rejects a body finishing after %s changes',
    async (change) => {
      let finish!: (value: string) => void
      const res = response('10')
      vi.spyOn(res, 'text').mockImplementation(
        () =>
          new Promise((resolve) => {
            finish = resolve
          }),
      )
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(res))
      const pending = load()
      await vi.waitFor(() => expect(finish).toBeTypeOf('function'))
      if (change === 'principal') resetPermissionsEpoch()
      else permissionsEpoch.value = '11'
      finish(JSON.stringify(fixture))
      await expect(pending).rejects.toThrow()
    },
  )

  it('forwards cancellation to the transport and exposes no snapshot', async () => {
    const abort = new AbortController()
    let transportSignal: AbortSignal | undefined
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_url, options: RequestInit) =>
          new Promise((_resolve, reject) => {
            transportSignal = options.signal as AbortSignal
            transportSignal.addEventListener('abort', () =>
              reject(new DOMException('Aborted', 'AbortError')),
            )
          }),
      ),
    )
    const pending = load(abort.signal)
    await vi.waitFor(() => expect(transportSignal).toBeDefined())
    abort.abort()
    await expect(pending).rejects.toThrow()
    expect(transportSignal?.aborted).toBe(true)
  })
})
