// SPDX-License-Identifier: AGPL-3.0-only
// Row order for the session list (AEON-468). By default rows follow their state,
// then when the session started, then its id: a heartbeat can change a row's
// state, never its place. A person may order by a column instead; that choice
// is remembered per viewer in this browser until they restore the default.
import { GROUPS, HEARTBEAT_STALE_MS, byStart as sessionsByStart, type SessionBranch, type SessionGroup } from '../../lib/agentState'
import type { SessionView } from '../../stores/agents'
import { intendedResult, sessionExecution } from './sessionRow'

export type SortKey = 'state' | 'result' | 'ticket' | 'execution' | 'heartbeat' | 'running'
export type SortDir = 'asc' | 'desc'
export interface SessionSort { key: SortKey; dir: SortDir }
export const SORT_KEYS: readonly SortKey[] = ['state', 'result', 'ticket', 'execution', 'heartbeat', 'running']
// The default order is State ascending. It is never stored: no choice means default.
export const DEFAULT_SORT: SessionSort = { key: 'state', dir: 'asc' }

type Branch = SessionBranch<SessionView>
type Value = number | string | string[] | null

const rank = (group: SessionGroup) => GROUPS.findIndex(g => g.id === group)
const ended = (b: Branch) => Date.parse(b.view.session.archived_at ?? b.view.session.stopped_at ?? b.view.session.created_at)
const byStart = (a: Branch, b: Branch) => sessionsByStart(a.view.session, b.view.session)
// History counts a removal as the end, like its own list does.
const byEnded = (a: Branch, b: Branch) => ended(b) - ended(a) || byStart(a, b)
// Within a state, live families keep their start order; ended ones lead with the latest.
const byState = (a: Branch, b: Branch) => rank(a.group) - rank(b.group) || (a.group === 'stopped' && b.group === 'stopped' ? byEnded(a, b) : byStart(a, b))
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

// What each column orders by. Heartbeat and Running are durations, like the
// columns show them: ascending is the most recent beat, the shortest run. Beats
// within the normal cadence count as equal, so on-time sessions keep their start
// order; a session moves only once it falls silent, when its state changes too.
function valueOf(key: Exclude<SortKey, 'state'>, b: Branch, now: number): Value {
  const s = b.view.session
  switch (key) {
    case 'result': return intendedResult(b.view)
    case 'ticket': return b.view.ticket?.key ?? null
    case 'execution': {
      const exec = sessionExecution(b.view)
      return exec.modelLine ? [exec.modelLine, b.view.harness, exec.account] : null
    }
    case 'heartbeat': {
      if (!s.heartbeat_at) return Number.POSITIVE_INFINITY
      const age = Math.max(0, now - Date.parse(s.heartbeat_at))
      return age < HEARTBEAT_STALE_MS ? 0 : age
    }
    case 'running': return Date.parse(s.stopped_at ?? new Date(now).toISOString()) - Date.parse(s.created_at)
  }
}

function compareValues(a: Exclude<Value, null>, b: Exclude<Value, null>): number {
  if (typeof a === 'number' && typeof b === 'number') return a === b ? 0 : a < b ? -1 : 1
  if (Array.isArray(a) && Array.isArray(b)) {
    for (let i = 0; i < Math.max(a.length, b.length); i++) {
      const c = collator.compare(a[i] ?? '', b[i] ?? '')
      if (c) return c
    }
    return 0
  }
  return collator.compare(String(a), String(b))
}

// The chosen column decides; a missing value (no ticket, no model) sits last in
// either direction. Ties fall back to start time and id, so equal rows never swap.
export function compareBy(sort: SessionSort, now: number) {
  const sign = sort.dir === 'asc' ? 1 : -1
  if (sort.key === 'state') return (a: Branch, b: Branch) => sign * (rank(a.group) - rank(b.group)) || byStart(a, b)
  const key = sort.key
  return (a: Branch, b: Branch) => {
    const va = valueOf(key, a, now), vb = valueOf(key, b, now)
    if (va === null || vb === null) return (va === null ? 1 : 0) - (vb === null ? 1 : 0) || byStart(a, b)
    return sign * compareValues(va, vb) || byStart(a, b)
  }
}

// Orders a forest without touching it. The default keeps each family's children
// as sessionForest built them (activity, start, id); a chosen column orders roots
// and, inside each family, children by the same key. History lists the latest
// ended family first unless a column is chosen.
export function orderForest(roots: Branch[], sort: SessionSort | null, now: number, history = false): Branch[] {
  if (!sort) return [...roots].sort(history ? byEnded : byState)
  const compare = compareBy(sort, now)
  const walk = (branches: Branch[]): Branch[] => branches.map(b => b.children.length ? { ...b, children: walk(b.children) } : b).sort(compare)
  return walk(roots)
}

// One click orders by a column, the next reverses it. Returning to State
// ascending is the default itself, so it clears the choice.
export function nextSort(current: SessionSort | null, key: SortKey): SessionSort | null {
  const now = current ?? DEFAULT_SORT
  const next: SessionSort = now.key === key ? { key, dir: now.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: 'asc' }
  return next.key === DEFAULT_SORT.key && next.dir === DEFAULT_SORT.dir ? null : next
}

// ---------- Per-viewer memory ----------
// Storage can be missing or throw (private windows, blocked storage): the page
// then keeps the choice for this visit only.
export const sortStorageKey = (viewer: string) => `aeon.agents.sort.${viewer}`
type SortStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>
function browserStorage(): SortStorage | undefined {
  try { return globalThis.localStorage } catch { return undefined }
}
export function readSort(viewer: string, storage: SortStorage | undefined = browserStorage()): SessionSort | null {
  if (!viewer || !storage) return null
  try {
    const raw = JSON.parse(storage.getItem(sortStorageKey(viewer)) ?? 'null') as Partial<SessionSort> | null
    if (!raw || !SORT_KEYS.includes(raw.key as SortKey) || (raw.dir !== 'asc' && raw.dir !== 'desc')) return null
    return raw.key === DEFAULT_SORT.key && raw.dir === DEFAULT_SORT.dir ? null : { key: raw.key as SortKey, dir: raw.dir }
  } catch { return null }
}
export function writeSort(viewer: string, sort: SessionSort | null, storage: SortStorage | undefined = browserStorage()) {
  if (!viewer || !storage) return
  try {
    if (sort) storage.setItem(sortStorageKey(viewer), JSON.stringify({ key: sort.key, dir: sort.dir }))
    else storage.removeItem(sortStorageKey(viewer))
  } catch { /* the choice lasts for this visit */ }
}
