/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-861 — authority-fenced owner for the development session home.
 */

import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'

import type { Paimos6SessionViewModel } from '@/v6/sessionHome'
import {
  persistPaimos6Selection,
  resolvePaimos6Selection,
  type Paimos6SelectionStorage,
} from '@/v6/sessionHomeSelection'

export type Paimos6SessionHomeState = 'idle' | 'loading' | 'ready' | 'empty' | 'unavailable'

export interface Paimos6SessionHomeProjection {
  projectId: number
  sessions: Paimos6SessionViewModel[]
  totals: { sessions: number; unread: number; attention: number }
}

export interface UsePaimos6SessionHomeOptions {
  principalId: Ref<number | null>
  /** Changes for session, principal, role, or project-permission changes. */
  authorityKey: Ref<string>
  projectId: Ref<number | null>
  deepLinkedSessionId: Ref<string | null>
  load: (projectId: number, signal: AbortSignal) => Promise<Paimos6SessionHomeProjection>
  replaceSessionQuery: (id: string | null) => void | Promise<void>
  storage?: Paimos6SelectionStorage | null
}

export function usePaimos6SessionHome(options: UsePaimos6SessionHomeOptions) {
  const storage = options.storage === undefined
    ? (typeof sessionStorage === 'undefined' ? null : sessionStorage)
    : options.storage
  const state = ref<Paimos6SessionHomeState>('idle')
  const sessions = ref<Paimos6SessionViewModel[]>([])
  const totals = ref({ sessions: 0, unread: 0, attention: 0 })
  const selectedId = ref<string | null>(null)
  const selectedSession = computed(() => (
    sessions.value.find((session) => session.id === selectedId.value) ?? null
  ))
  let requestVersion = 0
  let controller: AbortController | null = null

  function scope() {
    const principalId = options.principalId.value
    const projectId = options.projectId.value
    return principalId !== null && projectId !== null ? { principalId, projectId } : null
  }

  function clearProjection(nextState: Paimos6SessionHomeState) {
    sessions.value = []
    totals.value = { sessions: 0, unread: 0, attention: 0 }
    selectedId.value = null
    state.value = nextState
  }

  function reconcileSelection(resetCurrent = false) {
    const currentScope = scope()
    if (!currentScope || !['ready', 'empty'].includes(state.value)) return
    const result = resolvePaimos6Selection({
      scope: currentScope,
      authorizedIds: sessions.value.map((session) => session.id),
      deepLinkedId: options.deepLinkedSessionId.value,
      currentId: resetCurrent ? null : selectedId.value,
      storage,
    })
    selectedId.value = result.id
    if (result.id !== null) persistPaimos6Selection(storage, currentScope, result.id)
    if (result.clearInvalidDeepLink) persistPaimos6Selection(storage, currentScope, null)
    if (result.id !== options.deepLinkedSessionId.value || result.clearInvalidDeepLink) {
      void options.replaceSessionQuery(result.id)
    }
  }

  async function load(background = false) {
    const principalId = options.principalId.value
    const projectId = options.projectId.value
    const authorityKey = options.authorityKey.value
    const version = ++requestVersion
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController

    if (principalId === null || projectId === null) {
      clearProjection('idle')
      return
    }
    if (!background) clearProjection('loading')

    try {
      const projection = await options.load(projectId, requestController.signal)
      if (version !== requestVersion
        || requestController.signal.aborted
        || authorityKey !== options.authorityKey.value
        || principalId !== options.principalId.value
        || projectId !== options.projectId.value
        || projection.projectId !== projectId) return
      sessions.value = projection.sessions
      totals.value = projection.totals
      state.value = projection.sessions.length === 0 ? 'empty' : 'ready'
      reconcileSelection()
    } catch {
      if (version !== requestVersion
        || requestController.signal.aborted
        || authorityKey !== options.authorityKey.value
        || principalId !== options.principalId.value
        || projectId !== options.projectId.value) return
      clearProjection('unavailable')
    } finally {
      if (controller === requestController) controller = null
    }
  }

  function select(id: string): boolean {
    const currentScope = scope()
    if (!currentScope || !sessions.value.some((session) => session.id === id)) return false
    selectedId.value = id
    persistPaimos6Selection(storage, currentScope, id)
    void options.replaceSessionQuery(id)
    return true
  }

  function clearSelection() {
    const currentScope = scope()
    selectedId.value = null
    if (currentScope) persistPaimos6Selection(storage, currentScope, null)
    void options.replaceSessionQuery(null)
  }

  watch(
    [options.authorityKey, options.projectId],
    () => {
      // Synchronous privacy boundary: rows and opaque selection leave memory
      // before the replacement request is even scheduled.
      requestVersion += 1
      controller?.abort()
      controller = null
      clearProjection(options.principalId.value !== null && options.projectId.value !== null ? 'loading' : 'idle')
      void load()
    },
    { immediate: true, flush: 'sync' },
  )

  watch(options.deepLinkedSessionId, (id, previous) => {
    if (id === previous || !['ready', 'empty'].includes(state.value)) return
    reconcileSelection(true)
  }, { flush: 'sync' })

  onScopeDispose(() => {
    requestVersion += 1
    controller?.abort()
  })

  return {
    state,
    sessions,
    totals,
    selectedId,
    selectedSession,
    load,
    select,
    clearSelection,
  }
}
