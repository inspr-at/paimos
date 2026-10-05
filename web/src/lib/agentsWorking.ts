// SPDX-License-Identifier: AGPL-3.0-only
// AEON-540: the one start ceiling. Counts belong to the server's canonical
// owner snapshot; limits are independent ceilings, never reservations.
import { buildPools, HARNESS_NAME, hidesCapacityLimit, POOL_ORDER, type AccountRow } from './capacity.ts'
import type { CapacityWait } from './capacityWait.ts'

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
export interface WorkingAccountRoom {
  room: Record<string, number | null>
  reasons: Record<string, string>
  total: number | null
  incomplete: boolean
  full: boolean
  noAccounts: boolean
}
const accountBlockers: Record<CapacityWait['code'], string> = {
  schedule: 'outside scheduled hours', reserve: 'kept for personal use', reading: 'not measured yet — a current reading is missing',
  vendor: 'the vendor has stopped new starts', offline: 'the computer or account check is unavailable', sign_in: 'sign-in is required',
  hold: 'on hold', approval: 'ongoing agent use needs approval', capacity: 'all start slots are occupied', allowance: 'the account allowance is exhausted',
  models: 'no model is granted for this account', state: 'agent use is paused', residency: 'no account within the allowed providers',
}
/** Start advice and usage measurements are different facts. Only the server
 * decides eligibility; neither Ready nor a missing reading invents slots. */
export function workingAccountRoom(accounts: AccountRow[], now: number, current: boolean, configured: string[] = []): WorkingAccountRoom {
  const room: WorkingAccountRoom['room'] = {}, reasons: WorkingAccountRoom['reasons'] = {}
  const pools = buildPools(accounts, now)
  // Shared-quota aliases carry no gauge of their own; the canonical row does.
  const byId = new Map(pools.flatMap(p => p.rows).map(r => [r.id, r]))
  const known = new Set(['codex', 'claude', 'cursor', ...configured, ...accounts.map(a => a.harness)])
  let incomplete = !current
  for (const harness of POOL_ORDER.filter(h => known.has(h))) {
    const matching = pools.filter(p => p.mark === harness).flatMap(p => p.rows)
    if (!current) {
      room[harness] = null
      reasons[harness] = 'not measured yet — account information is unavailable'
      continue
    }
    if (!matching.length) {
      room[harness] = 0
      reasons[harness] = 'no linked accounts'
      continue
    }
    const unknown = matching.some(a => !a.routing || a.routing.wait?.code === 'reading')
    const slots = matching.reduce((n, a) => n + (a.routing?.available_slots ?? 0), 0)
    room[harness] = unknown && slots === 0 ? null : slots
    incomplete ||= unknown
    const notes = matching.flatMap(a => {
      if (!a.routing) return ['not measured yet — start advice is unavailable']
      const notes = a.routing.wait ? [accountBlockers[a.routing.wait.code]] : []
      const gauge = (a.sameQuotaAs && byId.get(a.sameQuotaAs)) || a
      if (a.routing.wait?.code !== 'reading' && (!gauge.primary || gauge.primary.freshness !== 'fresh' || gauge.primary.reading.source === 'estimate' || gauge.awaitingReading)) {
        notes.push(hidesCapacityLimit(a.harness) ? 'quota not measured yet — the harness does not report usage' : 'quota not measured yet — a managed run must report current usage')
      }
      return notes
    })
    reasons[harness] = [...new Set(notes)].join('; ')
  }
  const slots = Object.values(room).reduce<number>((n, value) => n + (value ?? 0), 0)
  return { room, reasons, total: incomplete && slots === 0 ? null : slots, incomplete,
    full: current && accounts.length > 0 && accounts.every(a => a.routing?.wait?.code === 'capacity'),
    noAccounts: current && accounts.length === 0 }
}
export function accountRoomCopy(accounts: WorkingAccountRoom): string {
  if (accounts.noAccounts) return 'No linked accounts.'
  if (accounts.total === null) return 'Account room is not measured yet; see the reasons below.'
  if (accounts.total > 0) return `Room for ${accounts.incomplete ? 'at least ' : ''}${accounts.total} more right now.`
  return accounts.full ? 'Full right now: all start slots are occupied.' : 'No account starts are available right now.'
}
export function accountRoomDetail(accounts: WorkingAccountRoom, harness: string): string {
  const slots = accounts.room[harness], reason = accounts.reasons[harness]
  let value = ''
  if (slots === null || slots === undefined) value = reason?.startsWith('not measured yet') ? '' : 'not measured yet'
  else if (slots > 0) value = `room for ${slots} more`
  else if (!reason) value = 'no start slots available'
  return `${HARNESS_NAME[harness] ?? harness} ${[value, reason].filter(Boolean).join(' — ')}`
}
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
  return setLimit(plan, harness, stepHarnessLimit(plan.limits[harness], effective, delta))
}
// One ladder in both views: off · 1 … 30 · no own limit. A stored API zero
// remains visible until edited; it is not rewritten by an unrelated change.
export function stepHarnessLimit(limit: HarnessLimit | undefined, effective: number, delta: number): HarnessLimit {
  if (delta === 0) return limit ?? 'no_limit'
  if (limitMode(limit) === 'none') return delta > 0 ? 'no_limit' : effective <= 1 ? 'off' : clamp(effective - 1)
  if (limit === 'off') return delta > 0 ? 1 : 'off'
  const shown = limit as number
  if (delta > 0) return shown >= CAP_MAX ? 'no_limit' : clamp(shown + 1)
  return shown <= 0 ? shown : shown <= 1 ? 'off' : clamp(shown - 1)
}
export const nextLimitMode = (limit: HarnessLimit | undefined): LimitMode => ({ none: 'max', max: 'off', off: 'none' } as const)[limitMode(limit)]
export function modeLimit(limit: HarnessLimit | undefined, mode: LimitMode, remembered = 2): HarnessLimit {
  return mode === 'none' ? 'no_limit' : mode === 'off' ? 'off' : Math.max(1, clamp(typeof limit === 'number' ? limit : remembered))
}
export function typedTotal(input: string): number | undefined {
  if (input.length > 64) return undefined
  const value = input.trim()
  return /^\d+$/.test(value) ? clamp(Number(value)) : undefined
}
export function typedLimit(input: string): HarnessLimit | undefined {
  if (input.length > 64) return undefined
  if (!input.trim()) return 'no_limit'
  const value = typedTotal(input)
  return value === undefined ? undefined : value === 0 ? 'off' : value
}
export function effectiveLimit(total: number, running: number, room: number | null): number {
  // Unknown account room never becomes an invented zero or a claim of capacity.
  return room === null ? total : Math.min(total, running + room)
}
export function noOwnTip(harness: string, total: number): string {
  const name = HARNESS_NAME[harness] ?? harness
  return total === 0 ? `${name} has no limit of its own; with 0 at once, nothing new starts.` : `${name} has no limit of its own; the ${total} at once and ${name}'s accounts cap it.`
}
export function liveCopy(total: number, running: number, room: number | null, full = false) {
  const prefix = `${running} running`
  if (total === 0) return `${prefix} · nothing new starts`
  if (running > total) return `${prefix} · winding down to ${total}`
  if (running === total) return `${prefix} · all ${total} in use`
  if (room === null) return `${prefix} · account room not measured yet`
  if (room === 0) return `${prefix} · ${full ? 'accounts full' : 'no account starts available'}`
  return `${prefix} · room for ${Math.min(total - running, room)} more`
}
export function statusCopy(total: number, running: number, room: number | null, full = false) {
  if (total === 0) return running ? `Start nothing new: the ${running} ${running === 1 ? 'agent' : 'agents'} running finish their work.` : 'Start nothing new. Nothing is running.'
  if (running > total) return `Winding down to ${total}: no new starts until below ${total}; running agents finish.`
  if (running === total) return `All ${total} in use: no further starts until one finishes.`
  if (room === 0) return `Room for ${total - running} more in the total; ${full ? 'all account start slots are occupied' : 'no account starts are available right now'}.`
  return room === null ? `Room for ${total - running} more in the total; account room is not measured yet.` : `Room for ${total - running} more: starts as work and accounts allow.`
}
export function nowCopy(total: number, running: number, room: number | null): string {
  const are = running === 1 ? 'is' : 'are'
  if (total === 0) return running ? `Nothing new starts. The ${running} running ${running === 1 ? 'finishes its' : 'finish their'} work.` : 'Nothing new starts, and nothing is running.'
  if (running > total) return `${running} ${are} running, ${running - total} more than ${total} at once; they finish before anything new starts.`
  if (running === total) return `${running} ${are} running, all ${total} at once; no further starts until one finishes.`
  if (room === null) return `${running} ${are} running; ${total - running} more would fit the ${total} at once. Account room is not measured yet.`
  if (room === 0) return `${running} ${are} running; ${total - running} more would fit the ${total} at once, but no account starts are available right now.`
  if (room < total - running) return `${running} ${are} running; ${total - running} more would fit the ${total} at once, but the accounts have room for ${room}.`
  return `${running} ${are} running; ${total - running} more can start as work comes in.`
}
export function waitingCopy(plan: WorkingPreference, snapshot: PlanSnapshot, room: Record<string, number | null>, waiting: WaitingWork[] | null, reasons: Record<string, string> = {}): string {
  if (waiting === null) return 'Waiting work is not available yet.'
  if (!waiting.length) return 'No work queued.'
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
      // Only confirmed positive room is ready; unknown room is never a start.
      else if (usable.some(h => (room[h] ?? 0) > 0)) why = item.reason || 'ready for a start'
      else if (usable.length && usable.every(h => room[h] === 0)) why = item.reason || usable.map(h => reasons[h] ? `${HARNESS_NAME[h] ?? h}: ${reasons[h]}` : '').filter(Boolean).join('; ') || 'room on an account'
      else if (usable.every(h => room[h] === null || room[h] === undefined)) why = item.reason || 'account room is not measured yet'
      else why = item.reason || usable.map(h => `${HARNESS_NAME[h] ?? h}: ${reasons[h] || (room[h] === 0 ? 'no start slots available' : 'account room not measured yet')}`).join('; ')
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
      words: mode === 'off' ? 'no new starts' : mode === 'none' ? `no own limit · up to ${effective} now` : `at most ${shown}`,
      summary: mode === 'none' ? 'no own limit' : mode === 'off' ? 'off' : `at most ${shown}`,
    }
  })
}
