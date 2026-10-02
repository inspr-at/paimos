// SPDX-License-Identifier: AGPL-3.0-only
// The release history's header in numbers (AEON-488). Seven stats cycle in one
// card, each with its value, a short sub-line, one or two chips that answer the
// next question a person asks ("and per day? per year?"), and the data for a
// small visual. The cadence chart counts releases per day, week or month over a
// chosen range. Calendar days are local, as in the list. Free of Vue for unit tests.
import { presentRelease, releasedAt, span, visibleReleases, type Release } from './releases.ts'

const HOUR = 3_600_000, DAY = 86_400_000, WEEK = 7 * DAY
const MONTH = 30.436875 * DAY
const WEEKS_A_YEAR = 365.25 / 7
// "Per week" is a rolling average over this many weeks; a shorter history
// averages since its first release.
export const WINDOW_WEEKS = 52

// ---------- Calendar ----------
// Calendar arithmetic, never multiples of 24 h, so daylight saving changes do not shift days.
const dayStart = (t: number) => { const d = new Date(t); return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() }
const addDays = (t: number, n: number) => { const d = new Date(t); return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n).getTime() }
const weekStart = (t: number) => addDays(dayStart(t), -((new Date(t).getDay() + 6) % 7))
const monthStart = (t: number) => { const d = new Date(t); return new Date(d.getFullYear(), d.getMonth(), 1).getTime() }
const addMonths = (t: number, n: number) => { const d = new Date(t); return new Date(d.getFullYear(), d.getMonth() + n, 1).getTime() }
// Calendar days from a to b, both counted ("23 Sep to 1 Oct" is 9).
const daysBetween = (a: number, b: number) => Math.round((dayStart(b) - dayStart(a)) / DAY) + 1

// Fixed English names: "Sep", never the "Sept" some locales print.
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const MONTHS_LONG = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']
const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
const WEEKDAYS_LONG = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']
const pad = (n: number) => String(n).padStart(2, '0')
// "23 Sep"
export const dayMonth = (t: number) => { const d = new Date(t); return `${d.getDate()} ${MONTHS[d.getMonth()]}` }
// "Thu 24 Sep"
export const dayName = (t: number) => `${WEEKDAYS[new Date(t).getDay()]} ${dayMonth(t)}`
// "09:56"
export const clock = (t: number) => { const d = new Date(t); return `${pad(d.getHours())}:${pad(d.getMinutes())}` }
// When something started, as short as the day allows: "12:15" today, "Thu 12:15"
// this past week, "23 Sep 12:15" before that.
export function clockSince(at: number, now: number) {
  const back = daysBetween(at, now) - 1
  if (back <= 0) return clock(at)
  if (back < 7) return `${WEEKDAYS[new Date(at).getDay()]} ${clock(at)}`
  return `${dayMonth(at)} ${clock(at)}`
}
// "today", "yesterday", "on Wed", "on 23 Sep"
function onDay(at: number, now: number) {
  const back = daysBetween(at, now) - 1
  if (back <= 0) return 'today'
  if (back === 1) return 'yesterday'
  return back < 7 ? `on ${WEEKDAYS[new Date(at).getDay()]}` : `on ${dayMonth(at)}`
}

// ---------- Numbers, rounded honestly ----------
const whole = new Intl.NumberFormat('en-GB', { maximumFractionDigits: 0 })
const smallRate = new Intl.NumberFormat('en-GB', { maximumSignificantDigits: 2, useGrouping: false })
// Whole numbers from 10, one decimal below, and two significant digits for
// rates under a tenth so sparse histories keep their actual order of magnitude.
export function rate(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0'
  if (n < 0.1) return smallRate.format(n)
  if (n >= 9.95) return whole.format(Math.round(n))
  return String(Math.round(n * 10) / 10)
}
// Large figures to two significant digits: "5,200", "380".
export function roughly(n: number): string {
  if (!Number.isFinite(n) || n < 100) return rate(n)
  const step = 10 ** (Math.floor(Math.log10(n)) - 1)
  return whole.format(Math.round(n / step) * step)
}
// A multiple: "3×", "≈ 1.4×". The "≈" says the rounding hid a part.
export function times(ratio: number): string {
  const r = ratio >= 3 ? Math.round(ratio) : Math.round(ratio * 10) / 10
  return `${Math.abs(r - ratio) / ratio < 0.02 ? '' : '≈ '}${rate(r)}×`
}
const plural = (n: number, one: string, many: string) => `${whole.format(n)} ${n === 1 ? one : many}`

// ---------- Inputs ----------
interface Dated { release: Release; at: number }
// Statistics include reservations even when the person's history hides their rows.
// Use publication, tag or reservation time, oldest first.
function datedReleases(releases: Release[]): Dated[] {
  return visibleReleases(releases, true)
    .map(release => { const at = releasedAt(release); return { release, at: at ? Date.parse(at) : NaN } })
    .filter(x => Number.isFinite(x.at))
    .sort((a, b) => a.at - b.at)
}
const nameOf = (r: Release) => r.codename || r.version
// Releases per calendar day, keyed by the day's local midnight.
function perDay(list: Dated[]) {
  const out = new Map<number, number>()
  for (const x of list) { const d = dayStart(x.at); out.set(d, (out.get(d) ?? 0) + 1) }
  return out
}
// Where the rolling window begins: 52 weeks back, or the first release when the history is shorter.
export function windowOf(firstAt: number, now: number) {
  const back = now - WINDOW_WEEKS * WEEK
  return firstAt > back ? { from: firstAt, short: true } : { from: back, short: false }
}
// Distinct tickets the releases tell under Features and under Fixes, each key
// counted once, at the release that first told it. A key told as a feature
// stays a feature when a later release tells it again as a fix.
export function distinctTickets(list: { release: Release; at: number }[]) {
  const seen = new Set<string>()
  const features: number[] = [], fixes: number[] = []
  for (const { release, at } of [...list].sort((a, b) => a.at - b.at)) {
    const told = presentRelease(release, 'en')
    for (const [lines, into] of [[told.features, features], [told.fixes, fixes]] as const) {
      for (const line of lines) {
        if (seen.has(line.key)) continue
        seen.add(line.key)
        into.push(at)
      }
    }
  }
  return { features, fixes }
}

// ---------- The seven stats ----------
export type StatKey = 'perweek' | 'features' | 'since' | 'median' | 'week' | 'streak' | 'busiest'
export const STAT_KEYS: readonly StatKey[] = ['perweek', 'features', 'since', 'median', 'week', 'streak', 'busiest']
// The data each stat's small visual draws; the card turns it into SVG.
export type StatViz =
  | { kind: 'cumulative'; points: number[]; total: number; from: string; pace: string; aria: string }
  | { kind: 'stacked'; bars: { features: number; fixes: number }[]; from: string; aria: string }
  | { kind: 'timeline'; ticks: number[]; last: number; lastBeforeWindow: boolean; lastName: string; gap: string; marks: { at: number; label: string }[]; aria: string }
  | { kind: 'histogram'; buckets: { label: string; n: number }[]; median: number; medianLabel: string; aria: string }
  | { kind: 'week'; days: { letter: string; n: number | null; today: boolean }[]; aria: string }
  | { kind: 'streak'; days: number[]; from: string; to: string; labels: string[]; aria: string }
  | { kind: 'days'; days: number[]; peak: number; peakLabel: string; from: string; aria: string }
export interface Stat { key: StatKey; label: string; value: string; sub: string; chips: string[]; viz: StatViz }

// Sample points for the per-week visuals: one a day for a short history, else
// thirteen even steps across the window. Each is the end of its step.
function steps(from: number, now: number): number[] {
  if (now - from <= 14 * DAY) {
    const out: number[] = []
    for (let d = addDays(dayStart(from), 1); d < now; d = addDays(d, 1)) out.push(d)
    out.push(now)
    return out
  }
  return Array.from({ length: 13 }, (_, i) => i === 12 ? now : from + (i + 1) * (now - from) / 13)
}
const bucketed = (times: number[], ends: number[], from: number) => ends.map((end, i) => {
  const start = i ? ends[i - 1] : from
  return times.filter(t => t >= start && (t < end || (i === ends.length - 1 && t <= end))).length
})

// Gap buckets: minutes for a fast cadence, days for a slow one.
const FAST = [{ upTo: 15, label: '<15m' }, { upTo: 30, label: '15–30m' }, { upTo: 60, label: '30–60m' }, { upTo: 120, label: '1–2h' }, { upTo: 240, label: '2–4h' }, { upTo: Infinity, label: '>4h' }]
const SLOW = [{ upTo: 60, label: '<1h' }, { upTo: 240, label: '1–4h' }, { upTo: 720, label: '4–12h' }, { upTo: 1440, label: '12–24h' }, { upTo: 4320, label: '1–3d' }, { upTo: Infinity, label: '>3d' }]

export function releaseStats(releases: Release[], now: number): Stat[] {
  const all = datedReleases(releases)
  if (!all.length) return []
  const first = all[0]!.at
  const { from, short } = windowOf(first, now)
  const list = all.filter(x => x.at >= from)
  const elapsed = Math.max(DAY, now - from)
  const fromLabel = dayMonth(from)
  const sinceWhen = short ? `since the first release, ${fromLabel}` : `over the last ${WINDOW_WEEKS} weeks`
  const ends = steps(from, now)
  const days = perDay(list), allDays = perDay(all)
  const today = dayStart(now)
  const stats: Stat[] = []

  // 1. Releases per week
  const perWeek = list.length / elapsed * WEEK
  const perDayRate = perWeek / 7
  const cumulative = ends.map(end => list.filter(x => x.at <= end).length)
  stats.push({
    key: 'perweek', label: 'Releases per week', value: rate(perWeek), sub: `on average ${sinceWhen}`,
    chips: [perDayRate >= 0.95 ? `≈ ${rate(perDayRate)} a day` : `≈ ${rate(perWeek * WEEKS_A_YEAR / 12)} a month`, `≈ ${roughly(perWeek * WEEKS_A_YEAR)} a year at this pace`],
    viz: { kind: 'cumulative', points: cumulative, total: list.length, from: fromLabel, pace: perDayRate >= 0.95 ? `≈ ${rate(perDayRate)} a day` : `≈ ${rate(perWeek)} a week`, aria: `Releases added up since ${fromLabel}, reaching ${list.length} today, about ${rate(perDayRate)} a day` },
  })

  // 2. Features per week: distinct tickets grouped as features
  const told = distinctTickets(list)
  const featuresPerWeek = told.features.length / elapsed * WEEK
  const fixesPerWeek = rate(told.fixes.length / elapsed * WEEK)
  const fixBars = bucketed(told.fixes, ends, from)
  stats.push({
    key: 'features', label: 'Features per week', value: rate(featuresPerWeek), sub: `on average ${short ? `since ${fromLabel}` : sinceWhen}`,
    chips: [
      told.features.length ? `≈ ${rate(featuresPerWeek / 7)} a day` : 'no feature tickets told yet',
      ...(told.fixes.length ? [`+ ${fixesPerWeek} ${fixesPerWeek === '1' ? 'fix' : 'fixes'} a week`] : []),
    ],
    viz: {
      kind: 'stacked', from: fromLabel,
      bars: bucketed(told.features, ends, from).map((features, i) => ({ features, fixes: fixBars[i]! })),
      aria: `New features and fixes since ${fromLabel}: ${plural(told.features.length, 'feature', 'features')} and ${plural(told.fixes.length, 'fix', 'fixes')}`,
    },
  })

  // Gaps between consecutive releases in the window.
  const gaps: number[] = []
  for (let i = 1; i < list.length; i++) gaps.push(list[i]!.at - list[i - 1]!.at)
  const sorted = [...gaps].sort((a, b) => a - b)
  const median = sorted.length ? (sorted.length % 2 ? sorted[(sorted.length - 1) / 2]! : (sorted[sorted.length / 2 - 1]! + sorted[sorted.length / 2]!) / 2) : null

  // 3. Since the last release
  const last = all[all.length - 1]!
  const sinceLast = Math.max(0, now - last.at)
  const verb = last.release.state === 'reserved' ? 'reserved' : last.release.published_at ? 'published' : 'tagged'
  const shown = Math.max(40 * HOUR, Math.min(14 * DAY, sinceLast + 4 * HOUR))
  const start = now - shown
  const lastBeforeWindow = last.at < start
  const marks: { at: number; label: string }[] = []
  for (let d = addDays(dayStart(start), 1); d < now; d = addDays(d, 1)) marks.push({ at: (d - start) / shown, label: dayMonth(d) })
  stats.push({
    key: 'since', label: 'Since the last release', value: span(sinceLast),
    sub: `${last.release.codename ? `${last.release.codename}, ${verb}` : verb.charAt(0).toUpperCase() + verb.slice(1)} ${onDay(last.at, now)} at ${clock(last.at)}`,
    chips: median ? [sinceLast < median ? 'within the median gap' : `${times(sinceLast / median)} the median gap`] : [],
    viz: {
      kind: 'timeline', ticks: all.filter(x => x.at >= start && x.at <= now && x !== last).map(x => (x.at - start) / shown), last: Math.max(0, Math.min(1, (last.at - start) / shown)), lastBeforeWindow,
      lastName: nameOf(last.release), gap: span(sinceLast), marks,
      aria: `Release times over the last ${Math.round(shown / HOUR)} hours; ${span(sinceLast)} since ${nameOf(last.release)}${lastBeforeWindow ? ', before this window' : ''}`,
    },
  })

  // 4. Median gap
  const scale = median !== null && median > 4 * HOUR ? SLOW : FAST
  const buckets = scale.map(b => ({ label: b.label, n: 0 }))
  for (const g of gaps) buckets[scale.findIndex(b => g / 60_000 < b.upTo)]!.n++
  let medianAt = 0
  if (median !== null) {
    const i = scale.findIndex(b => median / 60_000 < b.upTo)
    const lo = i ? scale[i - 1]!.upTo : 0, hi = scale[i]!.upTo
    medianAt = i + (Number.isFinite(hi) ? (median / 60_000 - lo) / (hi - lo) : .5)
  }
  const p90 = sorted.length ? sorted[Math.ceil(sorted.length * .9) - 1]! : null
  const mean = gaps.length ? gaps.reduce((a, b) => a + b, 0) / gaps.length : null
  const mode = buckets.reduce((best, b, i) => b.n > buckets[best]!.n ? i : best, 0)
  stats.push({
    key: 'median', label: 'Median gap', value: median === null ? '—' : span(median), sub: median === null ? 'needs a second release' : 'between two releases',
    chips: [...(mean !== null ? [`mean ${span(mean)}`] : []), ...(p90 !== null && gaps.length >= 5 ? [`9 in 10 within ${span(p90)}`] : [])],
    viz: {
      kind: 'histogram', buckets, median: medianAt, medianLabel: median === null ? '' : `median ${span(median)}`,
      aria: median === null ? 'No gaps between releases yet' : `Gaps between releases: most are ${buckets[mode]!.label.replace('–', ' to ')}; median ${span(median)}`,
    },
  })

  // 5. This week (Monday to today)
  const monday = weekStart(now)
  const thisWeek = all.filter(x => x.at >= monday)
  const counted = daysBetween(Math.max(monday, first), now)
  const lastMonday = addDays(monday, -7)
  const lastWeek = all.filter(x => x.at >= lastMonday && x.at < monday).length
  const weekDays = Array.from({ length: 7 }, (_, i) => {
    const d = addDays(monday, i)
    return { letter: 'MTWTFSS'[i]!, n: d > today ? null : allDays.get(d) ?? 0, today: d === today, name: WEEKDAYS_LONG[new Date(d).getDay()] }
  })
  stats.push({
    key: 'week', label: 'This week', value: whole.format(thisWeek.length), sub: `${thisWeek.length === 1 ? 'release' : 'releases'} since Monday`,
    chips: [`${rate(thisWeek.length / counted)} a day so far`, ...(first < monday ? [`last week ${whole.format(lastWeek)}${first >= lastMonday ? ` (from ${WEEKDAYS[new Date(first).getDay()]})` : ''}`] : [])],
    viz: { kind: 'week', days: weekDays.map(({ letter, n, today }) => ({ letter, n, today })), aria: `Releases per day this week: ${weekDays.filter(d => d.n !== null).map(d => `${d.name} ${d.n}`).join(', ')} so far` },
  })

  // 6. Streak: days in a row with a release, up to today (or yesterday, while today may still bring one)
  let end = allDays.has(today) ? today : allDays.has(addDays(today, -1)) ? addDays(today, -1) : null
  let streak = 0, streakReleases = 0, streakStart = today
  for (let d = end; d !== null && allDays.has(d); d = addDays(d, -1)) { streak++; streakReleases += allDays.get(d)!; streakStart = d }
  let longest = 0
  for (const d of allDays.keys()) {
    if (allDays.has(addDays(d, -1))) continue
    let n = 0
    for (let x = d; allDays.has(x); x = addDays(x, 1)) n++
    longest = Math.max(longest, n)
  }
  const shownDays = Math.min(14, Math.max(streak, 7))
  end ??= today
  const chain = Array.from({ length: shownDays }, (_, i) => allDays.get(addDays(end!, i - shownDays + 1)) ?? 0)
  stats.push({
    key: 'streak', label: 'Streak', value: plural(streak, 'day', 'days'),
    sub: streak ? 'in a row with at least one release' : 'no release yesterday or today',
    chips: [
      ...(streak > 1 ? [`every day since ${dayMonth(streakStart)}`] : streak === 1 ? [end === today ? 'started today' : 'started yesterday'] : []),
      longest > streak ? `longest ${plural(longest, 'day', 'days')}` : plural(streakReleases, 'release', 'releases'),
    ],
    viz: {
      kind: 'streak', days: chain, from: dayMonth(addDays(end, 1 - shownDays)), to: end === today ? 'today' : 'yesterday',
      labels: chain.map((_, i) => String(new Date(addDays(end!, i - shownDays + 1)).getDate())),
      aria: streak ? `${plural(streak, 'day', 'days')} in a row with releases, from ${dayMonth(streakStart)} to ${end === today ? 'today' : 'yesterday'}; dot size is the number of releases` : `Releases per day over the last ${shownDays} days; none yesterday or today`,
    },
  })

  // 7. Busiest day
  let peakDay = 0, peak = 0
  for (const [d, n] of days) if (n > peak || (n === peak && d > peakDay)) { peak = n; peakDay = d }
  const barsFrom = peakDay >= addDays(today, -29) ? Math.min(peakDay, addDays(today, -13)) : addDays(today, -13)
  const barsStart = Math.max(barsFrom, dayStart(first))
  const bars: number[] = []
  for (let d = barsStart; d <= today; d = addDays(d, 1)) bars.push(days.get(d) ?? 0)
  const peakIndex = peakDay >= barsStart ? daysBetween(barsStart, peakDay) - 1 : -1
  stats.push({
    key: 'busiest', label: 'Busiest day', value: whole.format(peak),
    sub: peak ? `${peak === 1 ? 'release' : 'releases'} on ${WEEKDAYS[new Date(peakDay).getDay()]}, ${dayMonth(peakDay)}` : `no releases ${sinceWhen.replace('over ', 'in ')}`,
    // Against the same pace the other chips use (≈ 14 a day), not calendar days.
    chips: !peak ? [] : peak > perDayRate * 1.05 ? [`${times(peak / perDayRate)} the daily average`] : ['about the daily average'],
    viz: { kind: 'days', days: bars, peak: peakIndex, peakLabel: dayName(peakDay), from: dayMonth(barsStart), aria: `Releases per day since ${dayMonth(barsStart)}; the busiest day was ${dayName(peakDay)} with ${peak}` },
  })
  return stats
}

// ---------- Cadence over a range ----------
export type RangeKey = '7d' | '14d' | '30d' | '90d' | '1y'
export interface RangeDef { key: RangeKey; short: string; title: string; unit: 'day' | 'week' | 'month'; count: number }
export const RANGES: readonly RangeDef[] = [
  { key: '7d', short: '7 days', title: 'Last 7 days', unit: 'day', count: 7 },
  { key: '14d', short: '14 days', title: 'Last 14 days', unit: 'day', count: 14 },
  { key: '30d', short: '30 days', title: 'Last 30 days', unit: 'day', count: 30 },
  { key: '90d', short: '90 days', title: 'Last 13 weeks', unit: 'week', count: 13 },
  { key: '1y', short: '1 year', title: 'Last 12 months', unit: 'month', count: 12 },
]
export const DEFAULT_RANGE: RangeKey = '7d'
export const rangeOf = (value: unknown): RangeKey => RANGES.some(r => r.key === value) ? value as RangeKey : DEFAULT_RANGE
// Where a person's chosen range is kept on this device.
export const releaseRangeKey = (principalId: string) => `aeon.release-history.range.${principalId}`

export interface Slot {
  // Stable calendar identity, independent of the slot's changing count or label.
  start: number
  // Short label under the bar ('' when the axis skips it) and the full one for the tooltip.
  label: string; full: string
  n: number
  // The first and last release in the slot, with their time ('' when none).
  first: string; last: string
  weekend: boolean
  // Entirely before the first release: drawn dashed, never as zero.
  before: boolean
  // Today, this week or this month: still filling.
  current: boolean
}
export interface Cadence {
  range: RangeDef; slots: Slot[]; total: number
  // The busiest slot (latest on a tie), or -1 with nothing to show.
  peak: number
  // Releases per slot at the pace since the range (or the first release) began; 0 without any.
  avg: number; avgLabel: string
  chips: string[]
  firstAt: number | null
  aria: string
}

export function cadence(releases: Release[], now: number, key: RangeKey): Cadence {
  const range = RANGES.find(r => r.key === key) ?? RANGES[0]!
  const all = datedReleases(releases)
  const firstAt = all.length ? all[0]!.at : null
  const N = range.count
  const startOf = (i: number) => range.unit === 'day' ? addDays(dayStart(now), i - N + 1)
    : range.unit === 'week' ? addDays(weekStart(now), 7 * (i - N + 1))
      : addMonths(monthStart(now), i - N + 1)
  const nowYear = new Date(now).getFullYear()
  const slots: Slot[] = Array.from({ length: N }, (_, i) => {
    const start = startOf(i), end = i === N - 1 ? Infinity : startOf(i + 1)
    const inside = all.filter(x => x.at >= start && x.at < end)
    const current = i === N - 1
    const date = new Date(start)
    const wd = date.getDay()
    let label: string, full: string, at: (x: Dated) => string
    if (range.unit === 'day') {
      full = current ? 'Today' : dayName(start)
      // A dense month labels every fifth day back from today; the first labelled one names its month.
      label = current ? 'Today' : N <= 14 ? `${WEEKDAYS[wd]} ${date.getDate()}` : (N - 1 - i) % 5 ? '' : (N - 1 - i) + 5 > N - 1 ? dayMonth(start) : String(date.getDate())
      at = x => `${nameOf(x.release)} ${clock(x.at)}`
    } else if (range.unit === 'week') {
      full = current ? 'This week' : `Week of ${dayMonth(start)}`
      label = current ? 'This week' : (N - 1 - i) % 2 ? '' : dayMonth(start)
      at = x => `${nameOf(x.release)} (${dayStart(x.at) === dayStart(now) ? 'today' : WEEKDAYS[new Date(x.at).getDay()]})`
    } else {
      const year = date.getFullYear() !== nowYear ? ` ${date.getFullYear()}` : ''
      full = current ? `${MONTHS_LONG[date.getMonth()]} so far` : `${MONTHS_LONG[date.getMonth()]}${year}`
      label = MONTHS[date.getMonth()]!
      at = x => `${nameOf(x.release)} (${dayMonth(x.at)})`
    }
    const firstRelease = inside[0], lastRelease = inside.length > 1 ? inside[inside.length - 1] : undefined
    return {
      start, label, full, n: inside.length, current,
      first: firstRelease ? at(firstRelease) : '', last: lastRelease ? at(lastRelease) : '',
      weekend: range.unit === 'day' && (wd === 0 || wd === 6),
      before: firstAt !== null && end !== Infinity && end <= firstAt,
    }
  })
  const total = slots.reduce((a, s) => a + s.n, 0)
  let peak = -1
  slots.forEach((s, i) => { if (s.n && (peak < 0 || s.n >= slots[peak]!.n)) peak = i })
  const rangeStart = startOf(0)
  const unitMs = range.unit === 'day' ? DAY : range.unit === 'week' ? WEEK : MONTH
  // At least a day, so the first hours of a history do not extrapolate wildly.
  const avg = firstAt === null || !total ? 0 : total / (Math.max(DAY, now - Math.max(rangeStart, firstAt)) / unitMs)
  const before = slots.some(s => s.before)
  const since = before && firstAt !== null ? ` since ${dayMonth(firstAt)}` : ''
  const peakChip = peak < 0 ? [] : [`peak ${slots[peak]!.n} · ${range.unit === 'day' ? dayName(startOf(peak)) : range.unit === 'week' ? `week of ${dayMonth(startOf(peak))}` : MONTHS_LONG[new Date(startOf(peak)).getMonth()]}`]
  const chips = !total ? [] : range.unit === 'day'
    ? [`≈ ${rate(avg)} a day${since}`, ...(since ? [] : [`≈ ${roughly(avg * 365.25)} a year at this pace`]), ...peakChip]
    : range.unit === 'week'
      ? [`≈ ${rate(avg)} a week${since}`, ...peakChip]
      : [`≈ ${roughly(avg)} a month at this pace`, ...(since && firstAt !== null ? [`first release ${dayMonth(firstAt)}`] : peakChip)]
  return {
    range, slots, total, peak, avg, avgLabel: avg ? `Ø ${roughly(avg)} a ${range.unit}` : '', chips, firstAt,
    aria: `${plural(total, 'release', 'releases')} in the ${range.title.toLowerCase()}`,
  }
}

// The y axis: 0 to a round top, three or four steps of 1, 2 or 5 times a power of ten.
export function axis(max: number): { top: number; step: number } {
  const target = Math.max(1, max * 1.12)
  const raw = target / 3
  const mag = 10 ** Math.floor(Math.log10(raw))
  const step = Math.max(1, [1, 2, 5, 10].map(m => m * mag).find(s => s >= raw) ?? 10 * mag)
  return { top: Math.max(step, Math.ceil(target / step) * step), step }
}
