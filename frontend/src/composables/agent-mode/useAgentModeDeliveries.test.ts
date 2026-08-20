/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'

import { ApiError } from '@/api/client'
import { AgentModeLoadError, classifyLoadError, type AgentModeSnapshot } from '@/services/agentMode'
import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot, type AgentModeSnapshotQuery } from '@/services/agentModeTransport'
import { useAgentModeDeliveries } from './useAgentModeDeliveries'

function snap(n: 1 | 10 | 100 | 0): AgentModeSnapshot {
  return normalizeWireSnapshot(n === 0 ? { schema_version: 1, server_time: '2026-08-20T13:48:00Z', rows: [] } : makeFixtureSnapshot(n), Date.now())
}

async function flush() {
  for (let i = 0; i < 6; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

describe('useAgentModeDeliveries (PAI-805 honest states)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('goes loading → ready and exposes server clock offset', async () => {
    const loader = vi.fn(async () => snap(10))
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 0 })
    expect(data.status.value).toBe('idle')
    const p = data.load()
    expect(data.status.value).toBe('loading')
    await p
    expect(data.status.value).toBe('ready')
    expect(data.deliveries.value).toHaveLength(10)
    expect(data.deliveriesById.value.size).toBe(10)
    expect(typeof data.serverOffsetMs.value).toBe('number')
    data.dispose()
  })

  it('reports empty without inventing deliveries', async () => {
    const data = useAgentModeDeliveries({ loader: async () => snap(0), hints: false, pollMs: 0 })
    await data.load()
    expect(data.status.value).toBe('empty')
    expect(data.deliveries.value).toEqual([])
    data.dispose()
  })

  it('retries offline failures with backoff and recovers', async () => {
    let calls = 0
    const loader = vi.fn(async () => {
      calls += 1
      if (calls < 3) throw new AgentModeLoadError('offline', 'down', 0)
      return snap(1)
    })
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 0, retryBaseMs: 1_000, retryMaxMs: 8_000 })
    await data.load()
    expect(data.status.value).toBe('offline')
    expect(data.attempt.value).toBe(1)
    expect(data.retryAt.value).not.toBeNull()
    expect(data.deliveries.value).toEqual([])
    await vi.advanceTimersByTimeAsync(1_000)
    await flush()
    expect(calls).toBe(2)
    expect(data.status.value).toBe('offline')
    expect(data.attempt.value).toBe(2)
    await vi.advanceTimersByTimeAsync(2_000)
    await flush()
    expect(calls).toBe(3)
    expect(data.status.value).toBe('ready')
    expect(data.attempt.value).toBe(0)
    data.dispose()
  })

  it('keeps the last known data visible while offline, and does not retry forbidden / not-found / error automatically', async () => {
    let mode: 'ok' | 'offline' | 'forbidden' | 'notfound' | 'error' = 'ok'
    const loader = vi.fn(async () => {
      if (mode === 'ok') return snap(10)
      if (mode === 'offline') throw new AgentModeLoadError('offline', 'down', 0)
      if (mode === 'forbidden') throw new AgentModeLoadError('forbidden', 'nope', 403)
      if (mode === 'notfound') throw new AgentModeLoadError('not-found', 'missing', 404)
      throw new AgentModeLoadError('error', 'boom', 500)
    })
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 0 })
    await data.load()
    expect(data.status.value).toBe('ready')

    mode = 'offline'
    await data.load({ background: true })
    expect(data.status.value).toBe('offline')
    expect(data.hasData.value).toBe(true)
    expect(data.deliveries.value).toHaveLength(10)

    for (const [m, status] of [['forbidden', 'forbidden'], ['notfound', 'not-found'], ['error', 'error']] as const) {
      mode = m
      const before = loader.mock.calls.length
      data.retryNow()
      await flush()
      expect(data.status.value).toBe(status)
      await vi.advanceTimersByTimeAsync(60_000)
      await flush()
      expect(loader.mock.calls.length).toBe(before + 1)
    }
    data.dispose()
  })

  it('classifies transport errors honestly', () => {
    expect(classifyLoadError(new ApiError(403, 'x')).kind).toBe('forbidden')
    expect(classifyLoadError(new ApiError(404, 'x')).kind).toBe('not-found')
    expect(classifyLoadError(new ApiError(0, 'timeout')).kind).toBe('offline')
    expect(classifyLoadError(new ApiError(503, 'x')).kind).toBe('offline')
    expect(classifyLoadError(new ApiError(500, 'x')).kind).toBe('error')
    expect(classifyLoadError(new TypeError('Failed to fetch')).kind).toBe('offline')
    expect(classifyLoadError(new Error('weird')).kind).toBe('error')
  })

  it('polls while visible and ignores stale responses', async () => {
    let calls = 0
    const loader = vi.fn(async () => {
      calls += 1
      return snap(1)
    })
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 5_000 })
    await data.load()
    expect(calls).toBe(1)
    await vi.advanceTimersByTimeAsync(5_000)
    await flush()
    expect(calls).toBe(2)
    expect(data.status.value).toBe('ready')
    data.dispose()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(calls).toBe(2)
  })

  it('keeps query/response parity when an older server-filter response resolves last', async () => {
    const query = ref<AgentModeSnapshotQuery>({ projectId: 6, laneKey: 'project:6/ungrouped' })
    const pending: Array<(value: AgentModeSnapshot) => void> = []
    const loader = vi.fn((_query: AgentModeSnapshotQuery) => new Promise<AgentModeSnapshot>((resolve) => pending.push(resolve)))
    const data = useAgentModeDeliveries({ loader, query, reloadOnQueryChange: false, hints: false, pollMs: 0 })

    const olderLoad = data.load()
    query.value = { projectId: 9, laneKey: 'project:9/epic:9001' }
    const newerLoad = data.load()
    const newer = snap(10)
    newer.cursor = 'newer-filter-response'
    pending[1](newer)
    await newerLoad
    expect(data.snapshot.value?.cursor).toBe('newer-filter-response')
    expect(data.deliveries.value).toHaveLength(10)

    const older = snap(1)
    older.cursor = 'stale-filter-response'
    pending[0](older)
    await olderLoad
    expect(loader.mock.calls.map(([requested]) => requested)).toEqual([
      { projectId: 6, laneKey: 'project:6/ungrouped' },
      { projectId: 9, laneKey: 'project:9/epic:9001' },
    ])
    expect(data.snapshot.value?.cursor).toBe('newer-filter-response')
    expect(data.deliveries.value).toHaveLength(10)
    data.dispose()
  })
})

describe('useAgentModeDeliveries — never retain unauthorized data (PAI-805 corrections)', () => {
  it('retries a rejected selected delivery exactly once without retaining its identity', async () => {
    const query = ref<AgentModeSnapshotQuery>({ selectedDelivery: 'gone-delivery' })
    const rejected = vi.fn()
    const loader = vi.fn(async (requested: AgentModeSnapshotQuery) => {
      if (requested.selectedDelivery) {
        throw new AgentModeLoadError('not-found', 'selection revoked', 404)
      }
      return snap(10)
    })
    const data = useAgentModeDeliveries({
      loader,
      query,
      reloadOnQueryChange: false,
      onSelectedDeliveryRejected: rejected,
      hints: false,
      pollMs: 0,
    })

    await data.load()

    expect(loader).toHaveBeenCalledTimes(2)
    expect(loader.mock.calls.map(([requested]) => requested.selectedDelivery)).toEqual([
      'gone-delivery',
      null,
    ])
    expect(rejected).toHaveBeenCalledOnce()
    expect(rejected).toHaveBeenCalledWith('gone-delivery')
    expect(data.status.value).toBe('ready')
    expect(data.deliveries.value).toHaveLength(10)
    data.dispose()
  })

  it('parks a general 404 and a failed selection fallback without retry loops', async () => {
    const generalLoader = vi.fn(async () => {
      throw new AgentModeLoadError('not-found', 'endpoint missing', 404)
    })
    const general = useAgentModeDeliveries({ loader: generalLoader, hints: false, pollMs: 0 })
    await general.load()
    expect(generalLoader).toHaveBeenCalledOnce()
    expect(general.status.value).toBe('not-found')
    general.dispose()

    const selectedLoader = vi.fn(async () => {
      throw new AgentModeLoadError('not-found', 'still missing', 404)
    })
    const selected = useAgentModeDeliveries({
      loader: selectedLoader,
      query: ref<AgentModeSnapshotQuery>({ selectedDelivery: 'revoked' }),
      reloadOnQueryChange: false,
      hints: false,
      pollMs: 0,
    })
    await selected.load()
    expect(selectedLoader).toHaveBeenCalledTimes(2)
    expect(selected.status.value).toBe('not-found')
    expect(selected.snapshot.value).toBeNull()
    selected.dispose()
  })

  it('drops the previous snapshot immediately on a fresh 403 or 404', async () => {
    for (const [kind, status] of [['forbidden', 403], ['not-found', 404]] as const) {
      let revoke = false
      const data = useAgentModeDeliveries({
        loader: async () => {
          if (revoke) throw new AgentModeLoadError(kind, 'revoked', status)
          return snap(10)
        },
        hints: false,
        pollMs: 0,
      })
      await data.load()
      expect(data.deliveries.value).toHaveLength(10)
      revoke = true
      await data.load({ background: true })
      expect(data.status.value).toBe(kind)
      expect(data.hasData.value).toBe(false)
      expect(data.snapshot.value).toBeNull()
      expect(data.deliveries.value).toEqual([])
      expect(data.deliveriesById.value.size).toBe(0)
      expect(data.degraded.value).toBe(false)
      data.dispose()
    }
  })

  it('keeps last-known data while offline / errored but flags it as degraded', async () => {
    let mode: 'ok' | 'offline' | 'error' = 'ok'
    const data = useAgentModeDeliveries({
      loader: async () => {
        if (mode === 'offline') throw new AgentModeLoadError('offline', 'down', 0)
        if (mode === 'error') throw new AgentModeLoadError('error', 'boom', 500)
        return snap(10)
      },
      hints: false,
      pollMs: 0,
    })
    await data.load()
    expect(data.degraded.value).toBe(false)
    mode = 'offline'
    await data.load({ background: true })
    expect(data.hasData.value).toBe(true)
    expect(data.degraded.value).toBe(true)
    mode = 'error'
    data.retryNow()
    await flush()
    expect(data.status.value).toBe('error')
    expect(data.degraded.value).toBe(true)
    mode = 'ok'
    data.retryNow()
    await flush()
    expect(data.status.value).toBe('ready')
    expect(data.degraded.value).toBe(false)
    data.dispose()
  })
})
