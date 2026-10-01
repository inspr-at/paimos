// SPDX-License-Identifier: AGPL-3.0-only
import { computed, shallowRef, type ComputedRef, type Ref, type ShallowRef } from 'vue'
import { APIError, StaleRequestError } from './api'
import { positionOf, type ReadOrder } from './position'

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
// This also prevents an old response from overwriting a newer refresh. With an
// order (AEON-449), the answer's server position decides too: an answer below a
// write of this tab is asked for again once, one below a newer answer is dropped.
// adopt turns the answer into the value to keep at the moment it is applied, so a
// ledger judges its rows with nothing between that and the assignment. A read of rows
// that only a ledger may show (AEON-449) must adopt: what it returns is not the data.
export function usePolledData<T>(read: () => Promise<T>, initial: T, onSuccess?: (value: T) => void, options?: { order?: ReadOrder }): PolledData<T>
export function usePolledData<R, T>(read: () => Promise<R>, initial: T, onSuccess: ((value: T) => void) | undefined, options: { order?: ReadOrder; adopt: (value: R) => T }): PolledData<T>
export function usePolledData<R, T>(read: () => Promise<R>, initial: T, onSuccess?: (value: T) => void, options: { order?: ReadOrder; adopt?: (value: R) => T } = {}): PolledData<T> {
  const { order, adopt } = options
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
    flightTurn = turn
    const current: Promise<void> = (async () => {
      for (let attempt = 0; attempt < 2; attempt++) {
        const started = Date.now()
        const dropped = () => turn !== generation || Date.now() - started > 30_000 || (typeof document !== 'undefined' && document.visibilityState === 'hidden')
        try {
          const ticket = order?.begin()
          const value = await read()
          if (dropped()) return
          const verdict = order && ticket ? order.land(ticket, positionOf(value)) : 'apply'
          if (verdict === 'stale') continue
          if (verdict === 'older') return
          const adopted = adopt ? adopt(value) : value as unknown as T
          data.value = adopted
          onSuccess?.(adopted)
          status.value = refreshStatus(status.value, { ok: true, at: Date.now() })
          return
        } catch (error) {
          if (error instanceof StaleRequestError || dropped()) return
          status.value = refreshStatus(status.value, {
            ok: false, error: error instanceof APIError ? `The server answered “${error.message}” (${error.status}).` : error instanceof Error ? error.message : 'Request failed. Please try again.',
            forbidden: error instanceof APIError && error.status === 403,
          })
          return
        }
      }
    })().finally(() => { if (flight === current) flight = undefined })
    flight = current
    return current
  }
  return { data, status, stale, refresh, invalidate }
}
export interface PolledData<T> { data: Ref<T>; status: ShallowRef<RefreshStatus>; stale: ComputedRef<boolean>; refresh: () => Promise<void>; invalidate: () => void }

// The same lifecycle for every periodic read. Ticks never stack; coming back to
// a tab or network refreshes immediately, including after a suspended timer.
export function usePoller(run: () => Promise<unknown> | void, period: number, options: { enabled?: () => boolean; invalidate?: () => void } = {}) {
  let timer: ReturnType<typeof setInterval> | undefined
  let running = false
  let started = false
  let pending = false
  let generation = 0
  let lastTick = Date.now()
  const enabled = () => document.visibilityState === 'visible' && navigator.onLine !== false && (options.enabled?.() ?? true)
  function tick(force = false) {
    const at = Date.now()
    const wake = at - lastTick > period * 2
    if (wake) { restart(); return }
    lastTick = at
    if (!enabled()) return
    if (running) { if (force && started) pending = true; return }
    if (!force && document.visibilityState !== 'visible') return
    running = true
    started = false
    const turn = generation
    Promise.resolve().then(() => { if (turn === generation) { started = true; return run() } }).catch(() => { /* the data owner presents refresh errors */ }).finally(() => {
      if (turn !== generation) return
      running = false
      started = false
      if (pending) { pending = false; tick(true) }
    })
  }
  // A suspended request cannot block the first read in a new awake period.
  // Its data owner invalidates it; its eventual completion cannot run this poller.
  function restart() {
    generation++
    running = false; started = false; pending = false
    options.invalidate?.()
    lastTick = Date.now()
    tick(true)
  }
  function visibility() {
    if (document.visibilityState === 'hidden') { generation++; running = false; started = false; pending = false; options.invalidate?.(); return }
    restart()
  }
  function online() { restart() }
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
    generation++
    running = false; started = false
    document.removeEventListener('visibilitychange', visibility)
    window.removeEventListener('online', online)
    options.invalidate?.()
  }
  return { start, stop, tick, restart }
}
