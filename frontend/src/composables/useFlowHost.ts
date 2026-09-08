/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { onBeforeUnmount, ref, watch, type Ref } from 'vue'

import {
  permissionsEpoch,
  sessionExpired,
} from '@/api/client'
import {
  getFlowHostState,
  identityBindingFrom,
  postFlowHostIntent,
  type FlowHostState,
  type FlowIntentResult,
} from '@/services/flowHost'
import { formatDisplayVersion } from '@/utils/version'

const TEN_MINUTES_MS = 10 * 60 * 1000

export function msUntilNextTenMinuteBoundary(now = Date.now()): number {
  const remainder = now % TEN_MINUTES_MS
  return remainder === 0 ? TEN_MINUTES_MS : TEN_MINUTES_MS - remainder
}

export function useFlowHost(projectId: Ref<number | null>) {
  const state = ref<FlowHostState | null>(null)
  const notice = ref('')
  const active = ref(false)
  let timer: number | null = null
  let request = 0

  function clearTimer() {
    if (timer != null) {
      window.clearTimeout(timer)
      timer = null
    }
  }

  function scheduleBoundaryRefresh() {
    clearTimer()
    if (typeof window === 'undefined' || !active.value) return
    timer = window.setTimeout(() => {
      void refresh()
    }, msUntilNextTenMinuteBoundary())
  }

  async function refresh() {
    const id = projectId.value
    const ticket = ++request
    if (sessionExpired.value || id == null) {
      state.value = null
      active.value = false
      clearTimer()
      return
    }
    const next = await getFlowHostState(id)
    if (ticket !== request) return
    if (!next) {
      state.value = null
      active.value = false
      clearTimer()
      return
    }
    next.header = {
      ...next.header,
      version: formatDisplayVersion(__APP_VERSION__),
    }
    state.value = next
    active.value = true
    scheduleBoundaryRefresh()
  }

  async function submitIntent(type: string, extra: Record<string, unknown> = {}): Promise<FlowIntentResult | null> {
    const id = projectId.value
    const current = state.value
    if (id == null || !current) return null
    try {
      const result = await postFlowHostIntent(id, type, identityBindingFrom(current), extra)
      if (result.notice) notice.value = result.notice
      if (result.error) notice.value = result.error
      return result
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Flow intent was rejected. No delivery work started.'
      notice.value = message
      await refresh()
      return { executed: false, error: message }
    }
  }

  watch(
    [projectId, permissionsEpoch, sessionExpired],
    () => {
      void refresh()
    },
    { immediate: true },
  )

  function onVisibility() {
    if (document.visibilityState === 'visible') void refresh()
  }

  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibility)
  }

  onBeforeUnmount(() => {
    request += 1
    clearTimer()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibility)
    }
  })

  return { state, active, notice, refresh, submitIntent }
}
