// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1497 (Tick's approved fuel gauge, aeon-1497-dial-usage-approved.html): the open
// Agents dial reads quota first. Every number comes from GET /agent-accounts/quota-outlook,
// which pools the person's accounts per harness and replays the routing policy server-side
// (verdict, safe pace, forecasts). This module only words that answer and lays out its chart.
import { api, APIError } from './api.ts'
import { HARNESS_NAME } from './capacity.ts'

export type OutlookAccountState = 'active' | 'ready' | 'full' | 'held' | 'not_reported' | 'private'
export interface OutlookForecast { kind: 'waits' | 'empties' | 'used_up' | 'unused' | 'enough'; at: string | null; refill_at: string | null; unused_percent: number | null }
export interface OutlookAccount {
  account_id: string; label: string; state: OutlookAccountState; pooled: boolean
  used_percent: number | null; limit_percent: number | null; resets_at: string | null; read_at: string | null
  reading_age_seconds: number | null; freshness: string; forecast: OutlookForecast | null
}
export interface OutlookPoint { at: string; left_percent: number }
export interface OutlookLine {
  points: OutlookPoint[]
  refills: { at: string; account_id: string; from_percent: number; to_percent: number }[]
  empty: { from: string; to: string }[]
}
export interface OutlookHour { hour_at: string; known: boolean; empty: boolean; burn: number[] }
export interface OutlookGroup {
  harness: string; state: 'measured' | 'not_reported' | 'held'; accounts: OutlookAccount[]
  pool: { accounts: number; used_percent: number; left_percent: number; sampled_at: string; sampled_age_seconds: number } | null
  pace: { per_hour_24h: number | null; per_hour_6h: number | null; safe_per_hour: number | null; measured_hours: number }
  verdict: 'too_fast' | 'too_slow' | 'on_pace' | 'unknown'
  headline: { kind: 'runs_dry' | 'unused' | 'enough' | 'unknown' | 'not_reported' | 'held'; at: string | null; until: string | null; account_id: string; unused_percent: number | null; focus_at: string | null }
  history: OutlookHour[]; past_line: OutlookPoint[]; past_refills: { at: string; account_id: string }[]
  current: OutlookLine | null; safe: OutlookLine | null; projected: OutlookHour[]
}
export interface QuotaOutlook { generated_at: string; anchor_hour: string; history_hours: number; horizon_hours: number; truncated: boolean; groups: OutlookGroup[] }

export async function getQuotaOutlook(): Promise<QuotaOutlook> {
  const response = await api('/agent-accounts/quota-outlook')
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data)
  }
  return response.json()
}

// ---------- Time, in the person's zone ----------
export const HOUR_MS = 3_600_000
const DAY = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
const pad = (n: number) => String(n).padStart(2, '0')
const formats = new Map<string, Intl.DateTimeFormat>()
export interface Zoned { y: number; m: number; d: number; wd: number; h: number; mi: number }
export function zoned(ms: number, tz?: string): Zoned {
  const key = tz || ''
  let f = formats.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat('en-GB', { ...(tz ? { timeZone: tz } : {}), year: 'numeric', month: 'numeric', day: 'numeric', weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
    formats.set(key, f)
  }
  const p = Object.fromEntries(f.formatToParts(new Date(ms)).map(x => [x.type, x.value]))
  return { y: +p.year!, m: +p.month! - 1, d: +p.day!, wd: DAY.indexOf(p.weekday!), h: +p.hour! % 24, mi: +p.minute! }
}
export const hm = (ms: number, tz?: string) => { const z = zoned(ms, tz); return `${pad(z.h)}:${pad(z.mi)}` }
export const dayName = (ms: number, tz?: string) => DAY[zoned(ms, tz).wd]!
export const dayHm = (ms: number, tz?: string) => `${dayName(ms, tz)} ${hm(ms, tz)}`
const sameDay = (a: number, b: number, tz?: string) => { const x = zoned(a, tz), y = zoned(b, tz); return x.y === y.y && x.m === y.m && x.d === y.d }
/** To ten minutes: a projected moment is never sharper than that. */
export const round10 = (ms: number) => Math.round(ms / 600_000) * 600_000
const at = (iso: string | null | undefined) => iso ? Date.parse(iso) : NaN
export function ago(seconds: number): string {
  const minutes = Math.max(0, Math.round(seconds / 60))
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  return hours < 48 ? `${hours} h ago` : `${Math.round(hours / 24)} d ago`
}
/** A reading this old reads as its age, in muted ink. */
export const STALE_SECONDS = 2 * 3600
export const f1 = (n: number) => (Math.round(n * 10) / 10).toFixed(1)
function span(ms: number): string {
  const minutes = Math.round(ms / 60_000)
  if (minutes < 60) return `${Math.max(10, Math.round(minutes / 10) * 10)} min`
  const hours = Math.round(minutes / 60)
  return hours < 48 ? `${hours} h` : `${Math.round(hours / 24)} d`
}

// ---------- The answer in words ----------
/** pending: the outlook is not read yet (or failed); the plan's accounts reserve the rows meanwhile. */
export type UsageKind = 'fast' | 'slow' | 'pace' | 'unknown' | 'held' | 'not_reported' | 'none' | 'pending'
/** The harnesses whose quota readings the server keeps (AEON-1499); others never report. */
export const QUOTA_HARNESSES = new Set(['claude', 'codex'])
export interface KnownAccount { account_id: string; label: string }
export interface UsageView {
  harness: string; name: string; kind: UsageKind; group: OutlookGroup | null
  /** The plan's accounts of this harness: rows to reserve while the outlook is read. */
  known: KnownAccount[]
  /** The headline sentence of the detail. */
  head: string
  /** The harness row's sub line. */
  short: string
  /** The folded unit's tooltip and accessible name. */
  tip: string
  /** "73 % of the pool used · 3 accounts · sampled 11:10". */
  muted: string
  stale: boolean
  verdict: string
  /** "Sustainable: up to 2.2 %/h (now 2.5 %/h)". */
  paceLine: string
  /** Hours after the anchor hour of the moment the headline names. */
  focus: number | null
}
export const VERDICT_WORD: Record<UsageKind, string> = { fast: 'Too fast', slow: 'Too slow', pace: 'On pace', unknown: 'Pace not measured yet', held: 'Held', not_reported: '', none: '', pending: '' }
export const VERDICT_ICON: Record<UsageKind, string> = { fast: 'alert', slow: 'hourglass', pace: 'check', unknown: 'ring-dashed', held: 'lock', not_reported: 'ring-dashed', none: '', pending: '' }

/**
 * One harness's usage in words. Without its group the plan's accounts (known) decide: none means
 * no account; a harness that never reports quota says so at once; otherwise the read is pending.
 */
export function usageView(harness: string, group: OutlookGroup | undefined, anchorHour: string | undefined, tz?: string, known: KnownAccount[] = [], read: 'loading' | 'ready' | 'error' = 'ready'): UsageView {
  const name = HARNESS_NAME[harness] ?? harness
  const base: UsageView = { harness, name, kind: 'none', group: group ?? null, known, head: 'No accounts yet', short: 'No accounts yet', tip: '', muted: '', stale: false, verdict: '', paceLine: '', focus: null }
  if (!group || !group.accounts.length) {
    if (!known.length) return base
    const accounts = `${known.length} ${known.length === 1 ? 'account' : 'accounts'}`
    if (read === 'ready' || !QUOTA_HARNESSES.has(harness)) return { ...base, kind: 'not_reported', head: 'Quota not reported', short: `Quota not reported · ${accounts}`, muted: accounts }
    return { ...base, kind: 'pending', head: '', short: '' }
  }
  const n = group.accounts.length, accounts = `${n} ${n === 1 ? 'account' : 'accounts'}`
  if (group.state === 'not_reported') return { ...base, kind: 'not_reported', head: 'Quota not reported', short: `Quota not reported · ${accounts}`, muted: accounts }
  if (group.state === 'held') return { ...base, kind: 'held', head: n === 1 ? 'The account is held.' : 'Every account is held.', short: 'all accounts held', tip: `${name}: every account is held`, muted: accounts }
  const label = (id: string) => group.accounts.find(a => a.account_id === id)?.label ?? 'an account'
  const pool = group.pool!
  const pooled = `${pool.accounts} ${pool.accounts === 1 ? 'account' : 'accounts'}`
  const missing = group.accounts.filter(a => a.state === 'not_reported').length
  const stale = pool.sampled_age_seconds >= STALE_SECONDS
  const muted = `${Math.round(pool.used_percent)} % of the pool used · ${pooled}${missing ? ` · ${missing} not reported` : ''} · sampled ${stale ? ago(pool.sampled_age_seconds) : hm(at(pool.sampled_at), tz)}`
  const sus = group.pace.safe_per_hour, now = group.pace.per_hour_24h
  const paceLine = sus === null ? '' : `Sustainable: up to ${f1(sus)} %/h${now === null ? '' : ` (now ${f1(now)} %/h)`}`
  const focus = group.headline.focus_at && anchorHour ? (at(group.headline.focus_at) - at(anchorHour)) / HOUR_MS : null
  const view = { ...base, muted, stale, paceLine, focus }
  const h = group.headline
  if (group.verdict === 'too_fast' && h.kind === 'runs_dry' && h.at) {
    const from = round10(at(h.at)), until = h.until ? at(h.until) : NaN
    const refill = Number.isFinite(until) ? (sameDay(from, until, tz) ? hm(until, tz) : dayHm(until, tz)) : ''
    const nowMs = at(pool.sampled_at) + pool.sampled_age_seconds * 1000
    const emptyNow = at(h.at) <= nowMs + 60_000
    let head: string, short: string
    if (emptyNow) {
      head = refill ? `${name} is out of quota until ${label(h.account_id)} refills at ${dayHm(until, tz)}.` : `${name} is out of quota, and nothing refills within 7 days.`
      short = refill ? `out of quota · refill ${dayHm(until, tz)}` : 'out of quota'
    } else {
      head = refill ? `${name} lasts until ${dayName(from, tz)} ~${hm(from, tz)}, then ${span(until - from)} without until ${label(h.account_id)} refills at ${refill}.` : `${name} lasts until ${dayName(from, tz)} ~${hm(from, tz)}; nothing refills within 7 days.`
      short = `lasts until ${dayHm(from, tz)}${refill ? ` · refill ${refill}` : ''}`
    }
    return { ...view, kind: 'fast', head, short, tip: `${name} ${short}`, verdict: VERDICT_WORD.fast }
  }
  if (group.verdict === 'too_slow' && h.kind === 'unused' && h.at && h.unused_percent !== null) {
    const u = Math.round(h.unused_percent), day = dayName(at(h.at), tz)
    return { ...view, kind: 'slow', head: `About ${u} % will refill unused on ${day}. There’s room for more work.`, short: `${u} % unused at refill`, tip: `${name}: about ${u} % refills unused on ${day}`, verdict: VERDICT_WORD.slow }
  }
  if (group.verdict === 'on_pace') {
    const next = h.at ? dayHm(at(h.at), tz) : ''
    return { ...view, kind: 'pace', head: next ? `Enough until the next refill (${next}).` : 'Enough for the next 7 days.', short: 'enough until refill', tip: next ? `${name} has enough until the refill ${next}` : `${name} has enough for the next 7 days`, verdict: VERDICT_WORD.pace }
  }
  return { ...view, kind: 'unknown', head: `${Math.round(pool.left_percent)} % of the pool left. The pace appears after an hour of readings.`, short: `${Math.round(pool.left_percent)} % left · pace not measured yet`, tip: `${name}: ${Math.round(pool.left_percent)} % left`, verdict: VERDICT_WORD.unknown, focus: null }
}

/** The default range: 3 days back (1 on phones); ahead at least 2 days (1 on phones), extended to the headline's moment, at most 7. */
export function defaultRange(view: UsageView, compact: boolean): { past: number; ahead: number } {
  const base = compact ? 1 : 2
  return { past: compact ? 1 : 3, ahead: view.focus === null ? base : Math.max(0, Math.min(7, Math.max(base, Math.ceil((view.focus + 1) / 24)))) }
}

// ---------- The subscriptions ----------
const PICKS: Record<number, number[]> = { 1: [1], 2: [1, 4], 3: [1, 3, 5], 4: [1, 2, 4, 5], 5: [1, 2, 3, 4, 5] }
/** One calm teal ramp; the picks keep neighbours apart for 1 to 5 accounts. */
export const shadeOf = (count: number, i: number) => `var(--burn-${PICKS[Math.min(5, Math.max(1, count))]![i] ?? 5})`
export interface AccountRow {
  id: string; name: string; shade: string; state: OutlookAccountState
  left: number | null; refills: string; forecast: string; tone: '' | 't-fast' | 't-slow' | 't-pace' | 'quiet'
  /** The state column: active or held with an icon, a stale reading's age, or nothing. */
  stateWord: string; stateIcon: string
}
export function accountRows(group: OutlookGroup | null, tz?: string, known: KnownAccount[] = []): AccountRow[] {
  if (!group) return known.map((a, i) => ({ id: a.account_id, name: a.label, shade: shadeOf(known.length, i), state: 'not_reported', left: null, refills: '', forecast: '', tone: '', stateWord: '', stateIcon: '' }))
  const n = group.accounts.length
  return group.accounts.map((a, i) => {
    const row: AccountRow = { id: a.account_id, name: a.label, shade: shadeOf(n, i), state: a.state, left: null, refills: '', forecast: '', tone: '', stateWord: '', stateIcon: '' }
    if (a.state === 'not_reported' || a.state === 'private' || a.used_percent === null) return row
    row.left = Math.max(0, Math.round(100 - a.used_percent))
    row.refills = a.resets_at ? `refills ${dayHm(at(a.resets_at), tz)}` : ''
    const f = a.forecast
    if (f?.kind === 'waits') { row.forecast = 'waits for refill'; row.tone = 'quiet' }
    else if (f?.kind === 'empties' && f.at) { row.forecast = `empty ~${dayHm(round10(at(f.at)), tz)}`; row.tone = 't-fast' }
    else if (f?.kind === 'used_up' && f.at) row.forecast = `used up ~${dayHm(round10(at(f.at)), tz)}`
    else if (f?.kind === 'unused' && f.unused_percent !== null) { row.forecast = `${Math.round(f.unused_percent)} % unused`; row.tone = f.unused_percent >= 20 ? 't-slow' : '' }
    else if (f?.kind === 'enough') { row.forecast = 'enough'; row.tone = 't-pace' }
    if (a.state === 'active') { row.stateWord = 'active'; row.stateIcon = 'pulse' }
    else if (a.state === 'held') { row.stateWord = 'held'; row.stateIcon = 'lock' }
    else if ((a.reading_age_seconds ?? 0) >= STALE_SECONDS) row.stateWord = ago(a.reading_age_seconds!)
    return row
  })
}

// ---------- The chart's data, in hours after the anchor hour ----------
export interface ChartLine { pts: [number, number][]; refills: { t: number; i: number; from: number; to: number }[]; empty: [number, number][] }
export interface ChartBar { parts: number[]; flag: boolean }
export interface ChartModel {
  anchor: number; nowH: number; names: string[]; shades: string[]
  past: Map<number, ChartBar>; future: Map<number, ChartBar>
  pastLine: [number, number][]; cur: ChartLine | null; safe: ChartLine | null
  pastRefills: { t: number; i: number }[]
  p24: number | null; p6: number | null; sus: number | null
}
export function chartModel(group: OutlookGroup, generatedAt: string, anchorHour: string): ChartModel {
  const anchor = at(anchorHour), h = (iso: string) => (at(iso) - anchor) / HOUR_MS
  const index = new Map(group.accounts.map((a, i) => [a.account_id, i]))
  const line = (l: OutlookLine | null): ChartLine | null => l && {
    pts: l.points.map(p => [h(p.at), p.left_percent]),
    refills: l.refills.map(r => ({ t: h(r.at), i: index.get(r.account_id) ?? 0, from: r.from_percent, to: r.to_percent })),
    empty: l.empty.map(e => [h(e.from), h(e.to)]),
  }
  const bars = (hours: OutlookHour[], flag: (x: OutlookHour) => boolean) => new Map(hours.filter(x => x.known).map(x => [Math.round(h(x.hour_at)), { parts: x.burn, flag: flag(x) }]))
  const n = group.accounts.length
  return {
    anchor, nowH: h(generatedAt), names: group.accounts.map(a => a.label), shades: group.accounts.map((_, i) => shadeOf(n, i)),
    past: bars(group.history, x => x.empty), future: bars(group.projected, x => x.empty),
    pastLine: group.past_line.map(p => [h(p.at), p.left_percent]), cur: line(group.current), safe: line(group.safe),
    pastRefills: group.past_refills.map(r => ({ t: h(r.at), i: index.get(r.account_id) ?? 0 })),
    p24: group.pace.per_hour_24h, p6: group.pace.per_hour_6h, sus: group.pace.safe_per_hour,
  }
}
/** The line's value at t: linear between corners, the later value on a vertical step. */
export function valueAt(pts: [number, number][], t: number): number | null {
  if (!pts.length || t < pts[0]![0] - 1e-9) return null
  let v = pts[0]![1]
  for (let j = 0; j < pts.length; j++) {
    const [x, y] = pts[j]!
    if (x <= t + 1e-9) { v = y; continue }
    const [x0, y0] = pts[j - 1]!
    return x - x0 < 1e-9 ? v : y0 + (y - y0) * (t - x0) / (x - x0)
  }
  return v
}
export const fullLine = (m: ChartModel) => [...m.pastLine, ...(m.cur?.pts.slice(1) ?? [])] as [number, number][]

// ---------- The chart's geometry (the approved design's frame, labels placed without overlap) ----------
export type Measure = (text: string, weight: number) => number
export interface Frame { W: number; H: number; L: number; T: number; ph: number; y0: number; sTop: number; sBot: number; SH: number; start: number; end: number; N: number; pitch: number }
export function frame(W: number, H: number, past: number, ahead: number): Frame {
  const compact = H < 200, L = 38, T = 30, G = compact ? 15 : 16, SH = compact ? 22 : 28, B = 20
  const ph = H - T - G - SH - B, y0 = T + ph, sTop = y0 + G, sBot = sTop + SH
  const start = -24 * past, N = 24 * (past + ahead), end = start + N
  return { W, H, L, T, ph, y0, sTop, sBot, SH, start, end, N, pitch: (W - L) / N }
}
export const xh = (f: Frame, h: number) => f.L + (h - f.start) * f.pitch
export const yl = (f: Frame, v: number) => f.y0 - v / 100 * f.ph
const px = (v: number) => Math.round(v) + 0.5
const clamp = (n: number, a: number, b: number) => Math.max(a, Math.min(b, n))
export interface SvgText { x: number; y: number; text: string; cls: string; anchor?: 'start' | 'middle' | 'end' }
export interface SvgLine { x1: number; x2: number; y1: number; y2: number; cls: string }
export interface SvgRect { x: number; y: number; w: number; h: number }
/** A burn bar: account i, measured (solid shade) or projected (its hatch). */
export interface SvgBar extends SvgRect { i: number; projected: boolean }
export interface ChartGeometry {
  frame: Frame; levels: SvgLine[]; levelText: SvgText[]; days: SvgLine[]; dayText: SvgText[]
  strip: { base: SvgLine; sus: SvgLine | null; label: SvgText }; bars: SvgBar[]; off: SvgRect[]
  area: string; pastPath: string; curPath: string; safePath: string
  empty: SvgRect[]; emptyText: SvgText[]; marks: (SvgLine & { kind: 'now' | 'refill' })[]; markText: SvgText[]; ends: SvgText[]
  smax: number; refillMarks: { h: number; name: string; past: boolean }[]
}
const pathOf = (f: Frame, pts: [number, number][]) => pts.map(([t, v], j) => `${j ? 'L' : 'M'}${xh(f, t).toFixed(1)} ${yl(f, v).toFixed(1)}`).join('')

/** Local midnights between two anchor hours, in the person's zone. */
function midnights(anchor: number, start: number, end: number, tz?: string): number[] {
  const out: number[] = []
  for (let h = start; h < end; h++) {
    const ms = anchor + h * HOUR_MS, z = zoned(ms, tz), into = z.h * 60 + z.mi
    const next = into === 0 ? 0 : 1440 - into
    if (next < 60) { const t = h + next / 60; if (t > start && t < end && !out.some(x => Math.abs(x - t) < 1e-6)) out.push(t) }
  }
  return out
}

export function chartGeometry(m: ChartModel, W: number, H: number, past: number, ahead: number, measure: Measure, tz?: string): ChartGeometry {
  const f = frame(W, H, past, ahead), { L, T, y0, sTop, sBot, SH, start, end, pitch } = f
  const out: ChartGeometry = { frame: f, levels: [], levelText: [], days: [], dayText: [], strip: { base: { x1: L, x2: W, y1: px(sBot), y2: px(sBot), cls: 'cx-sbase' }, sus: null, label: { x: L - 6, y: sTop + SH / 2 + 3.5, text: 'burn', cls: 'cx-gl', anchor: 'end' } }, bars: [], off: [], area: '', pastPath: '', curPath: '', safePath: '', empty: [], emptyText: [], marks: [], markText: [], ends: [], smax: 2, refillMarks: [] }
  for (const v of [0, 50, 100]) {
    const y = px(yl(f, v))
    out.levels.push({ x1: L, x2: W, y1: y, y2: y, cls: v === 0 ? 'cx-base' : 'cx-grid' })
    out.levelText.push({ x: L - 6, y: y + 3.5, text: `${v} %`, cls: 'cx-yl', anchor: 'end' })
  }
  // Days: a hairline at each local midnight, the weekday after it, 12:00 when a day is wide enough.
  const nowDay = dayName(m.anchor + m.nowH * HOUR_MS, tz), dayW = 24 * pitch
  const mids = midnights(m.anchor, start, end, tz)
  const label = (h: number, x: number) => { const d = dayName(m.anchor + (h + 0.01) * HOUR_MS, tz); out.dayText.push({ x, y: sBot + 14, text: d, cls: d === nowDay ? 'cx-dl today' : 'cx-dl' }) }
  if (!mids.length || xh(f, mids[0]!) - L >= 34) label(start, L + 3)
  for (const mid of mids) {
    const x = px(xh(f, mid))
    out.days.push({ x1: x, x2: x, y1: T, y2: y0, cls: 'cx-day' }, { x1: x, x2: x, y1: sTop, y2: sBot + 4, cls: 'cx-day' })
    if (W - xh(f, mid) >= 24) label(mid, xh(f, mid) + 4)
    if (dayW >= 80) { const xn = xh(f, mid + 12); if (xn < W - 30) out.dayText.push({ x: xn, y: sBot + 14, text: '12:00', cls: 'cx-hl-t', anchor: 'middle' }) }
  }
  if (dayW >= 80 && mids.length) { const xn = xh(f, mids[0]! - 12); if (xn > L + 34) out.dayText.push({ x: xn, y: sBot + 14, text: '12:00', cls: 'cx-hl-t', anchor: 'middle' }) }
  // The strip: burn per hour on the same hours; its dashed level is the safe pace.
  const total = (b: ChartBar) => b.parts.reduce((a, v) => a + v, 0)
  const peak = Math.max(0, m.sus ?? 0, ...[...m.past.values(), ...m.future.values()].map(total))
  out.smax = [2, 3, 4, 5, 6, 8, 10, 15, 20, 30, 50, 100].find(v => v >= peak * 1.05) ?? 100
  const ys = (v: number) => sBot - v / out.smax * SH
  if (m.sus !== null) out.strip.sus = { x1: L, x2: W, y1: px(ys(Math.min(m.sus, out.smax))), y2: px(ys(Math.min(m.sus, out.smax))), cls: 'cx-ssus' }
  const gap = pitch > 5 ? 1.5 : pitch >= 2.6 ? 1 : 0, bw = Math.max(0.9, pitch - gap)
  const stack = (h: number, parts: number[], projected: boolean, from = 0) => {
    let acc = from
    parts.forEach((v, i) => {
      if (v <= 0.001) return
      const top = ys(Math.min(out.smax, acc + v)), hgt = ys(Math.min(out.smax, acc)) - top
      acc += v
      if (hgt <= 0) return
      out.bars.push({ x: xh(f, h) + gap / 2, y: top, w: bw, h: hgt, i, projected })
    })
    return acc
  }
  for (let h = start; h < end; h++) {
    const p = m.past.get(h), q = m.future.get(h)
    const base = p && h <= 0 ? stack(h, p.parts, false) : 0
    if (q && h >= 0 && ahead > 0) stack(h, q.parts, true, base)
    if (p?.flag && h <= 0) out.off.push({ x: xh(f, h), y: sBot - 6, w: pitch, h: 6 })
  }
  // The line: measured (solid, a faint fill under it), then at the current pace (dashed) and at the safe pace (dotted).
  const nowX = xh(f, m.nowH)
  if (m.pastLine.length) {
    out.pastPath = pathOf(f, m.pastLine)
    out.area = `${out.pastPath}L${xh(f, m.pastLine[m.pastLine.length - 1]![0]).toFixed(1)} ${y0}L${xh(f, m.pastLine[0]![0]).toFixed(1)} ${y0}Z`
  }
  if (ahead > 0) {
    if (m.safe?.pts.length) out.safePath = pathOf(f, m.safe.pts)
    if (m.cur?.pts.length) out.curPath = pathOf(f, m.cur.pts)
  }
  // Empty: where the pool runs dry before a refill, a hatched danger band on the axis; its words sit under it, never on a line.
  const placed: [number, number][] = []
  const bands: [number, number][] = []
  for (const [a, b] of m.cur?.empty ?? []) {
    if (!(b > m.nowH && a < end && ahead > 0)) continue
    const x1 = xh(f, Math.max(a, start)), x2 = xh(f, Math.min(b, end))
    bands.push([x1, x2])
    out.empty.push({ x: x1, y: y0 - 7, w: x2 - x1, h: 7 })
    const text = `empty ${hm(round10(m.anchor + a * HOUR_MS), tz)}–${hm(m.anchor + b * HOUR_MS, tz)}`, w = measure(text, 650)
    const tx = clamp((x1 + x2) / 2 - w / 2, L + 2, W - w - 2)
    if (!placed.some(([p, q]) => tx < q + 8 && tx + w > p - 8)) { placed.push([tx, tx + w]); out.emptyText.push({ x: tx, y: y0 + 12, text, cls: 'cx-empty-t' }) }
  }
  // Markers: now, then each refill (a vertical step in the line), labelled in two rows above the chart.
  type Mark = { kind: 'now' | 'refill'; h: number; texts: string[]; pref: 'start' | 'end'; name?: string; past?: boolean; x?: number; row?: number; text?: string; a?: number }
  const marks: Mark[] = [{ kind: 'now', h: m.nowH, texts: ['now'], pref: 'end' }]
  if (ahead > 0) for (const r of m.cur?.refills ?? []) if (r.t > m.nowH && r.t <= end) marks.push({ kind: 'refill', h: r.t, texts: [`${m.names[r.i]} refills`, 'refill'], pref: 'start', name: m.names[r.i] })
  for (const r of [...m.pastRefills].filter(r => r.t >= start && r.t <= m.nowH).sort((a, b) => b.t - a.t)) marks.push({ kind: 'refill', h: r.t, texts: [`${m.names[r.i]} refilled`, 'refill'], pref: 'start', name: m.names[r.i], past: true })
  const rows: [number, number][][] = [[], []], BASE = [11, 24], PAD = 6
  const free = (row: number, a: number, b: number) => rows[row]!.every(([c, d]) => b + PAD <= c || a >= d + PAD)
  for (const mk of marks) {
    mk.x = xh(f, mk.h); mk.row = -1
    outer: for (const row of [0, 1]) for (const text of mk.texts) for (const anchor of [mk.pref, mk.pref === 'end' ? 'start' : 'end'] as const) {
      const w = measure(text, mk.kind === 'now' ? 650 : 500), a = anchor === 'start' ? mk.x + 4 : mk.x - 4 - w, b = a + w
      if (a < 0 || b > W || !free(row, a, b)) continue
      rows[row]!.push([a, b]); if (row === 0) rows[1]!.push([mk.x - 2, mk.x + 2])
      Object.assign(mk, { row, text, a }); break outer
    }
  }
  for (const mk of marks) {
    const x = px(mk.x!), top = mk.row === 0 ? 14 : mk.row === 1 ? 27 : T, cls = mk.kind === 'now' ? 'cx-now' : 'cx-refill'
    out.marks.push({ kind: mk.kind, x1: x, x2: x, y1: top, y2: y0, cls }, { kind: mk.kind, x1: x, x2: x, y1: sTop, y2: sBot, cls })
    if (mk.row! >= 0) out.markText.push({ x: mk.a!, y: BASE[mk.row!]!, text: mk.text!, cls: mk.kind === 'now' ? 'cx-now-t' : 'cx-refill-t' })
    if (mk.kind === 'refill') out.refillMarks.push({ h: mk.h, name: mk.name!, past: !!mk.past })
  }
  // The two projections are named where they have room: clear of the lines, the "empty" words and the plot's edges.
  if (ahead > 0 && m.cur && m.safe && m.p24 !== null && m.sus !== null) {
    const lc = `at ${f1(m.p24)} %/h`, lsf = `safe pace ${f1(m.sus)} %/h`, w = Math.max(measure(lc, 600), measure(lsf, 600))
    const tOf = (x: number) => start + (x - L) / pitch
    const spanAt = (pts: [number, number][], x: number) => [tOf(x - w / 2), tOf(x + w / 2), tOf(x)].map(t => yl(f, valueAt(pts, t) ?? 0))
    const boxes = [...bands.map(([p, q]) => [p, q, y0 - 9, y0]), ...m.cur.refills.filter(r => r.t > m.nowH && r.t <= end).map(r => [xh(f, r.t) - 2, xh(f, r.t) + 2, T, y0])]
    const hits = (x: number, yb: number) => boxes.some(([p, q, t, bb]) => x - w / 2 < q! + 4 && x + w / 2 > p! - 4 && yb - 10 < bb! && yb > t!)
    for (const share of [0.3, 0.45, 0.6, 0.2, 0.75, 0.12, 0.88]) {
      const x = nowX + share * (W - nowX)
      if (x - w / 2 < nowX + 6 || x + w / 2 > W - 2) continue
      const c = spanAt(m.cur.pts, x), s = spanAt(m.safe.pts, x)
      const top = Math.min(...c, ...s), bottom = Math.max(...c, ...s), cUp = c[2]! <= s[2]!
      const below = bottom + 14 <= y0 - 9, yA = top - 6, yB = below ? bottom + 14 : top - 19
      if (yB < T + 10 || yA < T + 10) continue
      const [yC, yS] = below ? (cUp ? [yA, yB] : [yB, yA]) : (cUp ? [yB, yA] : [yA, yB])
      if (hits(x, yC) || hits(x, yS)) continue
      out.ends.push({ x, y: yC, text: lc, cls: 'cx-end-c', anchor: 'middle' }, { x, y: yS, text: lsf, cls: 'cx-end-s', anchor: 'middle' })
      break
    }
  }
  return out
}

/** The hour under the pointer, or null left of the plot. */
export function hourAt(f: Frame, x: number): number | null {
  if (x < f.L) return null
  return clamp(f.start + Math.floor((x - f.L) / f.pitch), f.start, f.end - 1)
}
export interface InfoPart { text: string; strong?: boolean; quiet?: boolean }
/** The fixed info line for one hour: "Sat 14:00 · 43 % left · 3.8 %/h · measured · account-a@ 2.9 · …". */
export function hourInfo(m: ChartModel, g: ChartGeometry, h: number, harnessName: string, tz?: string): InfoPart[] {
  const t = m.anchor + h * HOUR_MS, projected = h >= Math.floor(m.nowH) && !(h < m.nowH && m.past.has(h) && !m.future.has(h))
  const left = valueAt(fullLine(m), h + 0.5)
  const parts: InfoPart[] = [{ text: `${dayName(t, tz)} ${hm(t, tz)}`, strong: true }]
  if (left !== null) parts.push({ text: ' · ' }, { text: `${Math.round(left)} % left`, strong: true })
  for (const r of g.refillMarks) if (r.h >= h && r.h < h + 1) parts.push({ text: ` · ${r.name} ${r.past ? 'refilled' : 'refills'}` })
  const p = m.past.get(h), q = m.future.get(h)
  const burn = [...(p?.parts ?? []).map((v, i) => v + (q?.parts[i] ?? 0))]
  if (!p && q) q.parts.forEach((v, i) => { burn[i] = v })
  const sum = burn.reduce((a, v) => a + v, 0)
  const kind: InfoPart = { text: projected ? 'projected' : 'measured', quiet: true }
  if (!p && !q) return [...parts, { text: ' · ' }, { text: 'not measured', quiet: true }]
  if ((p?.flag && !projected) || (q?.flag && projected)) return [...parts, { text: ` · ${projected ? 'empty' : `${harnessName} empty`}` }, { text: ' · ' }, kind]
  if (sum < 0.005) return [...parts, { text: ' · 0 %/h · ' }, kind]
  const each = burn.map((v, i) => [v, m.names[i]!] as const).filter(([v]) => v >= 0.05).sort((a, b) => b[0] - a[0]).map(([v, nm]) => `${nm} ${f1(v)}`).join(' · ')
  return [...parts, { text: ` · ${f1(sum)} %/h · ` }, kind, { text: each ? ` · ${each}` : '' }]
}

// ---------- The folded dial's mini line (AEON-1497 addendum) ----------
export interface MiniGeometry { w: number; h: number; floor: number; past: string; proj: string; dot: { x: number; y: number }; dry: boolean }
/** The past 24 h solid, a dashed projection to the next refill (24 h at most), a dot at now; 0–50 % when the window never exceeds half. */
export function miniGeometry(m: ChartModel, w: number, h = 14): MiniGeometry | null {
  if (!m.pastLine.length) return null
  const refills = m.cur?.refills.filter(r => r.t > m.nowH).map(r => r.t) ?? []
  const first = refills.length ? Math.min(...refills) : Infinity, tEnd = Math.min(m.nowH + 24, first)
  // Amber says the pool runs dry before that refill, even when it happens past the drawn day.
  const dry = !!m.cur?.empty.some(([a]) => a < first)
  const clip = (pts: [number, number][], a: number, b: number): [number, number][] => {
    const inside = pts.filter(([t]) => t > a + 1e-9 && t < b - 1e-9)
    const va = valueAt(pts, a), vb = valueAt(pts, b)
    const out: [number, number][] = va === null ? [] : [[a, va]]
    out.push(...inside)
    for (const p of pts.filter(([t]) => Math.abs(t - b) < 1e-9)) out.push(p)
    if (!pts.some(([t]) => Math.abs(t - b) < 1e-9) && vb !== null) out.push([b, vb])
    return out
  }
  const past = clip(m.pastLine, Math.max(m.nowH - 24, m.pastLine[0]![0]), m.nowH)
  const proj = m.cur ? clip(m.cur.pts, m.nowH, tEnd) : []
  const top = Math.max(...past.map(p => p[1]), ...proj.map(p => p[1])) <= 50 ? 50 : 100
  const xt = (t: number) => ((t - m.nowH + 24) / 48) * (w - 4) + 2, yv = (v: number) => 1.5 + (1 - v / top) * (h - 3)
  const path = (pts: [number, number][]) => pts.map(([t, v], i) => `${i ? 'L' : 'M'}${xt(t).toFixed(1)} ${yv(v).toFixed(1)}`).join('')
  const nowV = valueAt(m.pastLine, m.nowH) ?? 0
  return { w, h, floor: h - 1, past: path(past), proj: path(proj), dot: { x: xt(m.nowH), y: yv(nowV) }, dry }
}
