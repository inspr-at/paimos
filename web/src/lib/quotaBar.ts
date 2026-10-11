// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1172 (design AEON-1149): one picture of every limit on an account. The bar
// shows what is used, the room left today, today's limit (a tick), what is kept for
// you while you work and the account's floor; agents stop at whichever comes first.
// One pure function feeds the account list and the account panel, and later
// Capacity. It only words what the plan and the readings carry: an account without
// a reading is "Quota not reported", never a guess, and a reading that is out of
// date claims no room (the server waits for a fresh one before it starts more).
import type { DailyAccount } from './dailyLimits.ts'
import type { LimitUse } from './accountLimits.ts'
import { when, type CapacityWindow } from './capacity.ts'

/** What stops agents first: today's limit, an older limit set by hand, Keep for you, the floor, a Hold or the switch. */
export type QuotaStop = 'today' | 'rule' | 'keep' | 'floor' | 'held' | 'off'
export interface QuotaBarInput {
  /** Share of the long quota window used, 0–100; null when the account reports no quota. */
  used: number | null
  /** Today's limit as the used share where agents stop (the plan's limit_used_pct, already below the floor). */
  limit?: number | null
  /** Today's limit comes from Boost today. */
  boost?: boolean
  /** The account's floor, 0–100: agents never use the last of it. */
  floor?: number | null
  /** Keep for you now (R_eff): what agents leave you while you work; 0 outside your hours. */
  keep?: number | null
  /** An older percent limit set by hand (AEON-384): the percentage points it still allows this period. */
  ruleLeft?: number | null
  /** Agents leave the account alone: a Hold from before, or "Agents may use it" is off. */
  paused?: 'held' | 'off' | null
  /** The reading is out of date. */
  stale?: boolean
}
export interface QuotaBar {
  known: boolean
  used: number; left: number
  /** Today's limit (the tick); null when the plan does not say. */
  limit: number | null
  /** Where Keep for you, the floor and an older rule start, on the used scale; null when absent. */
  keepAt: number | null; floorAt: number | null; ruleAt: number | null
  /** The first stop and the room before it; null when today's limit is unknown or the reading is old. */
  stopAt: number | null; room: number | null; by: QuotaStop | null
  boost: boolean; stale: boolean
}

const finite = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n)
const clamp = (n: number) => Math.min(100, Math.max(0, n))
const round = (n: number) => Math.round(n * 10) / 10
/** Whole numbers read as whole numbers, anything else with one decimal. */
export const pct = (n: number) => String(round(n))

export function quotaBar(input: QuotaBarInput): QuotaBar {
  if (!finite(input.used)) return { known: false, used: 0, left: 100, limit: null, keepAt: null, floorAt: null, ruleAt: null, stopAt: null, room: null, by: null, boost: false, stale: false }
  const used = round(clamp(input.used))
  const floor = finite(input.floor) ? clamp(input.floor) : 0, keep = finite(input.keep) ? clamp(input.keep) : 0
  const floorAt = floor > 0 ? round(100 - floor) : null
  const keepAt = keep >= 0.5 ? round(100 - keep) : null
  const ruleAt = finite(input.ruleLeft) ? round(clamp(used + Math.max(0, input.ruleLeft))) : null
  const limit = finite(input.limit) ? round(Math.min(clamp(input.limit), 100 - floor)) : null
  const stale = !!input.stale
  const base = { known: true, used, left: round(100 - used), limit, keepAt, floorAt, ruleAt, boost: !!input.boost && limit !== null, stale }
  if (input.paused) return { ...base, stopAt: used, room: 0, by: input.paused }
  if (limit === null) return { ...base, stopAt: null, room: null, by: null }
  // Ties go to the stop a person set on purpose: today's limit is capped at the
  // floor, so a limit level with the floor means the floor binds.
  let stopAt = limit, by: QuotaStop = 'today'
  for (const [name, at] of [['rule', ruleAt], ['keep', keepAt], ['floor', floorAt]] as const) if (at !== null && at <= stopAt) { stopAt = at; by = name }
  return { ...base, stopAt, room: stale ? null : round(Math.max(0, stopAt - used)), by }
}

/** The points an older percent rule still allows this period; other units do not map onto the bar. */
export function ruleLeft(rule: LimitUse | null | undefined): number | null {
  if (!rule || rule.unit !== 'percent' || !finite(rule.amount) || !finite(rule.used)) return null
  return Math.max(0, rule.amount - rule.used)
}

export interface QuotaSource {
  /** The account's door in the viewer's plan (daily_state), when the plan carries it. */
  daily?: DailyAccount | null
  /** The long quota window from the capacity read: the reading outside the plan, and Keep for you now. */
  window?: CapacityWindow | null
  boost?: boolean
  rule?: LimitUse | null
  /** Sprint and Away leave nothing kept for you; a Hold or the switch leaves the account alone. */
  override?: '' | 'sprint' | 'hold' | 'away'
  off?: boolean
}
/** The bar's input from the plan, the capacity reading and the account's own state. */
export function quotaInput(s: QuotaSource): QuotaBarInput {
  const d = s.daily && !s.daily.details_redacted && finite(s.daily.used_pct) ? s.daily : null
  const w = s.window ?? null
  const used = d ? d.used_pct : w ? 100 - w.remaining_percent : null
  const free = s.override === 'sprint' || s.override === 'away'
  const keep = free || used === null ? 0 : Math.min(100 - used, w?.pacing.reserve_effective_percent ?? 0)
  return {
    used, limit: d?.limit_used_pct ?? null, boost: !!s.boost && !!d, floor: d?.floor_pct ?? null, keep, ruleLeft: ruleLeft(s.rule),
    paused: s.off ? 'off' : s.override === 'hold' ? 'held' : null,
    stale: d ? d.freshness !== 'fresh' : !!w && (w.freshness === 'stale' || w.freshness === 'expired'),
  }
}

// ---------- Drawing ----------
export interface QuotaSegments {
  used: number
  room: { at: number; width: number } | null
  keep: number; floor: number
  tick: { at: number; label: string; edge: '' | 'start' | 'end' } | null
}
export function quotaSegments(bar: QuotaBar): QuotaSegments {
  const tick = bar.limit !== null && bar.limit < 100 ? { at: bar.limit, label: `${bar.boost ? 'boost' : 'today'} ${pct(bar.limit)} %`, edge: bar.limit >= 88 ? 'end' as const : bar.limit <= 12 ? 'start' as const : '' as const } : null
  return {
    used: bar.used,
    room: bar.room ? { at: bar.used, width: bar.room } : null,
    keep: bar.keepAt === null ? 0 : round(100 - bar.keepAt),
    floor: bar.floorAt === null ? 0 : round(100 - bar.floorAt),
    tick,
  }
}
/** The bar in words, for its image role and its tip. */
export function quotaLabel(bar: QuotaBar, name: string, period = ''): string {
  if (!bar.known) return `${name}: quota not reported`
  const parts = [`${pct(bar.used)} % used${period ? ` ${period}` : ''}`]
  if (bar.limit !== null) parts.push(`${bar.boost ? 'Boost today' : 'today’s limit'} ${pct(bar.limit)} %`)
  if (bar.keepAt !== null) parts.push(`kept for you ${pct(100 - bar.keepAt)} % while you work`)
  if (bar.ruleAt !== null) parts.push(`limit set by you at ${pct(bar.ruleAt)} %`)
  if (bar.floorAt !== null) parts.push(`floor ${pct(100 - bar.floorAt)} %`)
  return `${name}: ${parts.join('; ')}.`
}

// ---------- Words ----------
/** "this week" for a weekly window, "this month" for a monthly one. */
export const periodWords = (w: CapacityWindow | null | undefined) => w?.reading.window_kind === 'weekly' ? 'this week' : w?.reading.window_kind === 'monthly' ? 'this month' : ''
const STOPPED: Record<QuotaStop, string> = {
  today: 'At today’s limit until midnight', rule: 'Agents stopped at the limit set by you', keep: 'Agents stopped · the rest is kept for you',
  floor: 'Agents stopped at the floor', held: 'Held · agents leave it alone', off: 'Off · agents leave it alone',
}
export interface QuotaCaption { text: string; tone: '' | 'warn'; icon: '' | 'gauge' | 'pause' | 'alert' }
/** The one caption under a list row's bar. A low warning says so first; warnings never stop agents. */
export function quotaCaption(bar: QuotaBar, o: { period?: string; resetsAt?: string | null; now: number; timezone?: string; low?: boolean }): QuotaCaption {
  if (!bar.known) return { text: 'Quota not reported', tone: '', icon: '' }
  const left = `${pct(bar.left)} % left`, resets = o.resetsAt ? ` · resets ${when(o.resetsAt, o.now, o.timezone)}` : ''
  const stopped = bar.room === 0 && bar.by ? STOPPED[bar.by] : ''
  if (o.low) return { text: `Low · ${left}${stopped ? ` · ${stopped.charAt(0).toLowerCase()}${stopped.slice(1)}` : resets}`, tone: 'warn', icon: 'gauge' }
  if (bar.stale) return { text: `${left} · reading out of date`, tone: '', icon: '' }
  if (stopped) return { text: stopped, tone: 'warn', icon: 'pause' }
  return { text: `${left}${o.period ? ` ${o.period}` : ''}${resets}`, tone: '', icon: '' }
}

export interface Said { text: string; strong?: boolean }
/**
 * The panel's sentence under the bar: how much more agents may use today and what
 * stops them first, or why they stopped. At the limit, new work follows the
 * harness's own choice (the model ladder, or wait until midnight).
 */
export function quotaSentence(bar: QuotaBar, o: { vendor: string; atLimit?: 'ladder' | 'wait' }): Said[] {
  if (!bar.known) return [{ text: 'This account doesn’t report its quota, so agents can’t pace it. The agent count on the dial still caps it.' }]
  if (bar.by === 'held') return [{ text: 'Agents leave it alone', strong: true }, { text: ' until the hold ends.' }]
  if (bar.by === 'off') return [{ text: 'Agents leave it alone', strong: true }, { text: ' while “Agents may use it” is off. Running work finishes.' }]
  if (bar.stale) return [{ text: 'The last reading is out of date.', strong: true }, { text: ' Agents wait for a fresh one before they use more.' }]
  if (bar.room === null || bar.stopAt === null) return [{ text: `${pct(bar.left)} % left.`, strong: true }, { text: ' Today’s limit comes from its owner’s plan.' }]
  if (bar.room > 0) {
    if (bar.stopAt >= 100) return [{ text: 'Agents may use ' }, { text: `everything left, ${pct(bar.room)} percentage points`, strong: true }, { text: ', before the reset.' }]
    const at = { today: `${bar.boost ? 'today’s Boost' : 'today’s limit'} (${pct(bar.stopAt)} %)`, rule: `the limit set by you (${pct(bar.stopAt)} %)`, keep: `what’s kept for you (from ${pct(bar.stopAt)} %)`, floor: `the floor (${pct(bar.stopAt)} %)` }[bar.by as 'today' | 'rule' | 'keep' | 'floor']
    return [{ text: 'Agents may use ' }, { text: `${pct(bar.room)} more percentage points today`, strong: true }, { text: `. They stop at ${at} first.` }]
  }
  if (bar.by === 'today') return [{ text: 'At today’s limit until midnight.', strong: true }, { text: o.atLimit === 'wait' ? ` New ${o.vendor} work waits until then.` : ` New ${o.vendor} work follows the model ladder meanwhile.` }]
  return [{ text: 'Agents have stopped using it.', strong: true }, { text: bar.by === 'keep' ? ' The rest is kept for you while you work.' : bar.by === 'rule' ? ' The limit set by you is used up for now.' : ' Only the floor is left.' }]
}
/**
 * The longest sentence of every state the panel's own controls can reach (the switch,
 * End now, Remove, a floor). The panel reserves the tallest of them, so a change in
 * Limits never moves the controls below the sentence (AEON-541).
 */
export function widestSentences(vendor: string): Said[][] {
  return [
    quotaBar({ used: 10.5, limit: 99, keep: 22.5 }), quotaBar({ used: 10.5, limit: 77.5, boost: true }), quotaBar({ used: 10.5, limit: 100 }),
    quotaBar({ used: 50.5, limit: 50.5 }), quotaBar({ used: 50.5, limit: 60.5, ruleLeft: 0 }), quotaBar({ used: 50.5, limit: 60.5, paused: 'off' }),
    quotaBar({ used: 50.5, limit: 60.5, paused: 'held' }), quotaBar({ used: 50.5, limit: 60.5, stale: true }), quotaBar({ used: 50.5 }),
  ].map(bar => quotaSentence(bar, { vendor, atLimit: 'ladder' }))
}
