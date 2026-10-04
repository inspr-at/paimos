// SPDX-License-Identifier: AGPL-3.0-only
// AEON-540: the one start ceiling. Counts belong to the server's canonical
// owner snapshot; limits are independent ceilings, never reservations.
import { HARNESS_NAME, POOL_ORDER, type PoolView } from './capacity.ts'

export const CAP_MAX = 30
export const DEFAULT_TOTAL = 15
export type HarnessLimit = 'no_limit' | 'off' | number
export type LimitMode = 'none' | 'max' | 'off'
export interface WorkingPreference { total: number; limits: Record<string, HarnessLimit> }
export interface PlanSnapshot extends WorkingPreference {
  principal_id: string; running: Record<string, number>; running_total: number
  source: 'default' | 'legacy' | 'plan'; updated_at: string | null
}
export interface WaitingWork { harness?: string; reason?: string }
export const limitMode = (limit: HarnessLimit | undefined): LimitMode => limit === 'off' ? 'off' : typeof limit === 'number' ? 'max' : 'none'
const clamp = (n: number) => Math.max(0, Math.min(CAP_MAX, n))
export const planValue = (snapshot: WorkingPreference): WorkingPreference => ({ total: snapshot.total, limits: { ...snapshot.limits } })
export function stepTotal(plan: WorkingPreference, delta: number): WorkingPreference {
  return { ...planValue(plan), total: clamp(plan.total + delta) }
}
export function setLimit(plan: WorkingPreference, harness: string, limit: HarnessLimit): WorkingPreference {
  return { ...planValue(plan), limits: { ...plan.limits, [harness]: limit } }
}
export function stepLimit(plan: WorkingPreference, harness: string, effective: number, delta: number): WorkingPreference {
  const limit = plan.limits[harness]
  const shown = limit === 'off' ? 0 : typeof limit === 'number' ? limit : effective
  const next = clamp(shown + delta)
  return setLimit(plan, harness, next < 1 ? 'off' : next)
}
export function stepExpandedLimit(plan: WorkingPreference, harness: string, shown: number, delta: number): WorkingPreference {
  return setLimit(plan, harness, Math.max(1, clamp(shown + delta)))
}
export function effectiveLimit(total: number, running: number, room: number | null): number {
  // Unknown account room never becomes an invented zero or a claim of capacity.
  return room === null ? total : Math.min(total, running + room)
}
export function noOwnTip(harness: string, total: number): string {
  const name = HARNESS_NAME[harness] ?? harness
  return total === 0 ? `${name} has no limit of its own; with 0 at once, nothing new starts.` : `${name} has no limit of its own; the ${total} at once and ${name}'s accounts cap it.`
}
export function liveCopy(total: number, running: number, room: number | null) {
  const prefix = `${running} running`
  if (total === 0) return `${prefix} · nothing new starts`
  if (running > total) return `${prefix} · winding down to ${total}`
  if (running === total) return `${prefix} · all ${total} in use`
  if (room === 0) return `${prefix} · accounts full`
  if (room === null) return `${prefix} · account room unknown`
  return `${prefix} · room for ${Math.min(total - running, room)} more`
}

// Only measured, owned pools can establish room. An empty ownership match or
// an absent routing projection is unknown, never evidence that accounts are full.
// buildPools has already deduplicated quota aliases before these sums.
export function workingAccountRoom(pools: Pick<PoolView, 'mark' | 'rows' | 'parallelRuns'>[], known: boolean, harnesses: string[]) {
  const measured = (matching: typeof pools): number | null => known && matching.length > 0 && matching.every(p => p.rows.length > 0 && p.rows.every(r => !!r.routing))
    ? matching.reduce((n, p) => n + p.parallelRuns, 0) : null
  return {
    byHarness: Object.fromEntries(harnesses.map(h => [h, measured(pools.filter(p => p.mark === h))])),
    total: measured(pools),
  }
}
export function statusCopy(total: number, running: number, room: number | null) {
  if (total === 0) return running ? `Start nothing new: the ${running} ${running === 1 ? 'agent' : 'agents'} running finish their work.` : 'Start nothing new. Nothing is running.'
  if (running > total) return `Winding down to ${total}: no new starts until below ${total}; running agents finish.`
  if (running === total) return `All ${total} in use: the next start waits until one finishes.`
  if (room === 0) return `Room for ${total - running} more, but the accounts are full: new starts wait for room.`
  return room === null ? `Room for ${total - running} more in the total; account room is not available yet.` : `Room for ${total - running} more: starts as work and accounts allow.`
}
export function nowCopy(total: number, running: number, room: number | null): string {
  const are = running === 1 ? 'is' : 'are'
  if (total === 0) return running ? `Nothing new starts. The ${running} running ${running === 1 ? 'finishes its' : 'finish their'} work.` : 'Nothing new starts, and nothing is running.'
  if (running > total) return `${running} ${are} running, ${running - total} more than ${total} at once; they finish before anything new starts.`
  if (running === total) return `${running} ${are} running, all ${total} at once; the next start waits for one to finish.`
  if (room === null) return `${running} ${are} running; ${total - running} more would fit the ${total} at once. Account room is not available yet.`
  if (room === 0) return `${running} ${are} running; the other ${total - running} wait for room on an account.`
  if (room < total - running) return `${running} ${are} running; ${total - running} more would fit the ${total} at once, but the accounts have room for ${room}.`
  return `${running} ${are} running; ${total - running} more can start as work comes in.`
}
export function waitingCopy(plan: WorkingPreference, snapshot: PlanSnapshot, room: Record<string, number | null>, waiting: WaitingWork[] | null): string {
  if (waiting === null) return 'Waiting work is not available yet.'
  if (!waiting.length) return 'No work waiting.'
  const groups = new Map<string, number>()
  for (const item of waiting) {
    let why: string
    if (plan.total === 0) why = '0 at once, nothing new starts'
    else if (snapshot.running_total > plan.total) why = 'winding down first'
    else if (snapshot.running_total === plan.total) why = `all ${plan.total} at once are in use`
    else {
      const candidates = item.harness ? [item.harness] : Object.keys(room)
      const usable = candidates.filter(h => plan.limits[h] !== 'off' && (typeof plan.limits[h] !== 'number' || (snapshot.running[h] ?? 0) < (plan.limits[h] as number)))
      if (!usable.length && item.harness) why = plan.limits[item.harness] === 'off' ? `${HARNESS_NAME[item.harness] ?? item.harness} is off` : `${HARNESS_NAME[item.harness] ?? item.harness} is at its limit`
      else if (!usable.length && candidates.length) why = 'every harness is off or at its limit'
      else if (usable.length && usable.every(h => room[h] === 0)) why = 'room on an account'
      else why = item.reason || 'ready for a start'
    }
    groups.set(why, (groups.get(why) ?? 0) + 1)
  }
  return [...groups].map(([why, n]) => why === 'room on an account' ? `${n} waiting for room on an account` : `${n} waiting: ${why}`).join(' · ')
}
export function workingRows(plan: WorkingPreference, snapshot: PlanSnapshot, room: Record<string, number | null>, waiting: WaitingWork[] | null) {
  const known = new Set(['codex', 'claude', 'cursor', ...Object.keys(room), ...Object.keys(snapshot.running), ...Object.keys(plan.limits)])
  return POOL_ORDER.filter(h => known.has(h)).map(key => {
    const limit = plan.limits[key], mode = limitMode(limit), running = snapshot.running[key] ?? 0
    const effective = effectiveLimit(plan.total, running, room[key] ?? null)
    const shown = mode === 'off' ? 0 : typeof limit === 'number' ? limit : effective
    const count = waiting?.filter(w => w.harness === key).length ?? 0
    const tail = mode === 'off' ? (running ? `${running} running, finishing` : 'none running') : typeof limit === 'number' && running > limit ? `${running} running, ${running - limit} above, finishing` : `${running} running`
    return { key, label: HARNESS_NAME[key] ?? key, mode, limit, running, effective, shown,
      sub: tail + (count ? ` · ${count} waiting` : ''),
      words: mode === 'off' ? 'no new starts' : mode === 'none' ? `${effective ? `up to ${effective}` : 'none now'} · no own limit` : `at most ${shown}`,
      summary: mode === 'none' ? 'no own limit' : mode === 'off' ? 'off' : `at most ${shown}`,
    }
  })
}
