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

// PAI-805 — Agent Mode data composable.
//
// Owns the snapshot lifecycle: initial load, visibility-aware polling,
// change-stream hints (refetch authoritative state, never patch from
// hints — PAI-804), offline retry with backoff, and the honest state
// machine the view renders. No demo fallback exists: when the loader
// fails the state says so.

import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref, shallowRef, watch, type Ref } from 'vue'
import { getActivePinia } from 'pinia'
import type { InjectionKey } from 'vue'

import { isSessionExpiredError } from '@/api/client'
import { useChangesStore } from '@/stores/changes'
import {
  classifyLoadError,
  fetchAgentModeSnapshot,
  type AgentModeLoadError,
  type AgentModeSnapshot,
  type AgentModeSnapshotLoader,
  type Delivery,
} from '@/services/agentMode'
import type { AgentModeSnapshotQuery } from '@/services/agentModeTransport'

export type AgentModeStatus =
  | 'idle'
  | 'loading'
  | 'ready'
  | 'empty'
  | 'offline'
  | 'forbidden'
  | 'not-found'
  | 'error'

/** Provide a custom loader (tests, DEV reference route). Production resolves
 * to `fetchAgentModeSnapshot` when nothing is provided. */
export const AGENT_MODE_LOADER_KEY: InjectionKey<AgentModeSnapshotLoader> = Symbol('agent-mode-loader')

export interface UseAgentModeDeliveriesOptions {
  loader?: AgentModeSnapshotLoader
  query?: Ref<AgentModeSnapshotQuery>
  /** Set false when query identity should apply to the next scheduled/hinted
   * refresh without creating a duplicate request immediately. */
  reloadOnQueryChange?: boolean
  /** Called when a 404 applies to a request that actually carried a selected
   * delivery. The composable clears data and retries exactly once without
   * that hint; the owner clears URL/persistence state. */
  onSelectedDeliveryRejected?: (deliveryId: string) => void
  enabled?: Ref<boolean>
  /** Poll interval while the tab is visible. 0 disables polling. */
  pollMs?: number
  /** Offline retry backoff (ms). */
  retryBaseMs?: number
  retryMaxMs?: number
  /** Subscribe to the mutation change stream as a refetch hint. */
  hints?: boolean
  now?: () => number
}

const DEFAULT_POLL_MS = 30_000
const DEFAULT_RETRY_BASE_MS = 2_000
const DEFAULT_RETRY_MAX_MS = 30_000
const HINT_DEBOUNCE_MS = 750

export function useAgentModeDeliveries(opts: UseAgentModeDeliveriesOptions = {}) {
  const loader = opts.loader ?? fetchAgentModeSnapshot
  const now = opts.now ?? (() => Date.now())
  const pollMs = opts.pollMs ?? DEFAULT_POLL_MS
  const retryBaseMs = opts.retryBaseMs ?? DEFAULT_RETRY_BASE_MS
  const retryMaxMs = opts.retryMaxMs ?? DEFAULT_RETRY_MAX_MS
  const query = opts.query ?? ref<AgentModeSnapshotQuery>({})

  const status = ref<AgentModeStatus>('idle')
  const snapshot = shallowRef<AgentModeSnapshot | null>(null)
  const error = shallowRef<AgentModeLoadError | null>(null)
  const refreshing = ref(false)
  const attempt = ref(0)
  const retryAt = ref<number | null>(null)
  const lastLoadedAt = ref<number | null>(null)
  const lastHintAt = ref<number | null>(null)

  const deliveries = computed<Delivery[]>(() => snapshot.value?.deliveries ?? [])
  /** Active rows plus the one server-authorized persistent selection outside
   * active filters. The latter is selection-only and never enters counts or
   * lane aggregates. */
  const selectableDeliveries = computed<Delivery[]>(() => {
    const active = deliveries.value
    const outside = snapshot.value?.selectedOutsideResults ?? null
    if (!outside || active.some((d) => d.id === outside.id)) return active
    return [...active, outside]
  })
  const deliveriesById = computed(() => {
    const m = new Map<string, Delivery>()
    for (const d of selectableDeliveries.value) m.set(d.id, d)
    return m
  })
  /** Browser→server clock offset (ms). 0 when the API omits server time. */
  const serverOffsetMs = computed(() => {
    const s = snapshot.value
    if (!s?.serverTime) return 0
    const serverMs = Date.parse(s.serverTime)
    return Number.isFinite(serverMs) ? serverMs - s.receivedAt : 0
  })
  const hasData = computed(() => snapshot.value !== null)
  /** Last-known data is being shown while the feed is unreachable. The
   * view must qualify it as stale and withhold exact estimates. */
  const degraded = computed(
    () => snapshot.value !== null && (status.value === 'offline' || status.value === 'error'),
  )

  let seq = 0
  let alive = true
  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let hintTimer: ReturnType<typeof setTimeout> | null = null
  let controller: AbortController | null = null
  let unsubscribeHints: (() => void) | null = null

  function clearTimers() {
    if (pollTimer !== null) clearTimeout(pollTimer)
    if (retryTimer !== null) clearTimeout(retryTimer)
    if (hintTimer !== null) clearTimeout(hintTimer)
    pollTimer = retryTimer = hintTimer = null
  }

  function enabled(): boolean {
    return opts.enabled ? opts.enabled.value : true
  }

  function schedulePoll() {
    if (pollTimer !== null) clearTimeout(pollTimer)
    pollTimer = null
    if (pollMs <= 0 || !alive) return
    pollTimer = setTimeout(() => {
      pollTimer = null
      if (typeof document !== 'undefined' && document.visibilityState !== 'visible') {
        schedulePoll()
        return
      }
      void load({ background: true })
    }, pollMs)
  }

  function scheduleRetry() {
    if (retryTimer !== null) clearTimeout(retryTimer)
    const wait = Math.min(retryMaxMs, retryBaseMs * 2 ** Math.max(0, attempt.value - 1))
    retryAt.value = now() + wait
    retryTimer = setTimeout(() => {
      retryTimer = null
      retryAt.value = null
      void load({ background: hasData.value })
    }, wait)
  }

  async function load(options: {
    background?: boolean
    queryOverride?: AgentModeSnapshotQuery
    selectionFallback?: boolean
  } = {}): Promise<void> {
    if (!alive || !enabled()) return
    const mySeq = ++seq
    controller?.abort()
    controller = typeof AbortController !== 'undefined' ? new AbortController() : null
    const requestedQuery = options.queryOverride ?? query.value
    if (options.background && hasData.value) refreshing.value = true
    else status.value = 'loading'
    try {
      const next = await loader(requestedQuery, { signal: controller?.signal })
      if (mySeq !== seq || !alive) return
      snapshot.value = next
      error.value = null
      attempt.value = 0
      retryAt.value = null
      lastLoadedAt.value = now()
      status.value = next.deliveries.length === 0 && !next.selectedOutsideResults ? 'empty' : 'ready'
      schedulePoll()
    } catch (e) {
      if (mySeq !== seq || !alive) return
      if (isSessionExpiredError(e)) {
        // The global modal owns this condition; keep the last known data.
        status.value = hasData.value ? status.value : 'error'
        return
      }
      const classified = classifyLoadError(e)
      const requestedSelection = typeof requestedQuery.selectedDelivery === 'string'
        ? requestedQuery.selectedDelivery.trim()
        : ''
      if (
        classified.kind === 'not-found' &&
        requestedSelection !== '' &&
        options.selectionFallback !== true
      ) {
        // A canonical selected-delivery miss is recoverable without retaining
        // the rejected identity. Clear first (security), notify the URL/storage
        // owner, then retry exactly once without the hint. A second 404 parks.
        snapshot.value = null
        error.value = null
        opts.onSelectedDeliveryRejected?.(requestedSelection)
        await load({
          background: false,
          queryOverride: { ...requestedQuery, selectedDelivery: null },
          selectionFallback: true,
        })
        return
      }
      error.value = classified
      if (classified.kind === 'offline') {
        attempt.value += 1
        status.value = 'offline'
        scheduleRetry()
      } else {
        if (classified.kind === 'forbidden' || classified.kind === 'not-found') {
          // Authorization changed (or the feed vanished): the previous
          // snapshot is no longer something this user may see. Drop it
          // immediately so nothing downstream can keep rendering it.
          snapshot.value = null
        }
        status.value = classified.kind
        // Non-transient failures are not retried automatically; the user
        // can retry and the poll loop stays parked.
      }
    } finally {
      if (mySeq === seq) refreshing.value = false
    }
  }

  function retryNow() {
    if (retryTimer !== null) clearTimeout(retryTimer)
    retryTimer = null
    retryAt.value = null
    void load({ background: hasData.value })
  }

  function onHint() {
    lastHintAt.value = now()
    if (hintTimer !== null) clearTimeout(hintTimer)
    hintTimer = setTimeout(() => {
      hintTimer = null
      if (status.value === 'forbidden' || status.value === 'not-found') return
      void load({ background: true })
    }, HINT_DEBOUNCE_MS)
  }

  function subscribeHints() {
    if (opts.hints === false || !getActivePinia()) return
    const changes = useChangesStore()
    unsubscribeHints = changes.subscribe(
      (event) =>
        event.subject_type === 'agent_run' ||
        event.subject_type === 'delivery' ||
        event.subject_type === 'issue' ||
        event.subject_type === 'run',
      () => onHint(),
    )
  }

  function dispose() {
    alive = false
    clearTimers()
    controller?.abort()
    unsubscribeHints?.()
    unsubscribeHints = null
  }

  if (opts.reloadOnQueryChange !== false) watch(query, () => void load(), { deep: true })

  if (getCurrentInstance()) {
    onMounted(() => {
      subscribeHints()
      void load()
    })
    onBeforeUnmount(dispose)
  }

  return {
    status,
    snapshot,
    deliveries,
    selectableDeliveries,
    deliveriesById,
    error,
    refreshing,
    attempt,
    retryAt,
    lastLoadedAt,
    lastHintAt,
    serverOffsetMs,
    hasData,
    degraded,
    load,
    retryNow,
    dispose,
  }
}
