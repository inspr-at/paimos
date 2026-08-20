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
import { nextTick } from 'vue'

import { ApiError } from '@/api/client'
import { AgentModeLoadError, classifyLoadError, type AgentModeSnapshot } from '@/services/agentMode'
import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import { useAgentModeDeliveries } from './useAgentModeDeliveries'

function snap(n: 1 | 10 | 100 | 0): AgentModeSnapshot {
  return normalizeWireSnapshot(n === 0 ? { server_time: '2026-08-20T13:48:00Z', deliveries: [] } : makeFixtureSnapshot(n), Date.now())
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
})
