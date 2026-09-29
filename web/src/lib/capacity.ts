// SPDX-License-Identifier: AGPL-3.0-only
// Account capacity on the Agents desk (AEON-297 API, AEON-299 UI): the observed
// windows per account, the person's pacing schedule, and the plain sentences that
// explain today's plan. Every number here comes from the server's pacing
// (GET /agent-accounts/capacity, POST …/capacity/preview); this module only picks
// windows, names states and words the plan. It never re-derives a budget.
import { api, APIError, RequestFailure, StaleRequestError } from './api.ts'

export type Pool = 'codex' | 'claude' | 'pi' | 'cursor' | 'grok'
export type OffDays = 'rest' | 'expire' | 'normal'
export type NightModel = 'daynight' | 'shifts' | 'blocks'
export type Override = '' | 'sprint' | 'hold'
export interface CapacityDay { on: boolean; start: number; end: number }
export interface CapacitySchedule {
  timezone: string
  override?: Override
  override_until?: string
  week: CapacityDay[]
  off_days: OffDays
  nights: boolean
  model: NightModel
  night: { start: number; end: number; k: number }
  shifts: { early: number; late: number; night: number; k: number[] }
  blocks: number[]
}
export interface CapacityReading {
  window_kind: '5h' | 'weekly' | 'monthly' | 'other'
  bucket?: string
  window_minutes: number
  used_percent: number
  resets_at: string
  plan?: string
  source: 'harness' | 'agentd' | 'estimate'
  read_at: string
  ordinary_usage_allowed?: boolean
}
export interface CapacityPacing {
  usable_hours: number; percent_per_hour: number; suggested_today_percent: number
  available_now_percent?: number; budget_percent?: number; used_today_percent?: number; tonight_percent?: number
  period_start?: string; period_end?: string; finish?: string | null
  allow_off?: boolean; unused?: boolean; ahead?: boolean
}
export interface CapacityWindow {
  reading: CapacityReading; starts_at: string; allowance: number; remaining_percent: number
  freshness: 'fresh' | 'aging' | 'stale' | 'expired'; usage_today_known?: boolean; pacing: CapacityPacing
}
export interface AccountCapacity {
  account_id: string; ongoing_use_approved?: boolean; schedule: CapacitySchedule; windows: CapacityWindow[]
  /** Why the last probe failed; only auth_failed is a confirmed sign-out. */
  probe_failure?: 'auth_failed' | 'unavailable'
  /** Earliest reset of the current windows, 5-hour included: where a Sprint ends. */
  limiting_reset?: string
}
export interface ScheduleOverride { scope: 'user' | 'pool' | 'account'; pool?: Pool; account_id?: string; schedule: CapacitySchedule | null; carry_overrides?: boolean }

// ---------- HTTP ----------
async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data)
  }
  return response.status === 204 ? (undefined as T) : response.json()
}
export const listCapacity = () => request<AccountCapacity[]>('/agent-accounts/capacity')
export const previewCapacity = (schedule: CapacitySchedule) => request<AccountCapacity[]>('/agent-accounts/capacity/preview', 'POST', { schedule: stripOverride(schedule) })
export const listSchedules = () => request<ScheduleOverride[]>('/agent-accounts/capacity/schedule')
export const putSchedule = (body: ScheduleOverride) => request<void>('/agent-accounts/capacity/schedule', 'PUT', body)

/**
 * A failed save whose outcome is unknown: the request may have committed and
 * only the answer was lost (no connection, timeout, a gateway error).
 */
export const uncertainFailure = (e: unknown) => e instanceof RequestFailure || e instanceof StaleRequestError || (e instanceof APIError && (e.status === 502 || e.status === 504))
/** Whether the person's saved schedule is the one that was sent. */
export function confirmsSave(entries: ScheduleOverride[], sent: CapacitySchedule) {
  const saved = entries.find(e => e.scope === 'user')?.schedule
  return !!saved && sameShape(saved, sent) && saved.timezone === sent.timezone
}

// ---------- Schedule shape ----------
export const DAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const
export const DAYS_LONG = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'] as const
export const presetWeek = (n: number): CapacityDay[] => DAYS.map((_, i) => ({ on: i < n, start: 8, end: 22 }))
/** Mirrors capacity.DefaultSchedule on the server. */
export function defaultSchedule(timezone: string): CapacitySchedule {
  return {
    timezone, week: presetWeek(5), off_days: 'expire', nights: false, model: 'daynight',
    night: { start: 22, end: 8, k: 0.6 }, shifts: { early: 6, late: 14, night: 22, k: [1, 1, 0.5] },
    blocks: Array.from({ length: 24 }, (_, h) => (h >= 8 && h < 22 ? 1 : 0.5)),
  }
}
export const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T
export function stripOverride(s: CapacitySchedule): CapacitySchedule {
  const { override: _o, override_until: _u, ...rest } = s
  return rest
}
/** Same schedule apart from Sprint/Hold and zone (the server's rule): a pool entry that only carries an override. */
export function sameShape(a: CapacitySchedule, b: CapacitySchedule) {
  const norm = (s: CapacitySchedule) => { const { timezone: _t, ...rest } = stripOverride(s); return JSON.stringify(rest) }
  return norm(a) === norm(b)
}

export function presetOf(week: CapacityDay[]): 5 | 6 | 7 | null {
  for (const n of [5, 6, 7] as const) if (week.every((d, i) => d.on === i < n && (!d.on || (d.start === 8 && d.end === 22)))) return n
  return null
}
export function daysSummary(week: CapacityDay[]): string {
  const on = week.map((d, i) => (d.on ? i : -1)).filter(i => i >= 0)
  if (!on.length) return 'no days'
  if (on.length === 7) return 'every day'
  const contiguous = on.every((v, i) => i === 0 || v === on[i - 1] + 1)
  if (contiguous && on.length > 2) return `${DAYS[on[0]]}–${DAYS[on[on.length - 1]]}`
  return on.map(i => DAYS[i]).join(', ')
}
export const ownHours = (week: CapacityDay[]) => week.some(d => d.on && (d.start !== 8 || d.end !== 22))
const pad = (n: number) => String(n).padStart(2, '0')
export const timeLabel = (v: number) => `${pad(Math.floor(v))}:${v % 1 ? '30' : '00'}`
/** Short hour for labels: 22, 08, 18:30, 24. */
export const hourLabel = (v: number) => (v % 1 ? timeLabel(v) : pad(v))
export function nightLabel(s: Pick<CapacitySchedule, 'model' | 'night'>): string {
  if (s.model === 'shifts') return '3 shifts'
  if (s.model === 'blocks') return 'Custom'
  return `${hourLabel(s.night.start)}–${hourLabel(s.night.end)}`
}
export function daysLabel(week: CapacityDay[]): { preset: 5 | 6 | 7 | null; custom: string; hint: string } {
  const preset = presetOf(week)
  const count = week.filter(d => d.on).length
  return { preset, custom: `Custom · ${count} ${count === 1 ? 'day' : 'days'}`, hint: daysSummary(week) + (preset === null && ownHours(week) ? ' · own hours' : '') }
}

/**
 * Agent pace for weekday `day` (Mon = 0) at local `hour`, for drawing the editors'
 * day ribbons. It is the schedule's shape, the same rule as the server's rate;
 * budgets and plans still come from the server.
 */
export function rateAt(s: CapacitySchedule, day: number, hour: number, allowOff = false): number {
  const active = (d: number) => s.week[(d + 7) % 7].on || allowOff || s.off_days === 'normal'
  const w = s.week[(day + 7) % 7]
  const work = active(day) && hour >= w.start && hour < w.end ? 1 : 0
  if (!s.nights) return work
  if (s.model === 'daynight') {
    const n = s.night
    let inside = hour >= n.start && hour < n.end
    let owns = active(day)
    if (n.start >= n.end) {
      inside = hour >= n.start || hour < n.end
      if (hour < n.end) owns = active(day - 1)
    }
    return inside && owns ? Math.max(work, n.k) : work
  }
  if (s.model === 'shifts') {
    const e = s.shifts.early
    const owner = hour < e ? day - 1 : day
    const h = hour < e ? hour + 24 : hour
    if (!active(owner)) return 0
    const late = s.shifts.late <= e ? s.shifts.late + 24 : s.shifts.late
    const night = s.shifts.night <= e ? s.shifts.night + 24 : s.shifts.night
    return h < late ? s.shifts.k[0] : h < night ? s.shifts.k[1] : s.shifts.k[2]
  }
  return active(day) ? s.blocks[Math.floor(hour)] ?? 0 : 0
}
export interface RibbonCell { k: number; expire: boolean }
export function dayProfile(s: CapacitySchedule, day: number, cells = 48): RibbonCell[] {
  return Array.from({ length: cells }, (_, i) => {
    const hour = (i + 0.5) * (24 / cells)
    const k = rateAt(s, day, hour)
    return { k, expire: k === 0 && s.off_days === 'expire' && !s.week[day].on && rateAt(s, day, hour, true) > 0 }
  })
}
/** Full-pace hours in one day, e.g. 17.6 for 08–22 plus a 60% night. */
export const fullPaceHours = (s: CapacitySchedule, day: number) => dayProfile(s, day, 48).reduce((sum, c) => sum + c.k, 0) / 2
export function scheduleProblem(s: CapacitySchedule, kind: 'week' | 'night'): string {
  if (kind === 'week') return s.week.some(d => d.on) ? '' : 'Pick at least one day.'
  if (!s.nights) return ''
  if (s.model === 'daynight' && s.night.start === s.night.end) return 'Night needs a start and an end.'
  if (s.model === 'shifts') {
    const { early, late, night } = s.shifts
    const L = late <= early ? late + 24 : late
    const N = night <= early ? night + 24 : night
    if (!(L < N && N < early + 24)) return 'Shifts must run early, late, night.'
  }
  return ''
}
/** Pace steps accepted by the server: off, full, or reduced 10–90%. */
export const clampRate = (v: number) => (v <= 0 ? 0 : v >= 1 ? 1 : Math.min(0.9, Math.max(0.1, Math.round(v * 10) / 10)))

// ---------- Time words ----------
const WD = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
const MO = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
interface Parts { y: number; m: number; d: number; wd: number; h: number; mi: number }
function parts(at: number, timezone?: string): Parts {
  if (!timezone) { const x = new Date(at); return { y: x.getFullYear(), m: x.getMonth(), d: x.getDate(), wd: x.getDay(), h: x.getHours(), mi: x.getMinutes() } }
  const f = new Intl.DateTimeFormat('en-GB', { timeZone: timezone, year: 'numeric', month: 'numeric', day: 'numeric', weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  const p = Object.fromEntries(f.formatToParts(new Date(at)).map(x => [x.type, x.value]))
  return { y: +p.year, m: +p.month - 1, d: +p.day, wd: WD.indexOf(p.weekday), h: +p.hour, mi: +p.minute }
}
const dayNumber = (p: Parts) => Date.UTC(p.y, p.m, p.d) / 864e5
/** "14:10", "tomorrow 18:02", "Fri 09:14", "Tue 6 Oct" — absolute, as a person reads a clock. */
export function when(iso: string | number, now: number, timezone?: string): string {
  const at = typeof iso === 'number' ? iso : Date.parse(iso)
  const p = parts(at, timezone), q = parts(now, timezone)
  const diff = dayNumber(p) - dayNumber(q)
  const hm = `${pad(p.h)}:${pad(p.mi)}`
  if (diff === 0) return hm
  if (diff === 1) return `tomorrow ${hm}`
  if (diff > 1 && diff < 6) return `${WD[p.wd]} ${hm}`
  return `${WD[p.wd]} ${p.d} ${MO[p.m]}`
}
export function whenFull(iso: string, timezone?: string): string {
  const p = parts(Date.parse(iso), timezone)
  return `${WD[p.wd]} ${p.d} ${MO[p.m]} ${pad(p.h)}:${pad(p.mi)}`
}
export function ago(iso: string, now: number): string {
  const minutes = Math.max(0, Math.round((now - Date.parse(iso)) / 60_000))
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours} h ago`
  return `${Math.round(hours / 24)} d ago`
}
/** Monday-first weekday of an instant in the schedule's zone. */
export const weekdayIn = (iso: string, timezone: string) => (parts(Date.parse(iso), timezone).wd + 6) % 7
export const pct = (v: number) => (v > 0 && v < 1 ? '<1%' : `${Math.round(v)}%`)

// ---------- Accounts and pools ----------
export type AccountState = 'live' | 'offline' | 'signin' | 'unavailable' | 'paused' | 'unread'
export interface AccountInput {
  id: string; label: string; harness: string; host: string
  state: 'available' | 'draining' | 'unavailable'; last_probe_ok?: boolean | null; plan?: string
  /** From the pairing projection of the account's computer, when it has one. */
  connectivity?: 'online' | 'offline' | 'unknown'
  /** From the capacity projection: why this account's last probe failed. */
  probeFailure?: 'auth_failed' | 'unavailable'
}
export interface AccountRow {
  id: string; name: string; host: string; harness: string; state: AccountState
  primary: CapacityWindow | null; five: CapacityWindow | null; schedule: CapacitySchedule | null; plan: string
  limitingReset: string
}
export const HARNESS_NAME: Record<string, string> = { codex: 'Codex', claude: 'Claude', grok: 'Grok', cursor: 'Cursor', pi: 'Pi' }
export const POOL_ORDER = ['codex', 'claude', 'grok', 'cursor', 'pi']
/** The vendor's own sign-in command, shown to copy. Aeon never takes the password. */
export const LOGIN_COMMAND: Record<string, string> = { codex: 'codex login', claude: 'claude /login', cursor: 'cursor-agent login' }

const KIND_RANK: Record<string, number> = { monthly: 0, weekly: 1, other: 2, '5h': 3 }
/** The long window is the bar; the 5-hour window, when there is another, is the small line under it. */
export function pickWindows(windows: CapacityWindow[]): { primary: CapacityWindow | null; five: CapacityWindow | null } {
  const general = windows.filter(w => !w.reading.bucket)
  const pool = general.length ? general : windows
  const sorted = [...pool].sort((a, b) => KIND_RANK[a.reading.window_kind] - KIND_RANK[b.reading.window_kind] || a.remaining_percent - b.remaining_percent)
  const primary = sorted[0] ?? null
  const five = primary && primary.reading.window_kind !== '5h' ? sorted.find(w => w.reading.window_kind === '5h') ?? null : null
  return { primary, five }
}
export function accountState(a: AccountInput, hasReading: boolean): AccountState {
  // Only a sign-out the vendor's status command confirmed for this account asks
  // for a sign-in; any other failed check is a quiet "reading unavailable".
  if (a.last_probe_ok === false && a.probeFailure === 'auth_failed') return 'signin'
  if (a.connectivity === 'offline') return 'offline'
  if (a.last_probe_ok === false || a.state === 'unavailable') return 'unavailable'
  if (a.state === 'draining') return 'paused'
  return hasReading ? 'live' : 'unread'
}
export function buildRows(accounts: AccountInput[], capacity: AccountCapacity[]): AccountRow[] {
  const byId = new Map(capacity.map(c => [c.account_id, c]))
  return accounts.map(a => {
    const cap = byId.get(a.id)
    const { primary, five } = pickWindows(cap?.windows ?? [])
    const state = accountState({ ...a, probeFailure: cap?.probe_failure ?? a.probeFailure }, !!primary)
    return { id: a.id, name: a.label, host: a.host, harness: a.harness, state, primary, five, schedule: cap?.schedule ?? null, plan: primary?.reading.plan || a.plan || '', limitingReset: cap?.limiting_reset ?? '' }
  })
}
export interface PoolView {
  id: string; name: string; plan: string; rows: AccountRow[]; override: Override; overrideUntil: string
  /** Where a Sprint on this pool would end: the server's earliest limiting reset in the pool. */
  sprintEnd: string
}
/** The override in force now: a Sprint ends at its reset. */
export function activeOverride(s: CapacitySchedule | null, now: number): Override {
  if (!s?.override) return ''
  if (s.override === 'sprint' && s.override_until && Date.parse(s.override_until) <= now) return ''
  return s.override
}
function windowWords(rows: AccountRow[]): string {
  const kinds = new Set(rows.flatMap(r => [r.primary?.reading.window_kind, r.five?.reading.window_kind]).filter(Boolean))
  const long = kinds.has('monthly') ? 'monthly' : kinds.has('weekly') ? 'weekly' : ''
  if (kinds.has('5h') && long) return `5-hour + ${long}`
  return long || (kinds.has('5h') ? '5-hour' : '')
}
export function buildPools(rows: AccountRow[], now: number): PoolView[] {
  const groups = new Map<string, AccountRow[]>()
  for (const row of rows) groups.set(row.harness, [...(groups.get(row.harness) ?? []), row])
  const reset = (r: AccountRow) => (r.primary ? Date.parse(r.primary.reading.resets_at) : Infinity)
  const order = (h: string) => { const i = POOL_ORDER.indexOf(h); return i < 0 ? 99 : i }
  return [...groups.entries()].sort(([a], [b]) => order(a) - order(b) || a.localeCompare(b)).map(([id, list]) => {
    // Routing order: live accounts by soonest reset, then the rest.
    const sorted = [...list].sort((x, y) => (x.state === 'live' ? 0 : 1) - (y.state === 'live' ? 0 : 1) || reset(x) - reset(y) || x.name.localeCompare(y.name))
    const plans = [...new Set(sorted.map(r => r.plan).filter(Boolean))]
    const plan = [plans.length === 1 ? plans[0] : '', windowWords(sorted)].filter(Boolean).join(' · ')
    const schedule = sorted.find(r => r.schedule)?.schedule ?? null
    const limits = sorted.map(r => r.limitingReset).filter(Boolean).sort((x, y) => Date.parse(x) - Date.parse(y))
    return { id, name: HARNESS_NAME[id] ?? id, plan, rows: sorted, override: activeOverride(schedule, now), overrideUntil: schedule?.override_until ?? '', sprintEnd: limits[0] ?? '' }
  })
}

// ---------- The plan per account ----------
export interface AccountPlan {
  left: number; used: number; budget: number; night: number; leftAtStart: number
  reset: string; finish: string | null; resetToday: boolean; dayOff: boolean; expiring: boolean; unused: boolean
  fresh: boolean; ahead: boolean; override: Override
}
export function accountPlan(row: AccountRow, now: number): AccountPlan | null {
  const w = row.primary
  if (!w) return null
  const p = w.pacing
  const s = row.schedule
  const left = w.remaining_percent
  const used = p.used_today_percent ?? 0
  const budget = p.budget_percent ?? used + p.suggested_today_percent
  const override = activeOverride(s, now)
  const dayOff = !!(s && p.period_start && !(s.week[weekdayIn(p.period_start, s.timezone)]?.on || s.off_days === 'normal'))
  return {
    left, used, budget, night: p.tonight_percent ?? 0, leftAtStart: left + used, reset: w.reading.resets_at, finish: p.finish ?? null,
    resetToday: !!p.period_end && Date.parse(w.reading.resets_at) <= Date.parse(p.period_end), dayOff, expiring: !!p.allow_off, unused: !!p.unused,
    fresh: !!p.period_start && Date.parse(w.starts_at) > Date.parse(p.period_start),
    ahead: row.state === 'live' && !override && !dayOff && (p.ahead ?? used > budget + 0.5), override,
  }
}
export interface Gauge { later: number; today: number; spent: number; tick: number | null; frozen: boolean }
/** What is left, today's share at its end, today's spend as a hatch, and where to stop tonight. */
export function gauge(row: AccountRow, plan: AccountPlan | null): Gauge {
  if (!plan) return { later: 0, today: 0, spent: 0, tick: null, frozen: row.state !== 'live' }
  const frozen = row.state !== 'live' || row.primary?.freshness === 'stale' || row.primary?.freshness === 'expired'
  const L = Math.max(0, Math.min(100, plan.left))
  const spent = Math.max(0, Math.min(plan.used, 100 - L))
  if (row.state !== 'live' || plan.override === 'hold' || (plan.dayOff && !plan.expiring)) return { later: L, today: 0, spent, tick: null, frozen }
  const today = Math.min(Math.max(0, plan.budget - plan.used), L)
  return { later: L - today, today, spent, tick: Math.max(0, Math.min(100, plan.leftAtStart - plan.budget)), frozen }
}
export type TodayCell =
  | { kind: 'quiet'; text: string }
  | { kind: 'signin'; command: string }
  | { kind: 'sprint'; text: string }
  | { kind: 'ahead'; used: string; plan: string }
  | { kind: 'share'; value: string; tip: string }
export function todayCell(row: AccountRow, plan: AccountPlan | null): TodayCell {
  if (row.state === 'offline') return { kind: 'quiet', text: `waits for ${row.host}` }
  if (row.state === 'signin') return { kind: 'signin', command: LOGIN_COMMAND[row.harness] ?? '' }
  if (row.state === 'paused') return { kind: 'quiet', text: 'paused' }
  if (row.state === 'unavailable') return { kind: 'quiet', text: 'reading unavailable' }
  if (!plan) return { kind: 'quiet', text: 'no reading yet' }
  if (plan.override === 'hold') return { kind: 'quiet', text: 'on hold' }
  if (plan.override === 'sprint') return { kind: 'sprint', text: `all ${pct(plan.left)}` }
  if (plan.dayOff && !plan.expiring) return { kind: 'quiet', text: 'day off' }
  if (plan.ahead) return { kind: 'ahead', used: pct(plan.used), plan: `~${pct(plan.budget)}` }
  const rest = Math.max(0, plan.budget - plan.used)
  return { kind: 'share', value: `~${pct(plan.budget)}`, tip: `Plan for today ~${pct(plan.budget)}: ${pct(plan.used)} used, ${pct(rest)} to go${plan.night >= 0.5 ? `; ~${pct(plan.night)} of it tonight` : ''}` }
}
/** Who measured it, and how old it is. Empty when there is no reading yet. */
export function sourceLine(row: AccountRow, now: number): string {
  const w = row.primary
  if (row.state === 'signin') return w ? `Sign-in expired · last read ${ago(w.reading.read_at, now)}` : 'Sign-in expired'
  // The pool sentence already says there is no reading, and there is no source yet.
  if (!w) return ''
  const r = w.reading
  const base = r.source === 'harness' ? `${HARNESS_NAME[row.harness] ?? row.harness} reported · ${ago(r.read_at, now)}`
    : r.source === 'agentd' ? `Read on ${row.host} · ${ago(r.read_at, now)}` : 'Estimated'
  if (row.state === 'offline') return `${base} · offline`
  if (w.freshness === 'stale' || w.freshness === 'expired') return `${base} · stale`
  return base
}

// ---------- The plan sentence per pool ----------
/** A run of sentence text: `strong` is the lead, `num` a figure. */
export interface Seg { text: string; strong?: boolean; num?: boolean }
export interface Sentence { segs: Seg[]; ahead?: boolean }
const t = (text: string): Seg => ({ text })
const b = (text: string): Seg => ({ text, strong: true })
const bn = (text: string): Seg => ({ text, strong: true, num: true })
const n = (text: string): Seg => ({ text, num: true })
export const plainText = (s: Sentence) => s.segs.map(x => x.text).join('')
function joinList(items: Seg[][], last = ' and '): Seg[] {
  return items.flatMap((item, i) => (i === 0 ? item : [t(i === items.length - 1 ? last : ', '), ...item]))
}
export function nextWorkDay(s: CapacitySchedule | null, periodStart: string | undefined): string {
  if (!s || !periodStart) return 'on your next work day'
  const today = weekdayIn(periodStart, s.timezone)
  for (let i = 1; i <= 7; i++) if (s.week[(today + i) % 7].on) return i === 1 ? 'tomorrow' : DAYS[(today + i) % 7]
  return 'on your next work day'
}
export function poolSentence(pool: PoolView, now: number, timezone?: string): Sentence {
  const at = (iso: string) => when(iso, now, timezone)
  const live = pool.rows.filter(r => r.state === 'live' && r.primary).map(r => ({ r, p: accountPlan(r, now)! }))
  if (!live.length) {
    const first = pool.rows[0]
    if (!first) return { segs: [] }
    if (first.state === 'signin') {
      const tail = first.primary ? ` ${pct(first.primary.remaining_percent)} left, resets ${at(first.primary.reading.resets_at)}.` : ''
      return { segs: [b(`Paused until you sign in again on ${first.host}.`), t(tail)] }
    }
    if (first.state === 'offline') return { segs: [b(`Waits for ${first.host} to come back online.`)] }
    if (first.state === 'unavailable') return { segs: [b(`Reading unavailable on ${first.host}.`), t(' Agents skip it until the next check succeeds.')] }
    if (first.state === 'paused') return { segs: [b('Paused.'), t(' Agents leave it alone until you resume it in Settings.')] }
    return { segs: [b('No reading yet'), t(' — starts with the first run.')] }
  }
  if (pool.override === 'hold') return { segs: [b('On hold.'), t(` Agents leave ${pool.name} alone until you resume; today's share moves to the coming days.`)] }
  if (pool.override === 'sprint') {
    const until = pool.overrideUntil ? ` until ${at(pool.overrideUntil)}` : ' until it resets'
    if (live.length === 1) return { segs: [b('Sprint:'), t(' agents may use all '), n(pct(live[0].p.left)), t(` of ${pool.name}${until}.`)] }
    return { segs: [b('Sprint:'), t(' agents may use everything left on '), ...joinList(live.map(x => [t(`${x.r.name} (`), n(pct(x.p.left)), t(')')])), t(`${until}.`)] }
  }
  const label = (x: { r: AccountRow }) => (live.length > 1 ? x.r.name : pool.name)
  const expiring = live.filter(x => x.p.expiring)
  if (live[0].p.dayOff && !expiring.length) {
    const lost = live.filter(x => x.p.unused)
    if (lost.length) return { segs: [b('Day off.'), t(' '), ...joinList(lost.map(x => [n(pct(x.p.left)), t(` of ${label(x)}`)]), ', '), t(' resets before your next work day and goes unused. Sprint to use it.')], ahead: true }
    return { segs: [b('Day off:'), t(` agents rest. ${pool.name} continues ${nextWorkDay(live[0].r.schedule, live[0].r.primary?.pacing.period_start)}.`)] }
  }
  if (expiring.length && live[0].p.dayOff) {
    return { segs: [b('Day off, but '), ...joinList(expiring.map(x => [bn(`~${pct(x.p.budget)}`), b(` of ${label(x)}`)]), ', ').map(s => ({ ...s, strong: true })), b(' would expire'), t(' before your next work day, so agents use it today.')] }
  }
  if (live.length === 1) {
    const { r, p } = live[0]
    if (p.unused) return { segs: [b(`Resets ${at(p.reset)} before your next work time`), t(': '), n(pct(p.left)), t(' would go unused. Sprint to use it.')], ahead: true }
    if (p.resetToday) return { segs: [b('Today: use all '), bn(pct(p.left)), b(' left.'), t(` ${pool.name} resets at ${at(p.reset)}.`)] }
    if (p.ahead) return { segs: [b('Ahead of pace:'), t(' '), n(pct(p.used)), t(` used today, the plan was ~${pct(p.budget)}. Agents ease off ${pool.name} until tomorrow.`)], ahead: true }
    const lead = p.night >= 0.5
      ? [b('Today: use up to '), bn(`~${pct(p.budget - p.night)}`), b(' by day and '), bn(`~${pct(p.night)}`), b(' tonight')]
      : [b('Today: use up to '), bn(`~${pct(p.budget)}`), b(` of ${pool.name}`)]
    const so = p.fresh ? 'fresh week' : `${pct(p.used)} so far`
    const near = !p.finish || Date.parse(p.reset) - Date.parse(p.finish) < 3 * 3600e3
    const tail = near ? `on track to finish at 0% right as it resets ${at(p.reset)}` : `on track to finish at 0% by ${at(p.finish!)}, before it resets ${at(p.reset)}`
    void r
    return { segs: [...lead, t(` (${so}) — ${tail}.`)] }
  }
  const parts = live.map(x => [bn(`~${pct(x.p.budget)}`), b(` of ${x.r.name}`)])
  const lead: Seg[] = [b(`Today${live.some(x => x.p.night >= 0.5) ? ' and tonight' : ''}: `), ...parts.flatMap((part, i) => (i === 0 ? part : [b(i === parts.length - 1 ? ', then ' : ', '), ...part]))]
  return { segs: [...lead, t(' — soonest reset first, so each lands at 0% as it resets.')] }
}

// ---------- Gauge preference ----------
export type GaugeMode = 'left' | 'used'
export interface GaugePreference { mode: GaugeMode; accounts: Record<string, GaugeMode> }
export const gaugeModeFor = (pref: GaugePreference | null, accountId: string): GaugeMode => pref?.accounts?.[accountId] ?? pref?.mode ?? 'left'
/** Toggle one account; an account that matches the global choice stores nothing. */
export function toggleAccountMode(pref: GaugePreference | null, accountId: string): GaugePreference {
  const base: GaugePreference = { mode: pref?.mode ?? 'left', accounts: { ...(pref?.accounts ?? {}) } }
  const next: GaugeMode = gaugeModeFor(base, accountId) === 'left' ? 'used' : 'left'
  if (next === base.mode) delete base.accounts[accountId]
  else base.accounts[accountId] = next
  return base
}
export const setGlobalMode = (_pref: GaugePreference | null, mode: GaugeMode): GaugePreference => ({ mode, accounts: {} })
