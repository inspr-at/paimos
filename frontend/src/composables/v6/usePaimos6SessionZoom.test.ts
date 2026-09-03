import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it } from 'vitest'

import { ApiError } from '@/api/client'
import type { Paimos6SessionViewModel } from '@/v6/sessionHome'
import type { Paimos6SessionZoomProjection } from '@/v6/sessionHomeZoom'
import { paimos6SelectionStorageKey, persistPaimos6Selection } from '@/v6/sessionHomeSelection'
import { usePaimos6SessionZoom } from './usePaimos6SessionZoom'

const A_ID = '17e5d8f7-0b11-4bee-a8a4-a11406de865a'
const B_ID = '27e5d8f7-0b11-4bee-a8a4-a11406de865a'
const C_ID = '37e5d8f7-0b11-4bee-a8a4-a11406de865a'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function storage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
  }
}

function row(id: string, title = id): Paimos6SessionViewModel {
  return {
    id,
    revision: 1,
    title,
    summary: '',
    agent: 'codex:amy',
    status: 'working',
    statusLabel: 'Working · active harness',
    activityState: 'busy',
    activityReason: 'adapter_activity',
    activityAgeSeconds: 2,
    closedReason: '',
    attention: false,
    attentionReason: null,
    exceptionCount: 0,
    actionRequestCount: 0,
    node: null,
    unread: 0,
    latestUnreadAt: null,
    mode: 'managed',
    harnessName: 'codex',
    advertisedCapabilities: { inbox: true, status: true, steer: true, interrupt: true, stop: true },
    capabilities: { directSteer: true, interrupt: true, stop: true, paimosSteer: false },
  }
}

function projection(
  projectId: number,
  zoom: string,
  sessions: Paimos6SessionViewModel[],
  selectedSession: Paimos6SessionViewModel | null = null,
): Paimos6SessionZoomProjection {
  const sampleLimit = zoom.length >= 3
    ? 100
    : zoom.length === 1
      ? zoom.charCodeAt(0) - 48
      : (zoom.charCodeAt(0) - 48) * 10 + zoom.charCodeAt(1) - 48
  return {
    projectId,
    zoom,
    band: zoom.length >= 4 ? 'far' : zoom.length >= 3 ? 'aggregate' : zoom.length >= 2 ? 'overview' : 'detail',
    sampleLimit,
    sampleTruncated: false,
    sessions,
    selectedSession,
    totals: {
      sessions: sessions.length + (selectedSession && !sessions.some(({ id }) => id === selectedSession.id) ? 1 : 0),
      unread: 0,
      attention_sessions: 0,
      exception_messages: 0,
      action_requests: 0,
      exception_targets: 0,
      sampled_exception_targets: 0,
    },
  }
}

async function settle() {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
}

describe('usePaimos6SessionZoom fenced selection owner (PAI-864)', () => {
  it('restores stored selection on initial no-session startup but honors popstate removal', async () => {
    const memory = storage()
    const linkedSession = ref<string | null>(null)
    persistPaimos6Selection(memory, { principalId: 7, projectId: 42 }, B_ID)
    const replacements: Array<string | null> = []
    const candidates: Array<string | null> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionZoom({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId: ref(42),
      zoom: ref('10'),
      deepLinkedProjectId: ref(42),
      deepLinkedSessionId: linkedSession,
      storage: memory,
      replaceSessionQuery: (id) => { replacements.push(id) },
      load: async (projectId, zoom, candidate) => {
        candidates.push(candidate)
        return projection(projectId, zoom, [row(A_ID)], candidate === null ? null : row(candidate, 'Stored B'))
      },
    }))!
    await settle()
    expect(home.selectedId.value).toBe(B_ID)
    expect(home.selectedSession.value?.title).toBe('Stored B')
    expect(replacements).toEqual([B_ID])

    // Simulate the router publishing the restored URL, then the browser Back
    // button returning to the earlier entry with no session query.
    linkedSession.value = B_ID
    await settle()
    linkedSession.value = null
    expect(home.selectedId.value).toBeNull()
    expect(home.selectedSession.value).toBeNull()
    expect(home.sessions.value).toEqual([])
    await settle()

    expect(candidates).toEqual([B_ID, B_ID, null])
    expect(home.selectedId.value).toBeNull()
    expect(home.selectedSession.value).toBeNull()
    expect(replacements).toEqual([B_ID])
    expect(memory.getItem(paimos6SelectionStorageKey({ principalId: 7, projectId: 42 }))).toBeNull()
    scope.stop()
  })

  it('keeps an out-of-sample selection stable across zoom and sample reorder', async () => {
    const zoom = ref('1')
    const selected = ref<string | null>(B_ID)
    const loads: Array<{ zoom: string; selected: string | null }> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionZoom({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId: ref(42),
      zoom,
      deepLinkedProjectId: ref(42),
      deepLinkedSessionId: selected,
      storage: storage(),
      replaceSessionQuery: () => {},
      load: async (projectId, requestedZoom, selectedId) => {
        loads.push({ zoom: requestedZoom, selected: selectedId })
        return projection(projectId, requestedZoom, [row(requestedZoom === '1' ? A_ID : C_ID)], row(B_ID, 'Pinned B'))
      },
    }))!
    await settle()
    expect(home.sessions.value.map(({ id }) => id)).toEqual([A_ID])
    expect(home.selectedSession.value?.title).toBe('Pinned B')

    zoom.value = '25'
    expect(home.sessions.value).toEqual([])
    expect(home.selectedSession.value).toBeNull()
    expect(home.selectedId.value).toBe(B_ID)
    await settle()
    expect(home.sessions.value.map(({ id }) => id)).toEqual([C_ID])
    expect(home.selectedSession.value?.id).toBe(B_ID)
    expect(loads).toEqual([
      { zoom: '1', selected: B_ID },
      { zoom: '25', selected: B_ID },
    ])
    scope.stop()
  })

  it('retries a selected 404 unselected and performs one clear only after success', async () => {
    const memory = storage()
    persistPaimos6Selection(memory, { principalId: 7, projectId: 42 }, B_ID)
    const replacements: Array<string | null> = []
    const candidates: Array<string | null> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionZoom({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId: ref(42),
      zoom: ref('10'),
      deepLinkedSessionId: ref(B_ID),
      storage: memory,
      replaceSessionQuery: (id) => { replacements.push(id) },
      load: async (projectId, zoom, candidate) => {
        candidates.push(candidate)
        if (candidate) throw new ApiError(404, 'not found')
        return projection(projectId, zoom, [row(A_ID)])
      },
    }))!
    await settle()
    expect(candidates).toEqual([B_ID, null])
    expect(home.state.value).toBe('ready')
    expect(home.selectedId.value).toBeNull()
    expect(replacements).toEqual([null])
    expect(memory.getItem(paimos6SelectionStorageKey({ principalId: 7, projectId: 42 }))).toBeNull()
    scope.stop()
  })

  it('keeps the scope unavailable and does not clear URL/storage when the retry also fails', async () => {
    const memory = storage()
    persistPaimos6Selection(memory, { principalId: 7, projectId: 42 }, B_ID)
    const replacements: Array<string | null> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionZoom({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId: ref(42),
      zoom: ref('10'),
      deepLinkedSessionId: ref(B_ID),
      storage: memory,
      replaceSessionQuery: (id) => { replacements.push(id) },
      load: async (_projectId, _zoom, candidate) => {
        if (candidate) throw new ApiError(404, 'not found')
        throw new ApiError(404, 'project not found')
      },
    }))!
    await settle()
    expect(home.state.value).toBe('unavailable')
    expect(replacements).toEqual([])
    expect(memory.getItem(paimos6SelectionStorageKey({ principalId: 7, projectId: 42 }))).toBe(B_ID)
    scope.stop()
  })

  it('fences abort-ignoring authority and zoom responses synchronously', async () => {
    const authority = ref('old-authority')
    const zoom = ref('10')
    const pending: Array<ReturnType<typeof deferred<Paimos6SessionZoomProjection>>> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionZoom({
      principalId: ref(7),
      authorityKey: authority,
      projectId: ref(42),
      zoom,
      deepLinkedSessionId: ref(null),
      storage: storage(),
      replaceSessionQuery: () => {},
      load: () => {
        const request = deferred<Paimos6SessionZoomProjection>()
        pending.push(request)
        return request.promise
      },
    }))!
    await settle()
    authority.value = 'new-authority'
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()
    await settle()
    pending[0].resolve(projection(42, '10', [row(A_ID, 'stale authority')]))
    await settle()
    expect(home.sessions.value).toEqual([])

    zoom.value = '25'
    expect(home.sessions.value).toEqual([])
    await settle()
    pending[1].resolve(projection(42, '10', [row(B_ID, 'stale zoom')]))
    pending[2].resolve(projection(42, '25', [row(C_ID, 'current zoom')]))
    await settle()
    expect(home.sessions.value.map(({ title }) => title)).toEqual(['current zoom'])
    scope.stop()
  })

  it('holds an atomic project/session/zoom transition and never exposes stale A', async () => {
    const projectId = ref<number | null>(42)
    const linkedProject = ref<number | null>(42)
    const linkedSession = ref<string | null>(A_ID)
    const zoom = ref('10')
    const pending: Array<{ projectId: number; zoom: string; candidate: string | null; request: ReturnType<typeof deferred<Paimos6SessionZoomProjection>> }> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionZoom({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId,
      zoom,
      deepLinkedProjectId: linkedProject,
      deepLinkedSessionId: linkedSession,
      storage: storage(),
      replaceSessionQuery: () => {},
      load: (requestedProject, requestedZoom, candidate) => {
        const request = deferred<Paimos6SessionZoomProjection>()
        pending.push({ projectId: requestedProject, zoom: requestedZoom, candidate, request })
        return request.promise
      },
    }))!
    await settle()

    linkedProject.value = 99
    linkedSession.value = B_ID
    zoom.value = '1000'
    expect(home.sessions.value).toEqual([])
    projectId.value = 99
    await settle()
    expect(pending[1]).toMatchObject({ projectId: 99, zoom: '1000', candidate: B_ID })

    pending[0].request.resolve(projection(42, '10', [row(A_ID)], row(A_ID)))
    await settle()
    expect(home.sessions.value).toEqual([])
    pending[1].request.resolve(projection(99, '1000', [row(C_ID)], row(B_ID, 'Selected B')))
    await settle()
    expect(home.sessions.value.map(({ id }) => id)).toEqual([C_ID])
    expect(home.selectedSession.value?.title).toBe('Selected B')
    scope.stop()
  })
})
