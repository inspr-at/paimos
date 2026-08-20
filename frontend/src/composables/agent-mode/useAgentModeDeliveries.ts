/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-804 — one authoritative snapshot lifecycle plus one dedicated SSE
// invalidation stream. SSE data is never applied as truth: registered events,
// polling and server-clock boundaries all converge through this one scheduler.

import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref, shallowRef, watch, type Ref } from 'vue'
import type { InjectionKey } from 'vue'

import { isSessionExpiredError, sessionExpired } from '@/api/client'
import {
  classifyLoadError,
  fetchAgentModeSnapshot,
  type AgentModeLoadError,
  type AgentModeSnapshot,
  type AgentModeSnapshotLoader,
  type Delivery,
} from '@/services/agentMode'
import {
  agentModeStreamBindingKey,
  openAgentModeEventStream,
  type AgentModeEventSourceFactory,
  type AgentModeEventStream,
} from '@/services/agentModeEvents'
import {
  buildSnapshotPath,
  parseAgentModeDeliveryKey,
  type AgentModeSnapshotQuery,
} from '@/services/agentModeTransport'

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
  reloadOnQueryChange?: boolean
  onSelectedDeliveryRejected?: (deliveryId: string) => void | Promise<void>
  enabled?: Ref<boolean>
  /** Authoritative convergence fallback. The production default is 30 s. */
  pollMs?: number
  retryBaseMs?: number
  retryMaxMs?: number
  /** Explicitly enable/disable the dedicated stream. A custom loader disables
   * streaming unless this or an injected factory explicitly opts back in. */
  stream?: boolean
  eventSourceFactory?: AgentModeEventSourceFactory
  /** Deprecated PAI-805 option retained only as a source-compatible alias for
   * `stream`; no generic `/api/changes` subscription remains. */
  hints?: boolean
  now?: () => number
}

const DEFAULT_POLL_MS = 30_000
const DEFAULT_RETRY_BASE_MS = 2_000
const DEFAULT_RETRY_MAX_MS = 30_000
const REFRESH_DEBOUNCE_MS = 750

interface LoadOptions {
  background?: boolean
  queryOverride?: AgentModeSnapshotQuery
  selectionFallback?: boolean
  force?: boolean
  queueIfBusy?: boolean
  resyncToken?: number
}

export function useAgentModeDeliveries(opts: UseAgentModeDeliveriesOptions = {}) {
  const loader = opts.loader ?? fetchAgentModeSnapshot
  const now = opts.now ?? (() => Date.now())
  const pollMs = opts.pollMs ?? DEFAULT_POLL_MS
  const retryBaseMs = opts.retryBaseMs ?? DEFAULT_RETRY_BASE_MS
  const retryMaxMs = opts.retryMaxMs ?? DEFAULT_RETRY_MAX_MS
  const query = opts.query ?? ref<AgentModeSnapshotQuery>({})
  const explicitStream = opts.stream ?? opts.hints
  const streamEnabled = explicitStream ?? (loader === fetchAgentModeSnapshot || opts.eventSourceFactory != null)

  const status = ref<AgentModeStatus>('idle')
  const snapshot = shallowRef<AgentModeSnapshot | null>(null)
  const error = shallowRef<AgentModeLoadError | null>(null)
  const refreshing = ref(false)
  const attempt = ref(0)
  const retryAt = ref<number | null>(null)
  const lastLoadedAt = ref<number | null>(null)
  const lastHintAt = ref<number | null>(null)
  const streamConnected = ref(false)

  const deliveries = computed<Delivery[]>(() => snapshot.value?.deliveries ?? [])
  const selectableDeliveries = computed<Delivery[]>(() => {
    const active = deliveries.value
    const outside = snapshot.value?.selectedOutsideResults ?? null
    if (!outside || active.some((delivery) => delivery.id === outside.id)) return active
    return [...active, outside]
  })
  const deliveriesById = computed(() => {
    const result = new Map<string, Delivery>()
    for (const delivery of selectableDeliveries.value) result.set(delivery.id, delivery)
    return result
  })
  const serverOffsetMs = computed(() => {
    const current = snapshot.value
    if (!current?.serverTime) return 0
    const serverMs = Date.parse(current.serverTime)
    return Number.isFinite(serverMs) ? serverMs - current.receivedAt : 0
  })
  const hasData = computed(() => snapshot.value !== null)
  const degraded = computed(
    () => snapshot.value !== null && (status.value === 'offline' || status.value === 'error'),
  )

  let requestSequence = 0
  let alive = true
  let controller: AbortController | null = null
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let refreshTimer: ReturnType<typeof setTimeout> | null = null
  let refreshDue: number | null = null
  let refreshVisibilityAware = false
  let activePromise: Promise<void> | null = null
  let activeRequestIdentity: string | null = null
  let queuedRefresh = false
  let activeBinding: string | null = null
  let eventStream: AgentModeEventStream | null = null
  let eventStreamGeneration = 0
  let activeResyncToken: number | null = null
  let resyncSequence = 0
  let signalSequence = 0
  let streamErrorProbeLatched = false

  function isEnabled(): boolean {
    return !sessionExpired.value && (opts.enabled ? opts.enabled.value : true)
  }

  function clearRetry() {
    if (retryTimer !== null) clearTimeout(retryTimer)
    retryTimer = null
    retryAt.value = null
  }

  function clearRefresh() {
    if (refreshTimer !== null) clearTimeout(refreshTimer)
    refreshTimer = null
    refreshDue = null
    refreshVisibilityAware = false
  }

  function closeStream() {
    eventStreamGeneration += 1
    eventStream?.close()
    eventStream = null
    streamConnected.value = false
  }

  function invalidateResync() {
    resyncSequence += 1
    activeResyncToken = null
  }

  function scheduleRefresh(delay: number, visibilityAware = false) {
    if (!alive || !isEnabled()) return
    // Once an authoritative request establishes an outage or parked error,
    // its retry/manual-recovery policy is the sole scheduler owner. Later SSE
    // hints must not replace exponential backoff with the 750 ms debounce or
    // turn a parked server error into an implicit retry loop. Reset bypasses
    // this scheduler and still forces a fail-closed resync directly.
    if (
      status.value === 'offline'
      || status.value === 'error'
      || status.value === 'forbidden'
      || status.value === 'not-found'
    ) return
    const safeDelay = Math.max(0, Number.isFinite(delay) ? delay : 0)
    const due = now() + safeDelay
    if (refreshTimer !== null && refreshDue !== null) {
      if (refreshDue < due || (refreshDue === due && !refreshVisibilityAware)) return
      clearRefresh()
    }
    refreshDue = due
    refreshVisibilityAware = visibilityAware
    refreshTimer = setTimeout(() => {
      const needsVisibility = refreshVisibilityAware
      refreshTimer = null
      refreshDue = null
      refreshVisibilityAware = false
      if (needsVisibility && typeof document !== 'undefined' && document.visibilityState !== 'visible') {
        scheduleBaselineRefresh()
        return
      }
      void load({ background: hasData.value, queueIfBusy: true })
    }, safeDelay)
  }

  function scheduleBaselineRefresh() {
    clearRefresh()
    const waits: Array<{ delay: number; visibilityAware: boolean }> = []
    if (pollMs > 0) waits.push({ delay: pollMs, visibilityAware: true })
    const current = snapshot.value
    const nextRefreshAt = current?.aggregates?.nextRefreshAt ?? null
    if (current?.serverTime && nextRefreshAt) {
      const boundary = Date.parse(nextRefreshAt)
      const serverNow = now() + serverOffsetMs.value
      if (Number.isFinite(boundary)) waits.push({ delay: Math.max(0, boundary - serverNow), visibilityAware: false })
    }
    waits.sort((left, right) => left.delay - right.delay)
    const earliest = waits[0]
    if (earliest) scheduleRefresh(earliest.delay, earliest.visibilityAware)
  }

  function scheduleRetry() {
    clearRetry()
    const wait = Math.min(retryMaxMs, retryBaseMs * 2 ** Math.max(0, attempt.value - 1))
    retryAt.value = now() + wait
    retryTimer = setTimeout(() => {
      retryTimer = null
      retryAt.value = null
      void load({ background: hasData.value, force: true })
    }, wait)
  }

  function ensureStream(requestedQuery: AgentModeSnapshotQuery, binding: string, cursor: string | null) {
    if (!streamEnabled || !alive || !isEnabled() || !cursor || eventStream) return
    const generation = ++eventStreamGeneration
    streamErrorProbeLatched = false
    try {
      eventStream = openAgentModeEventStream(requestedQuery, cursor, {
        onOpen() {
          if (generation !== eventStreamGeneration) return
          streamErrorProbeLatched = false
          streamConnected.value = true
        },
        onRefetch() {
          if (generation !== eventStreamGeneration) return
          lastHintAt.value = now()
          signalSequence += 1
          scheduleRefresh(REFRESH_DEBOUNCE_MS)
        },
        onCheckpoint() {
          // Native EventSource owns the Last-Event-ID update. No snapshot call.
        },
        onReset() {
          if (generation === eventStreamGeneration) void failClosedResync()
        },
        onInvalid() {
          if (generation === eventStreamGeneration) void failClosedResync()
        },
        onError(readyState) {
          if (generation !== eventStreamGeneration) return
          // CONNECTING is native reconnect and retains the sole source.
          // CLOSED cannot reconnect: retire it, then let the authoritative
          // probe reopen exactly one replacement after a successful snapshot.
          if (readyState === 2) {
            closeStream()
            // CLOSED ends the native reconnect episode. Even when an earlier
            // CONNECTING error already consumed its one probe, replacement of
            // the now-retired source needs one new authoritative probe.
            streamErrorProbeLatched = false
          }
          // One probe per stream/error episode. If that probe establishes an
          // outage, exponential retry remains the single scheduler owner;
          // repeated native errors cannot bypass it.
          if (streamErrorProbeLatched) return
          streamErrorProbeLatched = true
          signalSequence += 1
          scheduleRefresh(REFRESH_DEBOUNCE_MS)
        },
      }, opts.eventSourceFactory)
      activeBinding = binding
      streamConnected.value = true
    } catch {
      eventStream = null
      streamConnected.value = false
      scheduleBaselineRefresh()
    }
  }

  async function failClosedResync() {
    if (activeResyncToken !== null || !alive || !isEnabled()) return
    const token = ++resyncSequence
    activeResyncToken = token
    closeStream()
    clearRefresh()
    clearRetry()
    queuedRefresh = false
    controller?.abort()
    // Reset may mean audience revocation. Clear before the replacement fetch;
    // do not retain an identity that the new authorization may omit.
    snapshot.value = null
    error.value = null
    status.value = 'loading'
    try {
      await load({ background: false, force: true, resyncToken: token })
    } finally {
      if (activeResyncToken === token) activeResyncToken = null
    }
  }

  async function performLoad(options: LoadOptions, requestedQuery: AgentModeSnapshotQuery, binding: string): Promise<void> {
    if (!alive || !isEnabled()) return
    if (options.resyncToken == null && activeResyncToken !== null) invalidateResync()
    const mySequence = ++requestSequence
    const observedSignal = signalSequence
    if (activeBinding !== null && activeBinding !== binding) {
      invalidateResync()
      closeStream()
      clearRefresh()
      queuedRefresh = false
      snapshot.value = null
      error.value = null
      refreshing.value = false
    }
    controller?.abort()
    clearRetry()
    controller = typeof AbortController !== 'undefined' ? new AbortController() : null
    if (options.background && hasData.value) refreshing.value = true
    else status.value = 'loading'
    try {
      const next = await loader(requestedQuery, { signal: controller?.signal })
      if (mySequence !== requestSequence || !alive) return
      snapshot.value = next
      error.value = null
      attempt.value = 0
      clearRetry()
      lastLoadedAt.value = now()
      status.value = next.deliveries.length === 0 && !next.selectedOutsideResults ? 'empty' : 'ready'
      activeBinding = binding
      scheduleBaselineRefresh()
      ensureStream(requestedQuery, binding, next.cursor)
      if (signalSequence > observedSignal) scheduleRefresh(REFRESH_DEBOUNCE_MS)
    } catch (cause) {
      if (mySequence !== requestSequence || !alive) return
      clearRefresh()
      // A failed authoritative request establishes the only next owner
      // (retry backoff or a terminal state). Signals queued behind the failed
      // identity must not schedule a competing 750 ms bypass in the finalizer.
      queuedRefresh = false
      if (isSessionExpiredError(cause)) {
        closeStream()
        clearRetry()
        status.value = hasData.value ? status.value : 'error'
        return
      }
      const classified = classifyLoadError(cause)
      const requestedSelection = parseAgentModeDeliveryKey(requestedQuery.selectedDelivery) ?? ''
      if (
        classified.kind === 'not-found'
        && requestedSelection !== ''
        && options.selectionFallback !== true
      ) {
        // Work queued against a rejected selected-only identity cannot survive
        // and replay after the one selector-free fallback succeeds.
        queuedRefresh = false
        clearRefresh()
        clearRetry()
        snapshot.value = null
        error.value = null
        await opts.onSelectedDeliveryRejected?.(requestedSelection)
        await load({
          background: false,
          queryOverride: { ...requestedQuery, selectedDelivery: null },
          selectionFallback: true,
          force: true,
          resyncToken: options.resyncToken,
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
          snapshot.value = null
          closeStream()
          clearRetry()
        }
        status.value = classified.kind
      }
    } finally {
      if (mySequence === requestSequence) refreshing.value = false
    }
  }

  function load(options: LoadOptions = {}): Promise<void> {
    if (!alive || !isEnabled()) return Promise.resolve()
    const requestedQuery = options.queryOverride ?? query.value
    let binding: string
    let requestIdentity: string
    try {
      binding = agentModeStreamBindingKey(requestedQuery)
      requestIdentity = buildSnapshotPath(requestedQuery)
    } catch (cause) {
      requestSequence += 1
      invalidateResync()
      controller?.abort()
      clearRetry()
      clearRefresh()
      closeStream()
      queuedRefresh = false
      activePromise = null
      activeRequestIdentity = null
      snapshot.value = null
      refreshing.value = false
      const classified = classifyLoadError(cause)
      error.value = classified
      status.value = classified.kind
      return Promise.resolve()
    }
    if (!options.force && activePromise && activeRequestIdentity === requestIdentity) {
      if (options.queueIfBusy) queuedRefresh = true
      return activePromise
    }
    const promise = performLoad(options, requestedQuery, binding)
    activePromise = promise
    activeRequestIdentity = requestIdentity
    void promise.finally(() => {
      if (activePromise !== promise) return
      activePromise = null
      activeRequestIdentity = null
      if (queuedRefresh) {
        queuedRefresh = false
        scheduleRefresh(REFRESH_DEBOUNCE_MS)
      }
    })
    return promise
  }

  function retryNow() {
    clearRetry()
    void load({ background: hasData.value, force: true })
  }

  function dispose() {
    if (!alive) return
    alive = false
    invalidateResync()
    requestSequence += 1
    clearRetry()
    clearRefresh()
    queuedRefresh = false
    controller?.abort()
    closeStream()
  }

  if (opts.reloadOnQueryChange !== false) {
    watch(query, () => void load(), { deep: true })
  }
  if (opts.enabled) {
    watch(opts.enabled, (enabled) => {
      if (!enabled) {
        requestSequence += 1
        invalidateResync()
        controller?.abort()
        clearRetry()
        clearRefresh()
        queuedRefresh = false
        closeStream()
      } else {
        void load({ background: hasData.value, force: true })
      }
    })
  }
  watch(sessionExpired, (expired) => {
    if (expired) {
      requestSequence += 1
      invalidateResync()
      controller?.abort()
      clearRetry()
      clearRefresh()
      queuedRefresh = false
      closeStream()
      snapshot.value = null
      error.value = null
      refreshing.value = false
      status.value = 'idle'
      return
    }
    if (!alive || (opts.enabled && !opts.enabled.value)) return
    void load({ background: false, force: true })
  }, { flush: 'sync' })

  if (getCurrentInstance()) {
    onMounted(() => void load())
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
    streamConnected,
    load,
    retryNow,
    dispose,
  }
}
