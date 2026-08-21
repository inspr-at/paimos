/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 */

import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, ApiError, StalePermissionsEpochError } from '@/api/client'
import { makeFixtureSnapshot } from './agentModeFixtures'
import { fetchAgentModeSnapshot } from './agentMode'

describe('fetchAgentModeSnapshot authority metadata', () => {
  afterEach(() => vi.restoreAllMocks())

  it('forwards the exact response-local epoch and generation before returning normalized data', async () => {
    const events: string[] = []
    vi.spyOn(api, 'getWithMeta').mockImplementation(async () => {
      events.push('fetch')
      return {
        data: makeFixtureSnapshot(1),
        etag: null,
        lastModified: null,
        permissionsEpoch: '9007199254740993',
        permissionsEpochGeneration: 7,
        status: 200,
      } as never
    })
    const onResponseMeta = vi.fn(() => events.push('meta'))

    const snapshot = await fetchAgentModeSnapshot(
      { projectId: 6, selectedDelivery: 'dlv-1' },
      { onResponseMeta },
    )

    expect(api.getWithMeta).toHaveBeenCalledWith(
      '/agent-mode/deliveries?project_id=6&selected_delivery=dlv-1',
      { signal: undefined },
    )
    expect(onResponseMeta).toHaveBeenCalledWith({
      permissionsEpoch: '9007199254740993',
      permissionsEpochGeneration: 7,
    })
    expect(events).toEqual(['fetch', 'meta'])
    expect(snapshot.deliveries).toHaveLength(1)
  })

  it.each([
    { permissionsEpoch: null, permissionsEpochGeneration: 1 },
    { permissionsEpoch: '01', permissionsEpochGeneration: 1 },
    { permissionsEpoch: '9223372036854775808', permissionsEpochGeneration: 1 },
    { permissionsEpoch: '10', permissionsEpochGeneration: -1 },
  ])('fails closed on missing or invalid authority metadata %#', async (authority) => {
    vi.spyOn(api, 'getWithMeta').mockResolvedValue({
      data: makeFixtureSnapshot(1),
      etag: null,
      lastModified: null,
      ...authority,
      status: 200,
    } as never)
    const onResponseMeta = vi.fn()

    await expect(fetchAgentModeSnapshot({}, { onResponseMeta })).rejects.toMatchObject({
      kind: 'error',
    })
    expect(onResponseMeta).not.toHaveBeenCalled()
  })

  it('propagates stale-authority rejection without exposing a snapshot', async () => {
    const stale = new StalePermissionsEpochError()
    vi.spyOn(api, 'getWithMeta').mockRejectedValue(stale)
    const onResponseMeta = vi.fn()

    await expect(fetchAgentModeSnapshot({}, { onResponseMeta })).rejects.toBe(stale)
    expect(onResponseMeta).not.toHaveBeenCalled()
  })

  it('does not publish metadata or data when the request auth generation is superseded', async () => {
    vi.spyOn(api, 'getWithMeta').mockRejectedValue(
      new ApiError(0, 'request superseded by an authentication change'),
    )
    const onResponseMeta = vi.fn()

    await expect(fetchAgentModeSnapshot({}, { onResponseMeta })).rejects.toMatchObject({
      kind: 'offline',
    })
    expect(onResponseMeta).not.toHaveBeenCalled()
  })
})
