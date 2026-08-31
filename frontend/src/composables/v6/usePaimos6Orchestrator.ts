/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-865 — authority-fenced ordinary orchestrator projection.
 */

import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'

import {
  loadPaimos6Orchestrator,
  type Paimos6OrchestratorProjection,
} from '@/v6/orchestrator'

export type Paimos6OrchestratorState = 'idle' | 'loading' | 'ready' | 'unavailable'
export type Paimos6OrchestratorLoader = (signal?: AbortSignal) => Promise<Paimos6OrchestratorProjection>

export interface Paimos6OrchestratorOptions {
  principalId: Readonly<Ref<number | null>>
  authorityKey: Readonly<Ref<string>>
  projectKey: Readonly<Ref<string | null>>
  loader?: Paimos6OrchestratorLoader
}

export function usePaimos6Orchestrator(options: Paimos6OrchestratorOptions) {
  const projection = ref<Paimos6OrchestratorProjection | null>(null)
  const state = ref<Paimos6OrchestratorState>('idle')
  const error = ref<unknown>(null)
  const loader = options.loader ?? loadPaimos6Orchestrator
  let requestSequence = 0
  let alive = true
  let controller: AbortController | null = null

  const identityLabel = computed(() => projection.value?.displayLabel ?? 'Paimos')
  const statusText = computed(() => {
    if (state.value === 'loading') return 'orchestrator loading'
    if (state.value === 'unavailable' || state.value === 'idle') return 'orchestrator unavailable'
    return projection.value?.displayLabel === null
      ? 'orchestrator not configured'
      : 'orchestrator configured'
  })

  function boundaryIsCurrent(
    sequence: number,
    principalId: number,
    authorityKey: string,
    projectKey: string | null,
  ): boolean {
    return alive
      && sequence === requestSequence
      && options.principalId.value === principalId
      && options.authorityKey.value === authorityKey
      && options.projectKey.value === projectKey
  }

  function reload() {
    requestSequence += 1
    controller?.abort()
    controller = null
    projection.value = null
    error.value = null

    const principalId = options.principalId.value
    if (principalId === null) {
      state.value = 'idle'
      return
    }

    const sequence = requestSequence
    const authorityKey = options.authorityKey.value
    const projectKey = options.projectKey.value
    const requestController = new AbortController()
    controller = requestController
    state.value = 'loading'

    void loader(requestController.signal).then((result) => {
      if (!boundaryIsCurrent(sequence, principalId, authorityKey, projectKey)) return
      projection.value = result
      state.value = 'ready'
    }).catch((cause: unknown) => {
      if (!boundaryIsCurrent(sequence, principalId, authorityKey, projectKey)) return
      error.value = cause
      state.value = 'unavailable'
    }).finally(() => {
      if (controller === requestController) controller = null
    })
  }

  watch(
    [options.principalId, options.authorityKey, options.projectKey],
    reload,
    { flush: 'sync', immediate: true },
  )

  function dispose() {
    if (!alive) return
    alive = false
    requestSequence += 1
    controller?.abort()
    controller = null
    projection.value = null
    state.value = 'idle'
    error.value = null
  }

  onScopeDispose(dispose)

  return {
    projection,
    state,
    error,
    identityLabel,
    statusText,
    reload,
    dispose,
  }
}
