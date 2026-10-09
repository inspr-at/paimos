// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1036: the daily limit on the dial. Everything here is derived from what
// GET /agents/plan carries (settings in `daily`, read-only `daily_state`); the
// server decides who may start. This only words it, never invents headroom:
// a missing or stale reading is "not measured", never an on-pace claim, and
// never the red "at today's limit".
import { HARNESS_NAME, whenFull } from './capacity.ts'

export const DEFAULT_DAILY_POINTS = 10
export type PaceMode = 'pace' | 'everything'
export type EnteredAs = 'used' | 'left'
export type AtLimit = 'ladder' | 'wait'
export interface DailyBoost { limit_used_pct: number; entered_as: EnteredAs; until: string }
export interface DailySettings { pace: { mode: PaceMode; points_per_day: number | null }; boost_today: DailyBoost | null; at_limit: AtLimit }
export type DailyMap = Record<string, DailySettings>
export interface DailyResets { count: number; expires_at: string[] }
export interface DailyAccount {
  account_id: string; label: string; order: number
  used_pct: number | null; left_pct: number | null; start_of_day_used_pct: number | null; limit_used_pct: number | null; floor_pct: number | null
  resets_at: string | null; no_daily_limit?: boolean; read_at?: string | null; freshness: 'fresh' | 'stale' | 'expired' | 'unknown'
  resets: DailyResets | Record<string, unknown> | null; reset_policy?: string; reset_plan?: unknown
  routable?: boolean; details_redacted?: boolean; can_edit?: boolean
}
export interface DailyState {
  state: 'on_pace' | 'over_pace' | 'at_limit' | 'no_limit'
  limit_used_pct: number | null; today_points_used: number | null; today_points_allowed: number | null; over_pace_points: number | null
  active_account_id: string | null; next_on_ladder: string | null; accounts: DailyAccount[]
}
export interface DailyPlanFields { daily?: DailyMap; daily_state?: Record<string, DailyState>; daily_default_points?: number; daily_timezone?: string; daily_until?: string }

export const DEFAULT_DAILY: Readonly<DailySettings> = Object.freeze({ pace: Object.freeze({ mode: 'pace' as PaceMode, points_per_day: null }), boost_today: null, at_limit: 'ladder' as AtLimit })
/** A missing harness means pace at the person's default, no Boost and the ladder. */
export const dailyOf = (daily: DailyMap | undefined, harness: string): DailySettings => daily?.[harness] ?? { ...DEFAULT_DAILY, pace: { ...DEFAULT_DAILY.pace } }
export const cloneDaily = (daily: DailyMap | undefined): DailyMap => Object.fromEntries(Object.entries(daily ?? {}).map(([key, d]) => [key, { pace: { ...d.pace }, boost_today: d.boost_today && { ...d.boost_today }, at_limit: d.at_limit }]))
export function sameDaily(a: DailyMap | undefined, b: DailyMap | undefined): boolean {
  if (!a || !b) return !a && !b
  const keys = Object.keys(a)
  return keys.length === Object.keys(b).length && keys.every(key => !!b[key] && JSON.stringify(a[key]) === JSON.stringify(b[key]))
}

/** Percentages of an allowance window: whole numbers read as whole numbers, anything else with one decimal. */
export const pct = (n: number) => String(Math.round(n * 10) / 10)
export const points = (n: number) => `${pct(n)} percentage ${pct(n) === '1' ? 'point' : 'points'}`
const name = (harness: string) => HARNESS_NAME[harness] ?? harness

export type DailyKind = 'none' | 'unknown' | 'no_limit' | 'everything' | 'on_pace' | 'over_pace' | 'at_limit'
export interface DailyView {
  harness: string; kind: DailyKind; settings: DailySettings
  accounts: DailyAccount[]; account: DailyAccount | null
  /** Today's limit as a share of the allowance window that has been used. */
  limit: number | null
  used: number | null; left: number | null; resetsAt: string | null
  today: number | null; allowed: number | null; over: number
  /** Today's share in percentage points: the pace, or null when everything may be used. */
  share: number | null
  next: string | null
  /** The person may change today's limit on every account of the harness (live account.manage and visible usage). */
  editable: boolean
}
const finite = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n)

/**
 * One harness's daily reading. "At today's limit" needs a measurement on every
 * account at or above its limit; the server also reports at_limit when a
 * reading is merely stale (starts then wait), which the dial words as not
 * measured instead.
 */
export function dailyView(harness: string, settings: DailySettings, state: DailyState | undefined, defaultPoints = DEFAULT_DAILY_POINTS): DailyView {
  const accounts = state?.accounts ?? []
  const base: DailyView = { harness, kind: 'none', settings, accounts, account: null, limit: null, used: null, left: null, resetsAt: null, today: null, allowed: null, over: 0, share: null, next: null, editable: false }
  if (!state || !accounts.length) return base
  const account = accounts.find(a => a.account_id === state.active_account_id) ?? accounts[0]!
  const view: DailyView = { ...base, kind: 'unknown', account, next: state.next_on_ladder, editable: accounts.every(a => a.can_edit === true && !a.details_redacted) }
  if (state.state === 'no_limit') return { ...view, kind: 'no_limit' }
  // Only a fresh reading proves a limit: the server counts a stale one as at_limit too, and its numbers may be old.
  const measured = (a: DailyAccount) => finite(a.used_pct) && finite(a.limit_used_pct) && !a.details_redacted && a.freshness === 'fresh'
  const read = account.details_redacted ? null : account
  const known = read && finite(read.used_pct)
  const out: DailyView = {
    ...view,
    limit: finite(state.limit_used_pct) ? state.limit_used_pct : null,
    used: known ? read.used_pct : null, left: known && finite(read.left_pct) ? read.left_pct : known ? 100 - read.used_pct! : null,
    resetsAt: read?.resets_at ?? null,
    today: finite(state.today_points_used) ? state.today_points_used : null,
    allowed: finite(state.today_points_allowed) ? state.today_points_allowed : null,
    over: finite(state.over_pace_points) ? state.over_pace_points : 0,
    share: settings.pace.mode === 'everything' ? null : (settings.pace.points_per_day ?? defaultPoints),
  }
  if (state.state === 'at_limit') return { ...out, kind: accounts.every(measured) && accounts.every(a => a.used_pct! >= a.limit_used_pct!) ? 'at_limit' : 'unknown' }
  if (out.limit === null || out.used === null) return { ...out, kind: 'unknown' }
  // An active Boost still caps "use everything": the server enforces its ceiling, so the dial keeps that number.
  if (settings.pace.mode === 'everything' && !settings.boost_today) return { ...out, kind: 'everything' }
  return { ...out, kind: state.state === 'over_pace' && out.over > 0 ? 'over_pace' : 'on_pace' }
}

/** Over pace (gold) and at the limit (red) are the only states that keep a flag on the folded row. */
export const needsAttention = (v: DailyView) => v.kind === 'over_pace' || v.kind === 'at_limit'

/** What happens to new work at the limit, in the words of the person's choice. */
export function atLimitWords(v: DailyView, short = false): string {
  if (v.settings.at_limit === 'wait') return short ? 'new work waits' : 'new work waits until midnight'
  if (v.next) return short ? `${name(v.next)} next` : `${name(v.next)} next on the model ladder`
  return short ? 'follows the model ladder' : 'new work follows the model ladder'
}
/** On pace; under "use everything" with a Boost there is no pace, only today's limit. */
const withinWords = (v: DailyView, capital = false) => v.share === null ? (capital ? 'Within today’s limit' : 'within today’s limit') : capital ? 'On pace' : 'on pace'
/** The pace line of a harness row and of the detail; empty only where there is nothing to say. */
export function stateWords(v: DailyView, short = false): string {
  switch (v.kind) {
    case 'at_limit': return `At today’s limit · ${atLimitWords(v, short)}`
    case 'over_pace': return `${points(v.over)} over pace`
    case 'on_pace': return withinWords(v, true)
    case 'everything': return 'Uses everything before the reset'
    case 'no_limit': return 'Nothing to pace'
    case 'unknown': return 'Usage not measured right now'
    default: return 'No account to read'
  }
}
/** The row's second line after the running count. */
export function usageLine(v: DailyView): string {
  if (v.kind === 'no_limit' || v.kind === 'everything') return 'no daily limit'
  return v.limit !== null && (v.kind === 'on_pace' || v.kind === 'over_pace' || v.kind === 'at_limit') ? `≤ ${pct(v.limit)} % used today` : ''
}
/** Hover and focus text of a folded chip: the harness's own limit, then today's. */
export function chipTip(v: DailyView, limitWords: string): string {
  const head = `${name(v.harness)} · ${limitWords}`
  if (v.kind === 'at_limit') return `${head} · at today’s limit · ${atLimitWords(v, true)}`
  if (v.kind === 'on_pace' || v.kind === 'over_pace') return `${head} · ≤ ${pct(v.limit!)} % used today · ${v.kind === 'on_pace' ? withinWords(v) : `${points(v.over)} over pace`}`
  if (v.kind === 'unknown') return `${head} · usage not measured right now`
  return `${head} · ${v.kind === 'none' ? 'no account to read' : 'no daily limit'}`
}
/** The headline of the detail. */
export const detailHeadline = (v: DailyView) => v.kind === 'no_limit' || v.kind === 'everything' ? `${name(v.harness)} · no daily limit` : v.limit !== null && v.kind !== 'unknown' ? `${name(v.harness)} · daily limit: up to ${pct(v.limit)} % used` : `${name(v.harness)} · daily limit`

export interface DailyBar { mode: 'today' | 'week'; teal: number; gold: number; red: number; free: number; tick: { at: number; label: string } | null; label: string }
/**
 * Today's bar runs from zero to today's allowance: teal up to today's share, gold
 * over it, red past the limit, then free. Without a reading from today it shows
 * the week's use instead.
 */
export function dailyBar(v: DailyView): DailyBar | null {
  if (v.used === null) return null
  const week = (): DailyBar => ({ mode: 'week', teal: v.used!, gold: 0, red: 0, free: Math.max(0, 100 - v.used!), tick: null, label: `${pct(v.used!)} % of the window used` })
  if (v.today === null || v.allowed === null || v.share === null) return week()
  const scale = Math.max(v.allowed, v.today, 1), at = (n: number) => (n / scale) * 100
  const tealPts = Math.min(v.today, v.share, v.allowed), goldPts = Math.max(0, Math.min(v.today, v.allowed) - Math.min(v.today, v.share))
  const redPts = Math.max(0, v.today - v.allowed), freePts = Math.max(0, v.allowed - v.today)
  return {
    mode: 'today', teal: at(tealPts), gold: at(goldPts), red: at(redPts), free: at(freePts),
    tick: v.share < v.allowed ? { at: at(v.share), label: `pace ${pct(v.share)}` } : null,
    label: `Today: ${pct(v.today)} of ${pct(v.allowed)} percentage points used; ${pct(v.share)} is today’s share.`,
  }
}
export function todayLine(v: DailyView): string {
  if (v.today === null) return 'Today: not measured yet'
  if (v.allowed === null || v.share === null) return `Today: ${points(v.today)} used`
  return `Today: ${pct(v.today)} of ${points(v.allowed)} used · ${pct(Math.max(0, v.allowed - v.today))} left today`
}
/** "47 % used · 53 % left" in strong type, then which account, this week and when it resets. */
export function weekParts(v: DailyView, tz?: string): { strong: string; rest: string } {
  if (v.used === null) return { strong: '', rest: 'This week: not measured right now' }
  const who = v.accounts.length > 1 && v.account ? ` on ${v.account.label}` : ''
  return { strong: `${pct(v.used)} % used · ${pct(v.left ?? 100 - v.used)} % left`, rest: `${who} this week${v.resetsAt ? ` · resets ${whenFull(v.resetsAt, tz)}` : ''}` }
}

export const paceSummary = (v: DailyView) => v.settings.pace.mode === 'everything' ? 'Use everything before the reset' : `Stay on pace · ${points(v.share ?? DEFAULT_DAILY_POINTS)} a day`
export const boostSummary = (b: DailyBoost | null) => b ? `up to ${pct(b.limit_used_pct)} % used · ${pct(100 - b.limit_used_pct)} % left` : 'off'
/** The number the person typed, restated in the mode they chose. */
export const boostNumber = (b: DailyBoost, as: EnteredAs) => as === 'used' ? b.limit_used_pct : 100 - b.limit_used_pct
/** The stored form is always the used percentage, whichever way it was typed. */
export const makeBoost = (value: number, as: EnteredAs, until: string): DailyBoost => ({ limit_used_pct: as === 'used' ? value : 100 - value, entered_as: as, until })
/** Whole percentages 0 to 100; anything else is not an answer and is dropped, never clamped silently. */
export function typedPercent(input: string): number | undefined {
  const value = input.trim()
  if (value.length > 3 || !/^\d+$/.test(value)) return undefined
  const n = Number(value)
  return n >= 0 && n <= 100 ? n : undefined
}
/** Percentage points a day: whole numbers 1 to 50. */
export function typedPoints(input: string): number | undefined {
  const n = typedPercent(input)
  return n !== undefined && n >= 1 && n <= 50 ? n : undefined
}

/** Vendor-reported resets only; anything else shows nothing. */
export function resetsLine(a: DailyAccount | null, tz?: string): string {
  const r = a?.resets as Partial<DailyResets> | null | undefined
  if (!r || typeof r.count !== 'number' || !Number.isInteger(r.count) || r.count < 0) return ''
  if (!r.count) return 'No resets left'
  const first = Array.isArray(r.expires_at) ? r.expires_at.find(at => typeof at === 'string' && Number.isFinite(Date.parse(at))) : undefined
  return `${r.count} ${r.count === 1 ? 'reset' : 'resets'}${first ? ` · ${r.count === 1 ? '1 expires' : 'first expires'} ${whenFull(first, tz)}` : ''}`
}
