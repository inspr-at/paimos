// SPDX-License-Identifier: AGPL-3.0-only
// Agents working (AEON-499, the approved fragment on top of Accounts and
// computers): how many agents run now, by model or by ticket area, against the
// person's own targets. Running counts come from the live sessions; the targets
// are the person's preference. Nothing starts or stops an agent from here yet:
// Autopilot (AEON-451) is what will start work up to these targets, so the
// words never claim that it does.
import { HARNESS_NAME, POOL_ORDER } from './capacity.ts'

export type WorkingView = 'area' | 'model'
export interface WorkingPreference { cap?: number; view?: WorkingView; area?: Record<string, number>; model?: Record<string, number> }
export const CAP_MIN = 1
export const CAP_MAX = 12
/** A session holds a slot while it works, waits on a person, starts, or is throttled. */
export const RUNNING_GROUPS = ['working', 'needs', 'awaiting', 'throttled'] as const

export const AREAS = ['backend', 'frontend', 'full-stack', 'infra', 'design', 'docs'] as const
const AREA_LABEL: Record<string, string> = { backend: 'Backend', frontend: 'Frontend', 'full-stack': 'Full stack', infra: 'Infrastructure', design: 'Design', docs: 'Docs', '': 'No area set' }
const AREA_HINT: Record<string, string> = { backend: 'Server, data and APIs', frontend: 'The web app', 'full-stack': 'Both sides of a change', infra: 'Builds, deploys, hosts', design: 'UI and UX', docs: 'Docs and copy', '': 'Tickets without an area' }

export interface RunningSession { harness: string; model: string; area: string }
export interface WorkingRow {
  key: string; label: string; initial: string; sub: string
  running: number; target: number
  /** Filled dots run, rings are free target slots. */
  dots: boolean[]
  canDec: boolean; canInc: boolean
}
export interface WorkingPlan {
  /** The person's total; null until they choose one, and nothing stands in for it. */
  cap: number | null
  running: number; view: WorkingView
  unset: boolean
  /** Where a first step starts while no total is set: what runs now. */
  from: number
  canFewer: boolean; canMore: boolean
  /** Every agent of the total is assigned: no row takes another. */
  full: boolean
  /** Areas with no work and no target, offered as quiet "+ Area" choices. */
  spare: { key: string; label: string }[]
  runningLine: string
  capDots: boolean[]
  rows: WorkingRow[]
  assigned: number; flexible: number
  footLine: string
}

const clampCap = (n: number) => Math.max(CAP_MIN, Math.min(CAP_MAX, Math.round(n)))
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`
const dotsOf = (running: number, target: number) => Array.from({ length: Math.max(running, target) }, (_, i) => i < running)

/** "2 slots free", "every slot busy", "3 over your target". */
export function freeLine(cap: number, running: number): string {
  if (running < cap) return plural(cap - running, 'slot free', 'slots free')
  if (running === cap) return 'every slot busy'
  return `${running - cap} over your target`
}

/**
 * The control's words and dots. Model rows are the harnesses with accounts or
 * work; area rows are the ticket areas. Targets inside a view never add up to
 * more than the total; what is left over is flexible.
 */
export function workingPlan(input: { pref: WorkingPreference | null; sessions: RunningSession[]; harnesses: string[]; capacityKnown: boolean; roomNow: number | null }): WorkingPlan {
  const pref = input.pref ?? {}
  const running = input.sessions.length
  const unset = pref.cap === undefined
  const cap = unset ? null : clampCap(pref.cap!)
  // A step without a total starts from what runs now; a row target then fixes that total.
  const from = cap ?? running
  const limit = clampCap(from)
  const view: WorkingView = pref.view === 'model' ? 'model' : 'area'
  const targets = (view === 'area' ? pref.area : pref.model) ?? {}
  const count = (key: string) => input.sessions.filter(s => (view === 'area' ? s.area : s.harness) === key).length
  const order = (h: string) => { const i = POOL_ORDER.indexOf(h); return i < 0 ? 99 : i }
  // Areas show where work runs or a target is set; the rest wait as "+ Area" choices.
  const used = (key: string) => count(key) > 0 || (targets[key] ?? 0) > 0
  const keys = view === 'area'
    ? [...AREAS.filter(used), ...(used('') ? [''] : [])]
    : [...new Set([...input.harnesses, ...input.sessions.map(s => s.harness), ...Object.keys(targets).filter(k => (targets[k] ?? 0) > 0)])].sort((a, b) => order(a) - order(b) || a.localeCompare(b))
  const assigned = keys.reduce((sum, key) => sum + Math.max(0, targets[key] ?? 0), 0)
  const rows = keys.map(key => {
    const n = count(key)
    const target = Math.max(0, targets[key] ?? 0)
    const models = [...new Set(input.sessions.filter(s => s.harness === key && s.model).map(s => s.model))]
    const sub = view === 'area'
      ? `${n} running · ${AREA_HINT[key] ?? key}`
      : `${n} running${models.length ? ` · ${models.slice(0, 2).join(', ')}${models.length > 2 ? ` +${models.length - 2}` : ''}` : ''}`
    const label = view === 'area' ? AREA_LABEL[key] ?? key : HARNESS_NAME[key] ?? key
    return { key, label, initial: label.slice(0, 1).toUpperCase(), sub, running: n, target, dots: dotsOf(n, target), canDec: target > 0, canInc: assigned < limit }
  })
  const flexible = cap === null ? 0 : Math.max(0, cap - assigned)
  const capacity = input.roomNow !== null ? `the accounts have room for ${plural(input.roomNow, 'more agent', 'more agents')} now` : input.capacityKnown ? '' : 'no capacity readings yet, so the limits are yours, not the accounts’'
  // Without a total there is nothing to assign against: only the accounts' room is said.
  const foot = [
    cap === null ? '' : `${assigned} of ${cap} assigned`,
    flexible ? `${flexible} flexible: any ${view === 'area' ? 'area' : 'model'} takes them` : '',
    capacity,
  ].filter(Boolean).join(' · ')
  const footLine = foot.charAt(0).toUpperCase() + foot.slice(1)
  const spare = view === 'area' ? AREAS.filter(key => !used(key)).map(key => ({ key, label: AREA_LABEL[key] })) : []
  return {
    cap, running, view, unset, from,
    // Without a total, − sets one below what runs now and + one above.
    canFewer: from > CAP_MIN, canMore: from < CAP_MAX,
    full: assigned >= limit, spare,
    runningLine: cap === null ? 'no target set yet' : freeLine(cap, running),
    // Rings are free slots of a chosen total; without one only what runs is drawn.
    capDots: dotsOf(running, cap ?? 0), rows, assigned, flexible, footLine,
  }
}

/** The next preference after a step on the total; row targets above the new total shrink from the last row. */
export function stepCap(pref: WorkingPreference | null, delta: number, from: number): WorkingPreference {
  const next: WorkingPreference = { ...(pref ?? {}), cap: clampCap((pref?.cap ?? from) + delta) }
  for (const view of ['area', 'model'] as const) {
    const targets = { ...(next[view] ?? {}) }
    let over = Object.values(targets).reduce((a, b) => a + b, 0) - next.cap!
    for (const key of Object.keys(targets).reverse()) {
      if (over <= 0) break
      const cut = Math.min(over, targets[key])
      targets[key] -= cut
      over -= cut
    }
    if (next[view]) next[view] = targets
  }
  return next
}

/** One step on a row's target, kept within the total. */
export function stepRow(pref: WorkingPreference | null, view: WorkingView, key: string, delta: number, from: number): WorkingPreference {
  // A row target needs a total: without one it fixes what runs now, so the two never disagree.
  const base: WorkingPreference = { ...(pref ?? {}), cap: clampCap(pref?.cap ?? from) }
  const cap = base.cap!
  const targets = { ...(base[view] ?? {}) }
  const assigned = Object.values(targets).reduce((a, b) => a + b, 0)
  const now = targets[key] ?? 0
  const want = Math.max(0, now + delta)
  if (delta > 0 && assigned >= cap) return pref ?? base
  targets[key] = want
  return { ...base, [view]: targets }
}
