// SPDX-License-Identifier: AGPL-3.0-only
import { computed, shallowRef, type Ref } from 'vue'
import { APIError, StaleRequestError } from './api'

export type PollState = 'idle' | 'ready' | 'forbidden' | 'error'
export interface RefreshStatus { state: PollState; failures: number; updatedAt: number | null; error: string }
export const initialRefreshStatus = (): RefreshStatus => ({ state: 'idle', failures: 0, updatedAt: null, error: '' })
export function refreshStatus(previous: RefreshStatus, result: { ok: true; at: number } | { ok: false; error: string; forbidden?: boolean }): RefreshStatus {
  if (result.ok) return { state: 'ready', failures: 0, updatedAt: result.at, error: '' }
  const failures = previous.failures + 1
  return {
    state: result.forbidden && previous.updatedAt === null ? 'forbidden' : previous.updatedAt === null || failures >= 3 ? 'error' : 'ready',
    failures, updatedAt: previous.updatedAt, error: result.error,
  }
}

// A read is committed only when it completed in the same visible, awake period.
// This also prevents an old response from overwriting a newer refresh.
export function usePolledData<T>(read: () => Promise<T>, initial: T, onSuccess?: (value: T) => void) {
  const data = shallowRef<T>(initial) as Ref<T>
  const status = shallowRef(initialRefreshStatus())
  const stale = computed(() => status.value.failures > 0 && status.value.updatedAt !== null)
  let flight: Promise<void> | undefined
  let flightTurn = -1
  let generation = 0
  function invalidate() { generation++ }
  // Refreshes join the read in flight only while it is current: after an
  // invalidation the next refresh reads again instead of waiting on a dropped one.
  function refresh(): Promise<void> {
    if (flight && flightTurn === generation) return flight
    const turn = generation
    const started = Date.now()
    flightTurn = turn
    const current: Promise<void> = (async () => {
      try {
        const value = await read()
        if (turn !== generation || Date.now() - started > 30_000 || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
        data.value = value
        onSuccess?.(value)
        status.value = refreshStatus(status.value, { ok: true, at: Date.now() })
      } catch (error) {
        if (error instanceof StaleRequestError || turn !== generation || Date.now() - started > 30_000 || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
        status.value = refreshStatus(status.value, {
          ok: false, error: error instanceof APIError ? `The server answered “${error.message}” (${error.status}).` : error instanceof Error ? error.message : 'Request failed. Please try again.',
          forbidden: error instanceof APIError && error.status === 403,
        })
      }
    })().finally(() => { if (flight === current) flight = undefined })
    flight = current
    return current
  }
  return { data, status, stale, refresh, invalidate }
}

// The same lifecycle for every periodic read. Ticks never stack; coming back to
// a tab or network refreshes immediately, including after a suspended timer.
export function usePoller(run: () => Promise<unknown> | void, period: number, options: { enabled?: () => boolean; invalidate?: () => void } = {}) {
  let timer: ReturnType<typeof setInterval> | undefined
  let running = false
  let pending = false
  let lastTick = Date.now()
  const enabled = () => document.visibilityState === 'visible' && navigator.onLine !== false && (options.enabled?.() ?? true)
  function tick(force = false) {
    const at = Date.now()
    const wake = at - lastTick > period * 2
    if (wake) options.invalidate?.()
    lastTick = at
    if (!enabled()) return
    if (running) { if (force || wake) pending = true; return }
    if (!force && document.visibilityState !== 'visible') return
    running = true
    Promise.resolve().then(run).catch(() => { /* the data owner presents refresh errors */ }).finally(() => {
      running = false
      if (pending) { pending = false; tick(true) }
    })
  }
  function visibility() {
    if (document.visibilityState === 'hidden') { pending = false; options.invalidate?.(); return }
    tick(true)
  }
  function online() { tick(true) }
  function start(immediate = false) {
    if (timer) return
    lastTick = Date.now()
    document.addEventListener('visibilitychange', visibility)
    window.addEventListener('online', online)
    timer = setInterval(() => tick(), period)
    if (immediate) tick(true)
  }
  function stop() {
    clearInterval(timer); timer = undefined
    pending = false
    document.removeEventListener('visibilitychange', visibility)
    window.removeEventListener('online', online)
    options.invalidate?.()
  }
  return { start, stop, tick }
}
