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

import {
  ApiError,
  capturePermissionsEpochGeneration,
  observePermissionsEpochHeader,
  resetPermissionsEpoch,
  sessionExpired,
} from '@/api/client'
import { AgentModeLoadError, classifyLoadError, type AgentModeSnapshot } from '@/services/agentMode'
import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot, type AgentModeSnapshotQuery } from '@/services/agentModeTransport'
import type { AgentModeEventSourceLike, AgentModeMessageEvent } from '@/services/agentModeEvents'
import { useAgentModeDeliveries } from './useAgentModeDeliveries'

function snap(n: 1 | 10 | 100 | 0): AgentModeSnapshot {
  return normalizeWireSnapshot(makeFixtureSnapshot(n), Date.now())
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

class FakeEventSource implements AgentModeEventSourceLike {
  readonly listeners = new Map<string, Array<(event: AgentModeMessageEvent) => void>>()
  onerror: ((event: unknown) => void) | null = null
  readyState = 0
  close = vi.fn()

  addEventListener(type: string, listener: (event: AgentModeMessageEvent) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener])
  }

  emit(type: string, event: AgentModeMessageEvent) {
    for (const listener of this.listeners.get(type) ?? []) listener(event)
  }
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
    resetPermissionsEpoch()
    sessionExpired.value = false
  })
  afterEach(() => {
    sessionExpired.value = false
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

  it('scrubs and closes the stream across a 10 to 11 to 12 burst, then commits only the epoch-12 replacement', async () => {
    const stale = deferred<AgentModeSnapshot>()
    const current = deferred<AgentModeSnapshot>()
    let calls = 0
    const loader = vi.fn(async (_query, options) => {
      calls += 1
      if (calls === 1) {
        observePermissionsEpochHeader('10')
        options?.onResponseMeta?.({
          permissionsEpoch: '10',
          permissionsEpochGeneration: capturePermissionsEpochGeneration(),
        })
        return snap(10)
      }
      if (calls === 2) {
        const value = await stale.promise
        options?.onResponseMeta?.({
          permissionsEpoch: '10',
          permissionsEpochGeneration: capturePermissionsEpochGeneration(),
        })
        return value
      }
      const value = await current.promise
      options?.onResponseMeta?.({
        permissionsEpoch: '12',
        permissionsEpochGeneration: capturePermissionsEpochGeneration(),
      })
      return value
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
    })
    await data.load()
    expect(data.authorityVersion.value).toBe(1)
    expect(data.authorityEpoch.value).toBe('10')
    expect(sources).toHaveLength(1)

    const oldLoad = data.load({ background: true, force: true })
    observePermissionsEpochHeader('11')
    expect(data.snapshot.value).toBeNull()
    expect(data.deliveries.value).toEqual([])
    expect(data.authorityEpoch.value).toBeNull()
    expect(data.status.value).toBe('loading')
    expect(sources[0]!.close).toHaveBeenCalledOnce()

    // A newer revocation arrives only after the epoch-10 candidate is already
    // in flight. The queued replacement must target the newest required epoch,
    // never reopen on the intermediate epoch, and never start two replacements.
    observePermissionsEpochHeader('12')
    expect(data.authorityEpoch.value).toBeNull()
    expect(loader).toHaveBeenCalledTimes(2)

    stale.resolve(snap(10))
    await oldLoad
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)
    expect(data.authorityVersion.value).toBe(1)
    expect(data.retryAt.value).toBeNull()
    expect(data.snapshot.value).toBeNull()

    current.resolve(snap(1))
    await flush()
    expect(data.authorityVersion.value).toBe(2)
    expect(data.authorityEpoch.value).toBe('12')
    expect(data.deliveries.value).toHaveLength(1)
    expect(loader).toHaveBeenCalledTimes(3)
    expect(sources).toHaveLength(2)
    data.dispose()
  })

  it('accepts a lower epoch only after reset and never commits an old-generation response that resolves later', async () => {
    const oldResponse = deferred<AgentModeSnapshot>()
    let calls = 0
    let oldGeneration = -1
    const loader = vi.fn(async (_query, options) => {
      calls += 1
      if (calls === 1) {
        observePermissionsEpochHeader('10')
        options?.onResponseMeta?.({
          permissionsEpoch: '10',
          permissionsEpochGeneration: capturePermissionsEpochGeneration(),
        })
        return snap(10)
      }
      if (calls === 2) {
        oldGeneration = capturePermissionsEpochGeneration()
        const value = await oldResponse.promise
        options?.onResponseMeta?.({ permissionsEpoch: '10', permissionsEpochGeneration: oldGeneration })
        return value
      }
      observePermissionsEpochHeader('5')
      options?.onResponseMeta?.({
        permissionsEpoch: '5',
        permissionsEpochGeneration: capturePermissionsEpochGeneration(),
      })
      return snap(1)
    })
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 0 })
    await data.load()
    expect(data.authorityEpoch.value).toBe('10')
    expect(data.authorityVersion.value).toBe(1)

    const pendingOldLoad = data.load({ background: true, force: true })
    expect(loader).toHaveBeenCalledTimes(2)

    resetPermissionsEpoch()
    expect(data.snapshot.value).toBeNull()
    expect(data.authorityEpoch.value).toBeNull()
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)
    expect(data.authorityEpoch.value).toBe('5')
    expect(data.deliveries.value).toHaveLength(1)
    expect(data.authorityVersion.value).toBe(2)

    oldResponse.resolve(snap(100))
    await pendingOldLoad
    await flush()
    expect(data.authorityEpoch.value).toBe('5')
    expect(data.deliveries.value).toHaveLength(1)
    expect(data.authorityVersion.value).toBe(2)
    expect(loader).toHaveBeenCalledTimes(3)
    data.dispose()
  })

  it('supersedes a current response that raises the epoch and commits its one safe replacement', async () => {
    const raised = deferred<AgentModeSnapshot>()
    let calls = 0
    const loader = vi.fn(async (_query, options) => {
      calls += 1
      if (calls === 1) {
        observePermissionsEpochHeader('10')
        options?.onResponseMeta?.({
          permissionsEpoch: '10',
          permissionsEpochGeneration: capturePermissionsEpochGeneration(),
        })
        return snap(10)
      }
      const value = await raised.promise
      observePermissionsEpochHeader('11')
      options?.onResponseMeta?.({
        permissionsEpoch: '11',
        permissionsEpochGeneration: capturePermissionsEpochGeneration(),
      })
      return value
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
    })
    await data.load()
    expect(sources).toHaveLength(1)

    const currentLoad = data.load({ background: true, force: true })
    raised.resolve(snap(1))
    await currentLoad
    await flush()

    expect(sources[0]!.close).toHaveBeenCalledOnce()
    expect(loader).toHaveBeenCalledTimes(3)
    expect(data.authorityEpoch.value).toBe('11')
    expect(data.authorityVersion.value).toBe(2)
    expect(data.deliveries.value).toHaveLength(1)
    data.dispose()
  })

  it('does not let a superseded abort-ignoring request block the next epoch replacement', async () => {
    const superseded = deferred<AgentModeSnapshot>()
    const current = deferred<AgentModeSnapshot>()
    const replacement = deferred<AgentModeSnapshot>()
    let calls = 0
    const loader = vi.fn(async (_query, options) => {
      calls += 1
      const call = calls
      if (call === 1) {
        observePermissionsEpochHeader('10')
        options?.onResponseMeta?.({
          permissionsEpoch: '10',
          permissionsEpochGeneration: capturePermissionsEpochGeneration(),
        })
        return snap(10)
      }
      const value = await (call === 2
        ? superseded.promise
        : call === 3
          ? current.promise
          : replacement.promise)
      options?.onResponseMeta?.({
        permissionsEpoch: call === 4 ? '11' : '10',
        permissionsEpochGeneration: capturePermissionsEpochGeneration(),
      })
      return value
    })
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 0 })
    await data.load()

    const oldLoad = data.load({ background: true, force: true })
    const currentLoad = data.load({ background: true, force: true })
    current.resolve(snap(1))
    await currentLoad
    await flush()
    expect(data.authorityVersion.value).toBe(2)
    expect(loader).toHaveBeenCalledTimes(3)

    observePermissionsEpochHeader('11')
    expect(data.snapshot.value).toBeNull()
    expect(data.authorityEpoch.value).toBeNull()
    await flush()
    expect(loader).toHaveBeenCalledTimes(4)

    replacement.resolve(snap(1))
    await flush()
    expect(data.authorityEpoch.value).toBe('11')
    expect(data.authorityVersion.value).toBe(3)

    superseded.resolve(snap(100))
    await oldLoad
    await flush()
    expect(data.deliveries.value).toHaveLength(1)
    expect(data.authorityEpoch.value).toBe('11')
    expect(data.authorityVersion.value).toBe(3)
    data.dispose()
  })

  it('aborts a body-hung current snapshot and starts the higher-epoch replacement before it settles', async () => {
    const replacement = deferred<AgentModeSnapshot>()
    const signals: AbortSignal[] = []
    let calls = 0
    const loader = vi.fn(async (_query, options) => {
      calls += 1
      const call = calls
      if (options?.signal) signals.push(options.signal)
      if (call === 1) {
        observePermissionsEpochHeader('10')
        options?.onResponseMeta?.({
          permissionsEpoch: '10',
          permissionsEpochGeneration: capturePermissionsEpochGeneration(),
        })
        return snap(10)
      }
      if (call === 2) return await new Promise<AgentModeSnapshot>(() => {})
      const value = await replacement.promise
      options?.onResponseMeta?.({
        permissionsEpoch: '11',
        permissionsEpochGeneration: capturePermissionsEpochGeneration(),
      })
      return value
    })
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 0 })
    await data.load()
    void data.load({ background: true, force: true })
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)

    observePermissionsEpochHeader('11')
    expect(signals[1]!.aborted).toBe(true)
    expect(data.snapshot.value).toBeNull()
    expect(data.authorityEpoch.value).toBeNull()
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)

    replacement.resolve(snap(1))
    await flush()
    expect(data.authorityEpoch.value).toBe('11')
    expect(data.authorityVersion.value).toBe(2)
    expect(data.deliveries.value).toHaveLength(1)
    data.dispose()
  })

  it('advances authorityVersion only for a current successful snapshot commit', async () => {
    const pending: Array<{
      resolve: (value: AgentModeSnapshot) => void
      reject: (cause: unknown) => void
    }> = []
    const loader = vi.fn(() => new Promise<AgentModeSnapshot>((resolve, reject) => {
      pending.push({ resolve, reject })
    }))
    const data = useAgentModeDeliveries({ loader, hints: false, pollMs: 0 })
    expect(data.authorityVersion.value).toBe(0)

    const staleLoad = data.load({ force: true })
    const currentLoad = data.load({ force: true })
    pending[1].resolve(snap(10))
    await currentLoad
    expect(data.authorityVersion.value).toBe(1)

    pending[0].resolve(snap(1))
    await staleLoad
    expect(data.authorityVersion.value).toBe(1)

    const failedLoad = data.load({ force: true })
    pending[2].reject(new AgentModeLoadError('error', 'failed', 500))
    await failedLoad
    expect(data.authorityVersion.value).toBe(1)

    sessionExpired.value = true
    expect(data.snapshot.value).toBeNull()
    expect(data.authorityVersion.value).toBe(1)
    data.dispose()
  })

  it('supersedes a selected-delivery-only request without replacing the stream', async () => {
    const query = ref<AgentModeSnapshotQuery>({ projectId: 6, selectedDelivery: 'dlv-A' })
    const pending = new Map<string, (value: AgentModeSnapshot) => void>()
    const loader = vi.fn((requested: AgentModeSnapshotQuery) => new Promise<AgentModeSnapshot>((resolve) => {
      pending.set(requested.selectedDelivery ?? '', resolve)
    }))
    const sources: FakeEventSource[] = []
    const eventSourceFactory = vi.fn(() => {
      const source = new FakeEventSource()
      sources.push(source)
      return source
    })
    const data = useAgentModeDeliveries({
      loader,
      query,
      reloadOnQueryChange: false,
      eventSourceFactory,
      pollMs: 0,
    })

    const initial = data.load()
    const initialSnapshot = snap(1)
    initialSnapshot.selectedDeliveryId = 'dlv-A'
    pending.get('dlv-A')!(initialSnapshot)
    await initial
    expect(sources).toHaveLength(1)

    query.value = { projectId: 6, selectedDelivery: 'dlv-B' }
    const selectedB = data.load()
    query.value = { projectId: 6, selectedDelivery: 'dlv-C' }
    const selectedC = data.load()

    const current = snap(10)
    current.selectedDeliveryId = 'dlv-C'
    pending.get('dlv-C')!(current)
    await selectedC
    expect(data.snapshot.value?.selectedDeliveryId).toBe('dlv-C')

    const stale = snap(1)
    stale.selectedDeliveryId = 'dlv-B'
    pending.get('dlv-B')!(stale)
    await selectedB

    expect(loader.mock.calls.map(([requested]) => requested.selectedDelivery)).toEqual(['dlv-A', 'dlv-B', 'dlv-C'])
    expect(data.snapshot.value?.selectedDeliveryId).toBe('dlv-C')
    expect(sources).toHaveLength(1)
    expect(sources[0].close).not.toHaveBeenCalled()
    data.dispose()
  })

  it('closes immediately on global session loss and reopens once from a fresh snapshot', async () => {
    const loader = vi.fn(async () => snap(1))
    const sources: FakeEventSource[] = []
    const eventSourceFactory = vi.fn(() => {
      const source = new FakeEventSource()
      sources.push(source)
      return source
    })
    const data = useAgentModeDeliveries({ loader, eventSourceFactory, pollMs: 0 })
    await data.load()
    expect(loader).toHaveBeenCalledOnce()
    expect(sources).toHaveLength(1)
    expect(data.hasData.value).toBe(true)

    sessionExpired.value = true
    expect(sources[0].close).toHaveBeenCalledOnce()
    expect(data.hasData.value).toBe(false)

    sources[0].emit('refetch', {
      data: JSON.stringify({ schema_version: 1 }),
      lastEventId: 'Z'.repeat(211),
    })
    await vi.advanceTimersByTimeAsync(1_000)
    await flush()
    expect(loader).toHaveBeenCalledOnce()

    sessionExpired.value = false
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)
    expect(sources).toHaveLength(2)
    expect(sources[1].close).not.toHaveBeenCalled()
    data.dispose()
  })

  it('keeps CONNECTING native reconnect single-source and lets retry own an outage episode', async () => {
    let online = true
    const loader = vi.fn(async () => {
      if (!online) throw new AgentModeLoadError('offline', 'down', 0)
      return snap(1)
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
      retryBaseMs: 1_000,
      retryMaxMs: 8_000,
    })
    await data.load()
    online = false
    for (let index = 0; index < 100; index += 1) sources[0].onerror?.(new Error('reconnect'))
    await vi.advanceTimersByTimeAsync(750)
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)
    expect(sources).toHaveLength(1)
    expect(sources[0].close).not.toHaveBeenCalled()
    expect(data.status.value).toBe('offline')

    // Further transport errors cannot bypass the authoritative backoff.
    for (let index = 0; index < 100; index += 1) sources[0].onerror?.(new Error('still reconnecting'))
    await vi.advanceTimersByTimeAsync(999)
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)

    online = true
    await vi.advanceTimersByTimeAsync(2_000)
    await flush()
    expect(loader).toHaveBeenCalledTimes(4)
    expect(data.status.value).toBe('ready')
    expect(sources).toHaveLength(1)

    // A successful authoritative recovery starts a new error episode.
    sources[0].emit('open', { data: '', lastEventId: '' })
    sources[0].onerror?.(new Error('later episode'))
    await vi.advanceTimersByTimeAsync(750)
    await flush()
    expect(loader).toHaveBeenCalledTimes(5)
    data.dispose()
  })

  it('lets exponential retry exclusively own an outage after a signal queued behind a pending probe', async () => {
    let call = 0
    let rejectProbe!: (cause: unknown) => void
    const loader = vi.fn(async () => {
      call += 1
      if (call === 2) {
        return await new Promise<AgentModeSnapshot>((_resolve, reject) => { rejectProbe = reject })
      }
      return snap(1)
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
      retryBaseMs: 2_000,
      retryMaxMs: 8_000,
    })
    await data.load()
    const probe = data.load({ background: true, force: true })
    sources[0].emit('refetch', {
      data: JSON.stringify({ schema_version: 1 }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    await vi.advanceTimersByTimeAsync(750)
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)

    rejectProbe(new AgentModeLoadError('offline', 'down', 0))
    await probe
    expect(data.status.value).toBe('offline')
    expect(data.retryAt.value).not.toBeNull()
    await vi.advanceTimersByTimeAsync(1_999)
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)
    expect(data.status.value).toBe('ready')
    data.dispose()
  })

  it('does not let later refetch hints bypass offline backoff or a parked server error', async () => {
    let mode: 'ok' | 'offline' | 'error' = 'ok'
    const loader = vi.fn(async () => {
      if (mode === 'offline') throw new AgentModeLoadError('offline', 'down', 0)
      if (mode === 'error') throw new AgentModeLoadError('error', 'boom', 500)
      return snap(1)
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
      retryBaseMs: 2_000,
      retryMaxMs: 8_000,
    })
    await data.load()

    mode = 'offline'
    await data.load({ background: true, force: true })
    expect(data.status.value).toBe('offline')
    expect(data.retryAt.value).not.toBeNull()
    for (let index = 0; index < 100; index += 1) {
      sources[0].emit('refetch', {
        data: JSON.stringify({ schema_version: 1 }),
        lastEventId: `${'A'.repeat(210)}A`,
      })
      sources[0].onerror?.(new Error('still reconnecting'))
    }
    await vi.advanceTimersByTimeAsync(1_999)
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)

    mode = 'ok'
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)
    expect(data.status.value).toBe('ready')

    mode = 'error'
    await data.load({ background: true, force: true })
    expect(data.status.value).toBe('error')
    for (let index = 0; index < 100; index += 1) {
      sources[0].emit('refetch', {
        data: JSON.stringify({ schema_version: 1 }),
        lastEventId: `${'A'.repeat(210)}A`,
      })
      sources[0].onerror?.(new Error('parked reconnect'))
    }
    await vi.advanceTimersByTimeAsync(60_000)
    await flush()
    expect(loader).toHaveBeenCalledTimes(4)

    // A reset remains a fail-closed force path even though hint probes are
    // parked. It clears the old source and resynchronizes immediately.
    mode = 'ok'
    sources[0].emit('reset', {
      data: JSON.stringify({ schema_version: 1, reason: 'resync_required' }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    await flush()
    expect(loader).toHaveBeenCalledTimes(5)
    expect(data.status.value).toBe('ready')
    expect(sources).toHaveLength(2)
    data.dispose()
  })

  it('retires CLOSED EventSource and reopens only after an authoritative success', async () => {
    const loader = vi.fn(async () => snap(1))
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
    })
    await data.load()
    sources[0].readyState = 2
    sources[0].onerror?.(new Error('closed'))
    expect(sources[0].close).toHaveBeenCalledOnce()
    expect(sources).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(749)
    expect(loader).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)
    expect(sources).toHaveLength(2)
    data.dispose()
  })

  it('replaces a source that becomes CLOSED after its CONNECTING probe already succeeded', async () => {
    const loader = vi.fn(async () => snap(1))
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
    })
    await data.load()
    sources[0].onerror?.(new Error('connecting'))
    await vi.advanceTimersByTimeAsync(750)
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)
    expect(sources).toHaveLength(1)

    // No open event occurred; the same transport now becomes terminal.
    sources[0].readyState = 2
    sources[0].onerror?.(new Error('closed'))
    expect(sources[0].close).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(750)
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)
    expect(sources).toHaveLength(2)
    data.dispose()
  })

  it('makes the newest reset token authoritative across session loss while late old work stays inert', async () => {
    const query = ref<AgentModeSnapshotQuery>({ projectId: 6 })
    let resolveOldReset!: (value: AgentModeSnapshot) => void
    let resolveNewReset!: (value: AgentModeSnapshot) => void
    let call = 0
    const loader = vi.fn(async () => {
      call += 1
      if (call === 2) return await new Promise<AgentModeSnapshot>((resolve) => { resolveOldReset = resolve })
      if (call === 4) return await new Promise<AgentModeSnapshot>((resolve) => { resolveNewReset = resolve })
      return snap(1)
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      query,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
    })
    await data.load()
    sources[0].emit('refetch', {
      data: JSON.stringify({ schema_version: 1 }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    sources[0].emit('reset', {
      data: JSON.stringify({ schema_version: 1, reason: 'resync_required' }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    await flush()
    expect(loader).toHaveBeenCalledTimes(2)
    expect(data.hasData.value).toBe(false)

    sessionExpired.value = true
    sessionExpired.value = false
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)
    expect(sources).toHaveLength(2)

    // Seed another queued signal, then supersede it with the new reset.
    sources[1].emit('refetch', {
      data: JSON.stringify({ schema_version: 1 }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    sources[1].emit('reset', {
      data: JSON.stringify({ schema_version: 1, reason: 'resync_required' }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    await flush()
    expect(loader).toHaveBeenCalledTimes(4)
    const newest = snap(10)
    resolveNewReset(newest)
    await flush()
    expect(data.snapshot.value).toBe(newest)
    expect(sources).toHaveLength(3)

    const stale = snap(1)
    resolveOldReset(stale)
    await flush()
    await vi.advanceTimersByTimeAsync(2_000)
    await flush()
    expect(data.snapshot.value).toBe(newest)
    expect(loader).toHaveBeenCalledTimes(4)
    expect(sources).toHaveLength(3)
    expect(sources[2].close).not.toHaveBeenCalled()
    data.dispose()
  })

  it('clears an old binding synchronously and never restores it after a narrowed load fails', async () => {
    const query = ref<AgentModeSnapshotQuery>({})
    let rejectNarrow!: (cause: unknown) => void
    const loader = vi.fn(async (requested: AgentModeSnapshotQuery) => {
      if (requested.q === 'private-scope') {
        return await new Promise<AgentModeSnapshot>((_resolve, reject) => { rejectNarrow = reject })
      }
      return snap(10)
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      query,
      reloadOnQueryChange: false,
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
    })
    await data.load()
    expect(data.deliveries.value).toHaveLength(10)
    query.value = { q: 'private-scope' }
    const narrowed = data.load({ background: true })
    expect(data.snapshot.value).toBeNull()
    expect(data.deliveries.value).toEqual([])
    expect(sources[0].close).toHaveBeenCalledOnce()
    rejectNarrow(new AgentModeLoadError('offline', 'down', 0))
    await narrowed
    expect(data.status.value).toBe('offline')
    expect(data.snapshot.value).toBeNull()
    sources[0].emit('refetch', {
      data: JSON.stringify({ schema_version: 1 }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    await vi.advanceTimersByTimeAsync(1_000)
    expect(loader).toHaveBeenCalledTimes(2)
    data.dispose()
  })

  it('fences invalid query state and starts a fresh same-identity request after correction', async () => {
    const query = ref<AgentModeSnapshotQuery>({ projectId: 6 })
    const pending: Array<(value: AgentModeSnapshot) => void> = []
    const loader = vi.fn(async () => await new Promise<AgentModeSnapshot>((resolve) => pending.push(resolve)))
    const data = useAgentModeDeliveries({ loader, query, reloadOnQueryChange: false, hints: false, pollMs: 0 })
    const first = data.load()
    query.value = { projectId: '06' as never }
    await data.load()
    expect(data.snapshot.value).toBeNull()
    expect(data.status.value).toBe('error')
    query.value = { projectId: 6 }
    const corrected = data.load()
    expect(loader).toHaveBeenCalledTimes(2)
    const fresh = snap(10)
    pending[1](fresh)
    await corrected
    expect(data.snapshot.value).toBe(fresh)
    const stale = snap(1)
    pending[0](stale)
    await first
    expect(data.snapshot.value).toBe(fresh)
    data.dispose()
  })

  it('drops a queued stream refresh when selected B is rejected before its one selector-free fallback', async () => {
    const query = ref<AgentModeSnapshotQuery>({ selectedDelivery: 'dlv-A' })
    let rejectB!: (cause: unknown) => void
    const loader = vi.fn(async (requested: AgentModeSnapshotQuery) => {
      if (requested.selectedDelivery === 'dlv-B') {
        return await new Promise<AgentModeSnapshot>((_resolve, reject) => { rejectB = reject })
      }
      return snap(10)
    })
    const sources: FakeEventSource[] = []
    const data = useAgentModeDeliveries({
      loader,
      query,
      reloadOnQueryChange: false,
      onSelectedDeliveryRejected: async () => {
        query.value = { selectedDelivery: null }
        await Promise.resolve()
      },
      eventSourceFactory: () => {
        const source = new FakeEventSource()
        sources.push(source)
        return source
      },
      pollMs: 0,
    })

    await data.load()
    query.value = { selectedDelivery: 'dlv-B' }
    const selectedB = data.load({ background: true })
    sources[0].emit('refetch', {
      data: JSON.stringify({ schema_version: 1 }),
      lastEventId: `${'A'.repeat(210)}A`,
    })
    await vi.advanceTimersByTimeAsync(750)
    await flush()
    expect(loader.mock.calls.map(([requested]) => requested.selectedDelivery)).toEqual(['dlv-A', 'dlv-B'])

    rejectB(new AgentModeLoadError('not-found', 'selection revoked', 404))
    await selectedB
    await flush()
    expect(loader.mock.calls.map(([requested]) => requested.selectedDelivery)).toEqual(['dlv-A', 'dlv-B', null])
    expect(sources).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(5_000)
    await flush()
    expect(loader).toHaveBeenCalledTimes(3)
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
