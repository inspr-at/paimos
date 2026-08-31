import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it } from 'vitest'

import type { Paimos6SessionHomeProjection } from './usePaimos6SessionHome'
import { usePaimos6SessionHome } from './usePaimos6SessionHome'
import type { Paimos6SessionViewModel } from '@/v6/sessionHome'
import { paimos6SelectionStorageKey, persistPaimos6Selection } from '@/v6/sessionHomeSelection'

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

function row(id: string, attention = false): Paimos6SessionViewModel {
  return {
    id,
    title: id,
    summary: '',
    agent: 'codex:amy',
    status: 'working',
    statusLabel: 'Working · active harness',
    attention,
    attentionReason: attention ? '1 action request held for you' : null,
    exceptionCount: attention ? 1 : 0,
    actionRequestCount: attention ? 1 : 0,
    node: null,
    unread: 0,
    latestUnreadAt: null,
    mode: 'managed',
    harnessName: 'codex',
    advertisedCapabilities: { inbox: true, status: true, steer: true, interrupt: true, stop: true },
    capabilities: { directSteer: true, interrupt: true, stop: true, paimosSteer: false },
  }
}

function projection(projectId: number, sessions: Paimos6SessionViewModel[]): Paimos6SessionHomeProjection {
  return {
    projectId,
    sessions,
    totals: {
      sessions: sessions.length,
      unread: sessions.reduce((sum, session) => sum + session.unread, 0),
      attention: sessions.filter((session) => session.attention).length,
    },
  }
}

async function settle() {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
}

describe('usePaimos6SessionHome authority and selection owner (PAI-861)', () => {
  it('restores stored selection, keeps it through attention/reordering, and never auto-selects', async () => {
    const memory = storage()
    persistPaimos6Selection(memory, { principalId: 7, projectId: 42 }, 'session-b')
    let response = projection(42, [row('session-a'), row('session-b')])
    const replacements: Array<string | null> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionHome({
      principalId: ref(7),
      authorityKey: ref('authority-a'),
      projectId: ref(42),
      deepLinkedSessionId: ref(null),
      storage: memory,
      load: async () => response,
      replaceSessionQuery: (id) => { replacements.push(id) },
    }))!
    await settle()
    expect(home.selectedId.value).toBe('session-b')

    response = projection(42, [row('session-b', true), row('session-a')])
    await home.load(true)
    expect(home.selectedId.value).toBe('session-b')

    home.clearSelection()
    response = projection(42, [row('session-a'), row('session-b', true)])
    await home.load(true)
    expect(home.selectedId.value).toBeNull()
    expect(replacements[replacements.length - 1]).toBeNull()
    scope.stop()
  })

  it('clears rows and selection synchronously on permission and principal changes', async () => {
    const authorityKey = ref('permission-a')
    const principalId = ref<number | null>(7)
    const pending: Array<ReturnType<typeof deferred<Paimos6SessionHomeProjection>>> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionHome({
      principalId,
      authorityKey,
      projectId: ref(42),
      deepLinkedSessionId: ref(null),
      storage: storage(),
      load: () => {
        const request = deferred<Paimos6SessionHomeProjection>()
        pending.push(request)
        return request.promise
      },
      replaceSessionQuery: () => {},
    }))!
    await settle()
    pending[0].resolve(projection(42, [row('authorized-before-reset')]))
    await settle()
    home.select('authorized-before-reset')

    authorityKey.value = 'permission-b'
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()
    expect(home.state.value).toBe('loading')

    await settle()
    pending[1].resolve(projection(42, [row('authorized-after-reset')]))
    await settle()
    expect(home.sessions.value.map((session) => session.id)).toEqual(['authorized-after-reset'])

    principalId.value = null
    authorityKey.value = 'logged-out'
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()
    expect(home.state.value).toBe('idle')
    scope.stop()
  })

  it('fences an abort-ignoring late response after an authority reset', async () => {
    const authorityKey = ref('old-authority')
    const pending: Array<ReturnType<typeof deferred<Paimos6SessionHomeProjection>>> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionHome({
      principalId: ref(7),
      authorityKey,
      projectId: ref(42),
      deepLinkedSessionId: ref(null),
      storage: storage(),
      load: () => {
        const request = deferred<Paimos6SessionHomeProjection>()
        pending.push(request)
        return request.promise
      },
      replaceSessionQuery: () => {},
    }))!
    await settle()
    authorityKey.value = 'new-authority'
    await settle()

    pending[0].resolve(projection(42, [row('leaked-old-row')]))
    await settle()
    expect(home.sessions.value).toEqual([])

    pending[1].resolve(projection(42, [row('current-row')]))
    await settle()
    expect(home.sessions.value.map((session) => session.id)).toEqual(['current-row'])
    scope.stop()
  })

  it('clears synchronously and scopes the replacement request when the project changes', async () => {
    const projectId = ref<number | null>(42)
    const pending: Array<ReturnType<typeof deferred<Paimos6SessionHomeProjection>>> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionHome({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId,
      deepLinkedSessionId: ref(null),
      storage: storage(),
      load: () => {
        const request = deferred<Paimos6SessionHomeProjection>()
        pending.push(request)
        return request.promise
      },
      replaceSessionQuery: () => {},
    }))!
    await settle()
    pending[0].resolve(projection(42, [row('project-42-row')]))
    await settle()
    home.select('project-42-row')

    projectId.value = 99
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()
    await settle()
    pending[1].resolve(projection(99, [row('project-99-row')]))
    await settle()
    expect(home.sessions.value.map((session) => session.id)).toEqual(['project-99-row'])
    scope.stop()
  })

  it('holds an atomic B deep link through A clearing and validates it only against B', async () => {
    const projectId = ref<number | null>(42)
    const deepLinkedProjectId = ref<number | null>(42)
    const deepLinkedSessionId = ref<string | null>('session-a')
    const memory = storage()
    persistPaimos6Selection(memory, { principalId: 7, projectId: 42 }, 'session-a')
    const pending: Array<{
      projectId: number
      request: ReturnType<typeof deferred<Paimos6SessionHomeProjection>>
    }> = []
    const replacements: Array<string | null> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionHome({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId,
      deepLinkedProjectId,
      deepLinkedSessionId,
      storage: memory,
      load: (requestedProjectId) => {
        const request = deferred<Paimos6SessionHomeProjection>()
        pending.push({ projectId: requestedProjectId, request })
        return request.promise
      },
      replaceSessionQuery: (id) => { replacements.push(id) },
    }))!
    await settle()
    pending[0].request.resolve(projection(42, [row('session-a')]))
    await settle()
    expect(home.selectedId.value).toBe('session-a')

    void home.load(true)
    await settle()
    expect(pending[1].projectId).toBe(42)
    // Vue Router publishes the new query as one transition. The selection
    // watcher runs before the view's project watcher, so both URL refs become
    // B while the loaded projection is still A.
    deepLinkedProjectId.value = 99
    deepLinkedSessionId.value = 'session-b'
    expect(replacements).toEqual([])

    projectId.value = 99
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()
    await settle()
    expect(pending[2].projectId).toBe(99)

    pending[1].request.resolve(projection(42, [row('stale-a-row')]))
    await settle()
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()

    pending[2].request.resolve(projection(99, [row('session-b')]))
    await settle()
    expect(home.sessions.value.map((session) => session.id)).toEqual(['session-b'])
    expect(home.selectedId.value).toBe('session-b')
    expect(memory.getItem(paimos6SelectionStorageKey({ principalId: 7, projectId: 42 }))).toBe('session-a')
    expect(memory.getItem(paimos6SelectionStorageKey({ principalId: 7, projectId: 99 }))).toBe('session-b')
    scope.stop()
  })

  it('clears an invalid atomic B target without restoring A selection or storage into B', async () => {
    const projectId = ref<number | null>(42)
    const deepLinkedProjectId = ref<number | null>(42)
    const deepLinkedSessionId = ref<string | null>('session-a')
    const memory = storage()
    persistPaimos6Selection(memory, { principalId: 7, projectId: 42 }, 'session-a')
    persistPaimos6Selection(memory, { principalId: 7, projectId: 99 }, 'stale-b-memory')
    const pending: Array<ReturnType<typeof deferred<Paimos6SessionHomeProjection>>> = []
    const replacements: Array<string | null> = []
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionHome({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId,
      deepLinkedProjectId,
      deepLinkedSessionId,
      storage: memory,
      load: () => {
        const request = deferred<Paimos6SessionHomeProjection>()
        pending.push(request)
        return request.promise
      },
      replaceSessionQuery: (id) => { replacements.push(id) },
    }))!
    await settle()
    pending[0].resolve(projection(42, [row('session-a')]))
    await settle()
    expect(home.selectedId.value).toBe('session-a')

    void home.load(true)
    await settle()
    deepLinkedProjectId.value = 99
    deepLinkedSessionId.value = 'revoked-b-session'
    expect(replacements).toEqual([])
    projectId.value = 99
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()
    await settle()

    pending[1].resolve(projection(42, [row('stale-a-row')]))
    pending[2].resolve(projection(99, [row('authorized-b-session')]))
    await settle()
    expect(home.sessions.value.map((session) => session.id)).toEqual(['authorized-b-session'])
    expect(home.selectedId.value).toBeNull()
    expect(replacements[replacements.length - 1]).toBeNull()
    expect(memory.getItem(paimos6SelectionStorageKey({ principalId: 7, projectId: 42 }))).toBe('session-a')
    expect(memory.getItem(paimos6SelectionStorageKey({ principalId: 7, projectId: 99 }))).toBeNull()
    scope.stop()
  })

  it('represents exact empty and unavailable states without retaining rows', async () => {
    let fail = false
    const scope = effectScope()
    const home = scope.run(() => usePaimos6SessionHome({
      principalId: ref(7),
      authorityKey: ref('authority'),
      projectId: ref(42),
      deepLinkedSessionId: ref(null),
      storage: storage(),
      load: async () => {
        if (fail) throw new Error('unavailable')
        return projection(42, [])
      },
      replaceSessionQuery: () => {},
    }))!
    await settle()
    expect(home.state.value).toBe('empty')
    expect(home.sessions.value).toEqual([])

    fail = true
    await home.load()
    expect(home.state.value).toBe('unavailable')
    expect(home.sessions.value).toEqual([])
    expect(home.selectedId.value).toBeNull()
    scope.stop()
  })
})
