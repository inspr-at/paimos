// SPDX-License-Identifier: AGPL-3.0-only
// Progress over time for the Goals view (AEON-1457, design r5 and its amendment).
// The server sends one point per bucket (AEON-1456: hourly for 24 h, 6-hourly for
// 7 d, daily for 30 d), each the done % as of the bucket's end. These helpers turn
// it into the average rate, the closed signal line, the ETA popover's projection,
// the fitted chart and the point line. Free of Vue so it can be unit tested.

export type GoalRange = '24h' | '7d' | '30d'
export const GOAL_RANGES: readonly GoalRange[] = ['24h', '7d', '30d']
export const RANGE_LABEL: Record<GoalRange, string> = { '24h': '24 h', '7d': '7 d', '30d': '30 d' }
export const isGoalRange = (value: unknown): value is GoalRange => typeof value === 'string' && (GOAL_RANGES as readonly string[]).includes(value)

export interface RatePoint { t: string; pct: number; delta: number; changed_keys?: string[]; keys?: string[]; keys_truncated?: boolean }

const HOUR = 3_600_000
const DAY = 24 * HOUR
// Below this the line says nothing useful about when a goal lands (Tick, r5 amendment rule 3).
export const MIN_PROJECTING_RATE = 0.05
// The longest closed or open signal, so the slot never changes width (rule 4).
export const LONGEST_SIGNAL = 'Ø −12.5 %/day · 30 d'

// The history the window holds: from the window start, or from the goal's
// creation when it is younger. Buckets that end before the goal existed are left out.
export function availablePoints<P extends RatePoint>(points: readonly P[], createdAt?: number | null): P[] {
  if (!points.length || createdAt == null || !Number.isFinite(createdAt)) return [...points]
  const ends = points.map((_, i) => i + 1 < points.length ? Date.parse(points[i + 1]!.t) : Infinity)
  const first = ends.findIndex(end => end > createdAt)
  return first <= 0 ? [...points] : points.slice(first)
}

export interface Rate {
  // Percentage points per hour; negative when the goal grew faster than it was done.
  perHour: number
  // When the measured history starts, and how many distinct values it holds (the start counts).
  from: number
  values: number
  current: number
}

// The average rate over the available history, up to the window: the change from
// the start value to now, over the hours since the start (at least one hour).
export function averageRate(points: readonly RatePoint[], now: number, createdAt?: number | null): Rate | null {
  const shown = availablePoints(points, createdAt)
  if (!shown.length) return null
  const windowStart = Date.parse(shown[0]!.t)
  const young = createdAt != null && Number.isFinite(createdAt) && createdAt > windowStart
  // A goal born inside the window starts from what it held when its first bucket closed:
  // work already done when it was created is not progress made since.
  const start = young ? shown[0]!.pct : shown[0]!.pct - shown[0]!.delta
  const from = young ? createdAt! : windowStart
  const counted = young ? shown.slice(1) : shown
  const current = shown[shown.length - 1]!.pct
  const hours = Math.max(1, (now - from) / HOUR)
  return { perHour: (current - start) / hours, from, values: 1 + counted.filter(point => point.delta !== 0).length, current }
}

const decimal = (value: number, locale?: string) => new Intl.NumberFormat(locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(value)
// Per hour for 24 h; per day for 7 d and 30 d, where per hour reads as almost nothing (rule 2).
export function rateText(rate: Pick<Rate, 'perHour'>, range: GoalRange, locale?: string): string {
  const perDay = range !== '24h'
  const value = perDay ? rate.perHour * 24 : rate.perHour
  // −0.0 reads as a regression that did not happen.
  const shown = Math.abs(value) < 0.05 ? 0 : value
  return `Ø ${decimal(shown, locale).replace('-', '−')} %/${perDay ? 'day' : 'h'}`
}
export const signalText = (rate: Pick<Rate, 'perHour'>, range: GoalRange, locale?: string) => `${rateText(rate, range, locale)} · ${RANGE_LABEL[range]}`

// The gold stall line wins over the rate (rule 1); the server decides the stall.
export function stallText(since: number, now: number): string {
  return `no progress for ${Math.max(4, Math.floor((now - since) / HOUR))} h`
}

const clockFormat = (timeZone?: string) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone })
const dayKey = (ms: number, timeZone?: string) => new Intl.DateTimeFormat('en-CA', { timeZone }).format(new Date(ms))
const weekdayDay = (ms: number, timeZone?: string) => new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone }).format(new Date(ms)).replace(',', '')

// "At Ø 0.8 %/h this lands ~19:40": a clock time today, a date when past today,
// "beyond 30 days" past that. Omitted at 0.05 %/h or less, with fewer than three
// values, or once the goal is done (rule 3). LANDS keeps the agent's ETA.
export function projectionText(rate: Rate | null, range: GoalRange, now: number, done: boolean, options: { timeZone?: string; locale?: string } = {}): string | null {
  if (!rate || done || rate.values < 3 || rate.perHour <= MIN_PROJECTING_RATE || rate.current >= 100) return null
  const at = now + (100 - rate.current) / rate.perHour * HOUR
  const lead = `At ${rateText(rate, range, options.locale)} this lands`
  if (at - now > 30 * DAY) return `${lead} beyond 30 days`
  if (dayKey(at, options.timeZone) === dayKey(now, options.timeZone)) {
    const rounded = Math.round(at / (5 * 60_000)) * 5 * 60_000
    return `${lead} ~${clockFormat(options.timeZone).format(new Date(rounded))}`
  }
  return `${lead} ~${weekdayDay(at, options.timeZone)}`
}

// The point line names the bucket's start: "11:00", "Mon 06:00", "14 Oct".
export function bucketLabel(range: GoalRange, t: number, timeZone?: string): string {
  if (range === '24h') return clockFormat(timeZone).format(new Date(t))
  if (range === '7d') return `${new Intl.DateTimeFormat('en-GB', { weekday: 'short', timeZone }).format(new Date(t))} ${clockFormat(timeZone).format(new Date(t))}`
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', timeZone }).format(new Date(t))
}
// "13:00 · 20 % (+7) · AEON-1445, AEON-1441 +1 changed", or "· no change".
export function pointText(range: GoalRange, point: RatePoint, timeZone?: string): string {
  const keys = point.changed_keys ?? point.keys ?? []
  const more = keys.length - 2 + (point.keys_truncated ? 1 : 0)
  const change = point.delta ? ` (${point.delta > 0 ? '+' : '−'}${Math.abs(point.delta)})` : ''
  const which = keys.length ? ` · ${keys.slice(0, 2).join(', ')}${more > 0 ? ` +${point.keys_truncated ? 'more' : more}` : ''} changed` : ' · no change'
  return `${bucketLabel(range, Date.parse(point.t), timeZone)} · ${point.pct} %${change}${which}`
}
// "History since 16:40", with the day when it is not today.
export function historySince(createdAt: number, now: number, timeZone?: string): string {
  const time = clockFormat(timeZone).format(new Date(createdAt))
  if (dayKey(createdAt, timeZone) === dayKey(now, timeZone)) return `History since ${time}`
  return `History since ${new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone }).format(new Date(createdAt)).replace(',', '')} ${time}`
}

// The y-domain is fitted to the window: [max(0, min − 5), min(100, max + 5)], at least 20 points tall.
export function fittedDomain(values: readonly number[]): [number, number] {
  if (!values.length) return [0, 100]
  let lo = Math.max(0, Math.min(...values) - 5), hi = Math.min(100, Math.max(...values) + 5)
  if (hi - lo < 20) {
    const mid = (lo + hi) / 2
    lo = mid - 10; hi = mid + 10
    if (lo < 0) { hi -= lo; lo = 0 }
    if (hi > 100) { lo -= hi - 100; hi = 100 }
    lo = Math.max(0, lo)
  }
  return [lo, hi]
}

// The stepped line in a 100-wide viewBox, `height` px tall with 5 px headroom. Null
// values are buckets before the goal existed: the line starts where it did.
export const CHART = { height: 34, top: 5, bottom: 5, x0: 3, x1: 95 } as const
export interface ChartShape { path: string; refs: number[]; first: number; end: { x: number; y: number }; xOf: (index: number) => number; yOf: (pct: number) => number }
export function chartShape(values: readonly (number | null)[]): ChartShape {
  const known = values.filter((v): v is number => v !== null)
  const [lo, hi] = fittedDomain(known)
  const n = Math.max(1, values.length), w = (CHART.x1 - CHART.x0) / n
  const yOf = (pct: number) => CHART.top + (1 - (pct - lo) / (hi - lo)) * (CHART.height - CHART.top - CHART.bottom)
  const xOf = (index: number) => CHART.x0 + (index + 0.5) * w
  const first = values.findIndex(v => v !== null)
  let path = ''
  if (first !== -1) {
    path = `M${(CHART.x0 + first * w).toFixed(2)} ${yOf(values[first]!).toFixed(2)}`
    for (let i = first + 1; i < values.length; i++) path += `H${(CHART.x0 + i * w).toFixed(2)}V${yOf(values[i] ?? known[known.length - 1]!).toFixed(2)}`
    path += `H${CHART.x1}`
  }
  // Faint 0 and 100 lines only when they fall inside the domain.
  const refs = [0, 100].filter(v => v >= lo && v <= hi).map(yOf)
  return { path, refs, first: Math.max(0, first), end: { x: CHART.x1, y: yOf(known.length ? known[known.length - 1]! : lo) }, xOf, yOf }
}
