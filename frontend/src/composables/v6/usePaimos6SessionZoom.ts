/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-864 — authority- and zoom-fenced owner of a bounded session sample.
 */

import { onScopeDispose, ref, watch, type Ref } from 'vue'

import { ApiError } from '@/api/client'
import type { Paimos6SessionViewModel } from '@/v6/sessionHome'
import {
  isCanonicalPaimos6SessionId,
  type Paimos6SessionZoomProjection,
  type Paimos6SessionZoomTotals,
  type Paimos6ZoomBand,
} from '@/v6/sessionHomeZoom'
import {
  persistPaimos6Selection,
  readPaimos6StoredSelection,
  type Paimos6SelectionScope,
  type Paimos6SelectionStorage,
} from '@/v6/sessionHomeSelection'

export type Paimos6SessionZoomState = 'idle' | 'loading' | 'ready' | 'empty' | 'unavailable'

const EMPTY_TOTALS: Paimos6SessionZoomTotals = {
  sessions: 0,
  unread: 0,
  attention_sessions: 0,
  exception_messages: 0,
  action_requests: 0,
  exception_targets: 0,
  sampled_exception_targets: 0,
}

export interface UsePaimos6SessionZoomOptions {
  principalId: Ref<number | null>
  authorityKey: Ref<string>
  projectId: Ref<number | null>
  zoom: Ref<string>
  deepLinkedProjectId?: Ref<number | null>
  deepLinkedSessionId: Ref<string | null>
  load: (
    projectId: number,
    zoom: string,
    selectedSessionId: string | null,
    signal: AbortSignal,
  ) => Promise<Paimos6SessionZoomProjection>
  replaceSessionQuery: (id: string | null) => void | Promise<void>
  storage?: Paimos6SelectionStorage | null
}

export function usePaimos6SessionZoom(options: UsePaimos6SessionZoomOptions) {
  const storage = options.storage === undefined
    ? (typeof sessionStorage === 'undefined' ? null : sessionStorage)
    : options.storage
  const state = ref<Paimos6SessionZoomState>('idle')
  const sessions = ref<Paimos6SessionViewModel[]>([])
  const totals = ref<Paimos6SessionZoomTotals>({ ...EMPTY_TOTALS })
  const band = ref<Paimos6ZoomBand>('overview')
  const sampleLimit = ref(10)
  const sampleTruncated = ref(false)
  const selectedId = ref<string | null>(null)
  const selectedSession = ref<Paimos6SessionViewModel | null>(null)
  let requestVersion = 0
  let controller: AbortController | null = null

  function scope(): Paimos6SelectionScope | null {
    const principalId = options.principalId.value
    const projectId = options.projectId.value
    return principalId !== null && projectId !== null ? { principalId, projectId } : null
  }

  function clearProjection(nextState: Paimos6SessionZoomState, clearSelection: boolean) {
    sessions.value = []
    totals.value = { ...EMPTY_TOTALS }
    sampleTruncated.value = false
    selectedSession.value = null
    if (clearSelection) selectedId.value = null
    state.value = nextState
  }

  function isCurrent(
    version: number,
    requestController: AbortController,
    authorityKey: string,
    principalId: number,
    projectId: number,
    zoom: string,
  ): boolean {
    return version === requestVersion
      && !requestController.signal.aborted
      && authorityKey === options.authorityKey.value
      && principalId === options.principalId.value
      && projectId === options.projectId.value
      && zoom === options.zoom.value
  }

  function requestedSelection(currentScope: Paimos6SelectionScope): string | null {
    if (options.deepLinkedProjectId
      && options.deepLinkedProjectId.value !== options.projectId.value) return null
    if (options.deepLinkedSessionId.value !== null) return options.deepLinkedSessionId.value
    if (selectedId.value !== null) return selectedId.value
    return readPaimos6StoredSelection(storage, currentScope)
  }

  async function load() {
    const principalId = options.principalId.value
    const projectId = options.projectId.value
    const zoom = options.zoom.value
    const authorityKey = options.authorityKey.value
    const version = ++requestVersion
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController

    if (principalId === null || projectId === null) {
      clearProjection('idle', true)
      return
    }
    if (options.deepLinkedProjectId
      && options.deepLinkedProjectId.value !== projectId) {
      clearProjection('loading', true)
      return
    }

    const currentScope = { principalId, projectId }
    const rawCandidate = requestedSelection(currentScope)
    const candidate = isCanonicalPaimos6SessionId(rawCandidate) ? rawCandidate : null
    clearProjection('loading', false)

    try {
      let projection: Paimos6SessionZoomProjection
      let rejectedCandidate = rawCandidate !== null && candidate === null
      try {
        projection = await options.load(projectId, zoom, candidate, requestController.signal)
      } catch (error) {
        if (!(candidate !== null && (error instanceof ApiError || typeof error === 'object' && error !== null)
          && (error as { status?: unknown }).status === 404)) throw error
        if (!isCurrent(version, requestController, authorityKey, principalId, projectId, zoom)) return
        projection = await options.load(projectId, zoom, null, requestController.signal)
        rejectedCandidate = true
      }

      if (!isCurrent(version, requestController, authorityKey, principalId, projectId, zoom)
        || projection.projectId !== projectId || projection.zoom !== zoom) return
      sessions.value = projection.sessions
      totals.value = projection.totals
      band.value = projection.band
      sampleLimit.value = projection.sampleLimit
      sampleTruncated.value = projection.sampleTruncated
      state.value = projection.totals.sessions === 0 ? 'empty' : 'ready'

      if (rejectedCandidate) {
        selectedId.value = null
        selectedSession.value = null
        persistPaimos6Selection(storage, currentScope, null)
        void options.replaceSessionQuery(null)
      } else if (candidate !== null) {
        selectedId.value = candidate
        selectedSession.value = projection.selectedSession
        persistPaimos6Selection(storage, currentScope, candidate)
        if (options.deepLinkedSessionId.value !== candidate) void options.replaceSessionQuery(candidate)
      } else {
        selectedId.value = null
        selectedSession.value = null
      }
    } catch {
      if (!isCurrent(version, requestController, authorityKey, principalId, projectId, zoom)) return
      clearProjection('unavailable', true)
    } finally {
      if (controller === requestController) controller = null
    }
  }

  function select(id: string): boolean {
    const currentScope = scope()
    const session = sessions.value.find((candidate) => candidate.id === id)
      ?? (selectedSession.value?.id === id ? selectedSession.value : null)
    if (!currentScope || !session) return false
    selectedId.value = id
    selectedSession.value = session
    persistPaimos6Selection(storage, currentScope, id)
    void options.replaceSessionQuery(id)
    return true
  }

  function clearSelection() {
    const currentScope = scope()
    selectedId.value = null
    selectedSession.value = null
    if (currentScope) persistPaimos6Selection(storage, currentScope, null)
    void options.replaceSessionQuery(null)
  }

  watch(
    [options.authorityKey, options.projectId, options.zoom],
    ([nextAuthority, nextProject], previous) => {
      const sameOwner = previous !== undefined
        && previous[0] === nextAuthority
        && previous[1] === nextProject
      requestVersion += 1
      controller?.abort()
      controller = null
      clearProjection(
        options.principalId.value !== null && options.projectId.value !== null ? 'loading' : 'idle',
        !sameOwner,
      )
      void load()
    },
    { immediate: true, flush: 'sync' },
  )

  watch(
    [options.deepLinkedSessionId, ...(options.deepLinkedProjectId ? [options.deepLinkedProjectId] : [])],
    () => {
      if (options.deepLinkedProjectId
        && options.deepLinkedProjectId.value !== options.projectId.value) return
      requestVersion += 1
      controller?.abort()
      controller = null
      if (options.deepLinkedSessionId.value !== null) {
        selectedId.value = isCanonicalPaimos6SessionId(options.deepLinkedSessionId.value)
          ? options.deepLinkedSessionId.value
          : null
      }
      clearProjection(
        options.principalId.value !== null && options.projectId.value !== null ? 'loading' : 'idle',
        false,
      )
      void load()
    },
    { flush: 'sync' },
  )

  onScopeDispose(() => {
    requestVersion += 1
    controller?.abort()
  })

  return {
    state,
    sessions,
    totals,
    band,
    sampleLimit,
    sampleTruncated,
    selectedId,
    selectedSession,
    load,
    select,
    clearSelection,
  }
}
