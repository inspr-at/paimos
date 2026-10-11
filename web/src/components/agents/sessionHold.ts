// SPDX-License-Identifier: AGPL-3.0-only
// Calm enter and leave (AEON-1211, design AEON-1196 §3.2; the UI half of AEON-1195).
// A state change is a state: a row keeps its place while its badge, line 2 and time
// change. Changes in structure (a session arriving or leaving, a row moving to another
// group or parent, a new order) wait while the person points at the list, has keyboard
// focus in one of its rows or has one of its menus open. They apply 1.5 s after that
// ends, on Show now, or when the page loads. Whether a row matches the list's filter is part
// of its place: a held row keeps the match it froze with, whatever changed the live one.
// An ended session keeps its place and end state for 10 minutes, then moves to Ended at
// the next allowed moment.
import { computed, onScopeDispose, shallowRef, watch, type ComputedRef } from 'vue'

export const LINGER_MS = 10 * 60_000
export const RELEASE_MS = 1500

type Ended = { stopped_at?: string | null; archived_at?: string | null }
const endedAt = (session: Ended) => session.stopped_at && !session.archived_at ? Date.parse(session.stopped_at) : NaN
/** An ended session that still keeps its place: it ended less than 10 minutes ago and was not removed. */
export function lingers(session: Ended, now: number) {
  const at = endedAt(session)
  return Number.isFinite(at) && now - at < LINGER_MS
}
/** When a lingering session moves to Ended (at the next allowed moment from then). */
export const movesAt = (session: Ended) => endedAt(session) + LINGER_MS

/** What the list froze: every member in tree order, its structural key, its filter match and the copy it saw. */
export interface Frame<T> { order: string[]; rank: Map<string, number>; keys: Map<string, string>; matches: Map<string, boolean>; views: Map<string, T> }
export interface Structure<T> {
  id: (view: T) => string
  /** Where a view sits: its group and parent. A different key is a move. */
  key: (view: T) => string
  /** Every view's id in tree order (folds and filters aside). */
  order: (views: readonly T[]) => string[]
  /** Whether a view matches the list's filter. A different match is a move too. */
  match?: (view: T) => boolean
}

export function frameOf<T>(views: readonly T[], structure: Structure<T>): Frame<T> {
  const order = structure.order(views), match = structure.match
  return {
    order, rank: new Map(order.map((id, index) => [id, index])),
    keys: new Map(views.map(view => [structure.id(view), structure.key(view)])),
    matches: new Map(match ? views.map(view => [structure.id(view), match(view)]) : []),
    views: new Map(views.map(view => [structure.id(view), view])),
  }
}

/** Whether a live view sits where the frame placed it: the same key and the same filter match. */
const inPlace = <T>(frame: Frame<T>, structure: Structure<T>, id: string, view: T) =>
  frame.keys.get(id) === structure.key(view) && (!structure.match || frame.matches.get(id) === structure.match(view))

/** The ids outside one longest run that keeps the frame's order: the fewest rows that moved. */
export function reordered(order: readonly string[], rank: ReadonlyMap<string, number>): Set<string> {
  const ids = order.filter(id => rank.has(id))
  const tails: number[] = [], back: number[] = new Array(ids.length).fill(-1)
  for (let i = 0; i < ids.length; i++) {
    const value = rank.get(ids[i]!)!
    let lo = 0, hi = tails.length
    while (lo < hi) { const mid = (lo + hi) >> 1; if (rank.get(ids[tails[mid]!]!)! < value) lo = mid + 1; else hi = mid }
    if (lo) back[i] = tails[lo - 1]!
    tails[lo] = i
  }
  const kept = new Set<string>()
  for (let i = tails.length ? tails[tails.length - 1]! : -1; i >= 0; i = back[i]!) kept.add(ids[i]!)
  return new Set(ids.filter(id => !kept.has(id)))
}

/** The changes a frame holds back from the live list: arrivals, departures, moves and new orders. */
export function heldChanges<T>(frame: Frame<T>, live: readonly T[], structure: Structure<T>, follows: ReadonlySet<string> = new Set()): Set<string> {
  const changed = new Set<string>()
  const liveIds = new Set<string>()
  for (const view of live) {
    const id = structure.id(view)
    liveIds.add(id)
    if (follows.has(id)) continue
    if (!frame.keys.has(id) || !inPlace(frame, structure, id, view)) changed.add(id)
  }
  for (const id of frame.keys.keys()) if (!liveIds.has(id) && !follows.has(id)) changed.add(id)
  for (const id of reordered(structure.order(live), frame.rank)) if (!follows.has(id)) changed.add(id)
  return changed
}

/** The views the list lays out under a frame: members in place, a departed member as last seen. */
export function settle<T>(frame: Frame<T>, live: readonly T[], structure: Structure<T>, follows: ReadonlySet<string> = new Set()): T[] {
  const byId = new Map(live.map(view => [structure.id(view), view]))
  const out: T[] = []
  for (const [id, held] of frame.views) {
    if (follows.has(id)) continue
    const now = byId.get(id)
    // The live copy shows the newest state; a move waits, so the held copy places it.
    out.push(now && inPlace(frame, structure, id, now) ? now : held)
  }
  for (const id of follows) { const now = byId.get(id); if (now) out.push(now) }
  return out
}

/** Siblings in the frame's order; a followed newcomer goes last among them. */
export function inFrameOrder<B extends { view: unknown; children: B[] }>(branches: B[], rank: ReadonlyMap<string, number>, id: (branch: B) => string): B[] {
  const at = (branch: B) => rank.get(id(branch)) ?? Number.POSITIVE_INFINITY
  return branches.map(branch => branch.children.length ? { ...branch, children: inFrameOrder(branch.children, rank, id) } : branch).sort((a, b) => at(a) - at(b))
}

export interface SessionHold<T> {
  /** The views to lay out: the live list, or the frame while it holds. */
  views: ComputedRef<T[]>
  /** The frozen order while it holds. */
  rank: ComputedRef<ReadonlyMap<string, number> | null>
  /** How many rows wait to enter, leave or move. */
  pending: ComputedRef<number>
  /** The filter match a held row froze with; undefined while nothing holds it or the person's own change follows it. */
  matched: (id: string) => boolean | undefined
  showNow: () => void
  /** The person's own changes (a removal, a move) apply at once. */
  follow: (ids: Iterable<string>) => void
  reset: () => void
}

export interface HoldTimers { set: (run: () => void, ms: number) => unknown; clear: (handle: unknown) => void }
// Called as functions, never as methods: a browser's setTimeout refuses another `this`.
const browserTimers: HoldTimers = { set: (run, ms) => setTimeout(run, ms), clear: handle => clearTimeout(handle as ReturnType<typeof setTimeout>) }

export function useSessionHold<T>(live: () => readonly T[], structure: Structure<T>, holding: () => boolean, timers: HoldTimers = browserTimers): SessionHold<T> {
  const frame = shallowRef<Frame<T> | null>(null)
  const follows = shallowRef<ReadonlySet<string>>(new Set())
  let release: unknown
  const cancel = () => { if (release !== undefined) timers.clear(release); release = undefined }
  function reset() { cancel(); frame.value = null; follows.value = new Set() }
  function showNow() { follows.value = new Set(); frame.value = holding() ? frameOf(live(), structure) : null }
  watch(holding, held => {
    if (held) { cancel(); if (!frame.value) frame.value = frameOf(live(), structure) }
    else if (frame.value && release === undefined) release = timers.set(() => { release = undefined; if (!holding()) reset() }, RELEASE_MS)
  }, { immediate: true })
  onScopeDispose(cancel)
  return {
    views: computed(() => frame.value ? settle(frame.value, live(), structure, follows.value) : [...live()]),
    rank: computed(() => frame.value?.rank ?? null),
    pending: computed(() => frame.value ? heldChanges(frame.value, live(), structure, follows.value).size : 0),
    matched: id => frame.value && !follows.value.has(id) ? frame.value.matches.get(id) : undefined,
    showNow,
    follow(ids) { if (frame.value) follows.value = new Set([...follows.value, ...ids]) },
    reset,
  }
}
