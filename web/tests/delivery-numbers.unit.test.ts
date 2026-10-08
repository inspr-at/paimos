// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1002 risks: a window is bucketed wrongly or extrapolated; days before the
// source covers them look measured; a change reads "better" when it is worse;
// the paired numbers (3, 7, 8, 10) count the wrong things; p90 squashes the chart.
import { expect, it, vi } from 'vitest'
vi.mock('../src/lib/api.ts', () => ({ api: async () => new Response('{}'), APIError: class extends Error {} }))
import {
  bucketsOf, chartModel, deltaOf, durationTop, nightStreak, readPrefs, regions, tileModel, TILES,
  type Metric, type MetricKey, type MetricWindow, type WindowDays,
} from '../src/lib/deliveryNumbers'
import { deliveryText } from '../src/lib/deliveryNumbersText'

const TODAY = '2026-10-08'
const day = (back: number) => new Date(Date.parse(`${TODAY}T00:00:00Z`) - back * 86_400_000).toISOString().slice(0, 10)
const coverage = (days: number, from: string | null, extra: Partial<MetricWindow['coverage']> = {}) => ({
  window_days: days, bucket: days <= 30 ? 'day' as const : days <= 180 ? 'week' as const : 'month' as const, buckets_total: days, buckets_covered: days, covered_days: days, from, full: true, ...extra,
})
function metric(key: MetricKey, fields: Partial<Metric> = {}, window: Partial<MetricWindow> = {}, days = 7): Metric {
  return {
    number: 1, key, label: key, unit: 'minutes', source: '', definition: '', status: 'ok', reason: null, latest: null, series: [], target: { value: 10, direction: 'max', note: '', source: 'Arion' },
    daily: Array.from({ length: 30 }, (_, index) => ({ date: day(29 - index), status: 'ok' as const, n: 2, value: 12, p50: 12, p90: 20 })),
    windows: [{ days, status: 'ok', n: 40, value: 12, p50: 12, p90: 20, coverage: coverage(days, day(days - 1)), previous: { status: 'ok', n: 30, value: 14, p50: 14, p90: 22, coverage: coverage(days, day(2 * days - 1)) }, ...window }],
    ...fields,
  }
}
const def = (key: string) => TILES.find(tile => tile.key === key)!
const text = deliveryText('en')

it('the page preferences fall back to Simple and 7 days and refuse anything else', () => {
  expect(readPrefs(null)).toEqual({ level: 'simple', window: 7 })
  expect(readPrefs({ level: 'expert', window: 365 })).toEqual({ level: 'expert', window: 365 })
  expect(readPrefs({ level: 'admin', window: 14 })).toEqual({ level: 'simple', window: 7 })
})

it('a change is steady within 10 %, 3 points for shares, and better or worse by the target’s direction', () => {
  const ctx = { days: 7, text, lang: 'en' as const, historyLoading: false }
  const at = (value: number, before: number) => metric('pr_ci_wall', {}, { value, previous: { status: 'ok', n: 1, value: before, p50: before, p90: before, coverage: coverage(7, day(13)) } }).windows[0]
  expect(deltaOf(def('pr_ci_wall'), at(16, 17), 'max', ctx)).toMatchObject({ cls: 'same', text: '−1 min', word: 'steady', icon: 'arrow-down' })
  expect(deltaOf(def('pr_ci_wall'), at(131, 164), 'max', ctx)).toMatchObject({ cls: 'better', text: '−33 min' })
  expect(deltaOf(def('time_to_first_green'), at(55, 48), 'max', ctx)).toMatchObject({ cls: 'worse', text: '+7 min' })
  expect(deltaOf(def('first_attempt_green'), at(35, 31), 'min', ctx)).toMatchObject({ cls: 'better', text: '+4 pts' })
  expect(deltaOf(def('first_attempt_green'), at(33, 31), 'min', ctx)).toMatchObject({ cls: 'same' })
  // No window before is no comparison, never a change from zero; a running backfill says so.
  const missing = at(16, 0); missing.previous = { ...missing.previous!, status: 'no_data', n: 0, value: null, coverage: coverage(7, null, { full: false }) }
  expect(deltaOf(def('pr_ci_wall'), missing, 'max', ctx)).toMatchObject({ cls: 'none', text: 'No data in the 7 days before' })
  expect(deltaOf(def('pr_ci_wall'), missing, 'max', { ...ctx, historyLoading: true }).text).toBe('History still loading: no comparison yet')
})

it('days before the source covers them are "no data yet", later empty days are not, and nothing is invented', () => {
  const daily = Array.from({ length: 30 }, (_, index) => {
    const date = day(29 - index)
    return date < day(3) ? { date, status: 'no_data' as const, n: 0, value: null, p50: null, p90: null }
      : date === day(1) ? { date, status: 'no_data' as const, n: 0, value: null, p50: null, p90: null }
        : { date, status: 'ok' as const, n: 3, value: 15, p50: 15, p90: 25 }
  })
  const subject = metric('pr_ci_wall', { daily }, { coverage: coverage(7, day(3), { full: false, covered_days: 4, buckets_covered: 4 }) })
  const { buckets, samples } = bucketsOf(subject, 7, 'en')
  expect(buckets.map(bucket => bucket.status)).toEqual(['no_data', 'no_data', 'no_data', 'ok', 'ok', 'empty', 'ok'])
  expect(buckets.map(bucket => bucket.label)).toEqual(['Fri 2', 'Sat 3', 'Sun 4', 'Mon 5', 'Tue 6', 'Wed 7', 'Today'])
  expect(samples).toHaveLength(7)
  expect(regions(buckets)).toEqual([{ start: 0, end: 3, status: 'no_data' }])
  const model = chartModel(def('pr_ci_wall'), new Map([['pr_ci_wall', subject]]), 7, null, 'en')
  expect(model.panels[0].lines[0].values).toEqual([null, null, null, 15, 15, null, 15])
  expect(model.readouts[0]).toBe('2 Oct · No data yet')
  expect(model.readouts[5]).toBe('7 Oct · Nothing recorded')
  expect(model.readouts[6]).toBe('Today · p50 15 min · p90 25 min · 3 runs')
  expect(model.foot).toBe('GitHub App + backfill · covers 4 of 7 days')
})

it('long windows use the server’s weeks and months and label only some of them', () => {
  const weeks = Array.from({ length: 13 }, (_, index) => ({ days: 90, from: day(Math.min(89, (12 - index) * 7 + 6)), to: day((12 - index) * 7), bucket: 'week' as const, status: 'ok' as const, n: 5, value: 10, p50: 10, p90: 12 }))
  const months = Array.from({ length: 13 }, (_, index) => ({ days: 365, from: `20${index < 3 ? '25' : '26'}-${String(((index + 9) % 12) + 1).padStart(2, '0')}-01`, to: '', bucket: 'month' as const, status: 'ok' as const, n: 1, value: 1, p50: 1, p90: 1 }))
  const subject = metric('pr_ci_wall', { series: [...weeks, ...months] }, { days: 90, coverage: coverage(90, day(89)) }, 90)
  subject.windows.push({ ...subject.windows[0], days: 365, coverage: coverage(365, day(364)) })
  const week = bucketsOf(subject, 90, 'en').buckets
  expect(week).toHaveLength(13)
  expect(week.filter(bucket => bucket.show)).toHaveLength(4)
  expect(week[12]).toMatchObject({ label: 'this wk', long: 'This week', show: true })
  const month = bucketsOf(subject, 365 as WindowDays, 'en').buckets
  expect(month[12].long).toBe('October (so far)')
  expect(month.filter(bucket => bucket.show).map(bucket => bucket.label)).toEqual(['Oct', 'Jan', 'Apr', 'Jul', 'Oct'])
})

it('the paired numbers count what they say: flakes, “changes”, scripted rounds and green nights', () => {
  const window = (key: MetricKey, n: number, value: number, unit: Metric['unit'] = 'percent') => [key, metric(key, { unit }, { n, value })] as const
  const metrics = new Map<MetricKey, Metric>([
    window('first_attempt_green', 168, 35), window('flaked_failures', 40, 24),
    window('review_time', 12, 13, 'minutes'), window('review_changes_share', 12, 58.3),
    window('merge_rounds_model_share', 10, 80),
  ])
  expect(tileModel(def('first_attempt_green'), metrics, 7, null, 'en')).toMatchObject({ value: { main: '35', unit: '%' }, lineB: '168 first attempts' })
  expect(tileModel(def('first_attempt_green'), metrics, 7, null, 'en').lineA.parts.map(part => part.text).join('')).toBe('Failed only by flakes 24%')
  expect(tileModel(def('review_time'), metrics, 7, null, 'en').lineB).toBe('“changes” verdicts 7 of 12 (58%)')
  const merge = tileModel(def('merge_rounds_model_share'), metrics, 7, null, 'en')
  expect(merge).toMatchObject({ value: { main: '2 of 10', unit: 'scripted' }, lineB: '10 merge rounds reported' })
  // Nightly: 4 red nights after 3 covered nights without a run; tonight's run is still due.
  const nights = Array.from({ length: 30 }, (_, index) => {
    const date = day(29 - index), back = 29 - index
    return back === 0 ? { date, status: 'no_data' as const, n: 0, value: null, p50: null, p90: null }
      : back <= 4 ? { date, status: 'ok' as const, n: 1, value: 0, p50: 0, p90: 0 }
        : { date, status: 'no_data' as const, n: 0, value: null, p50: null, p90: null }
  })
  const nightly = metric('nightly_green', { daily: nights, unit: 'percent' }, { n: 4, value: 0, coverage: coverage(7, day(6), { covered_days: 7 }) })
  nightly.windows.push({ ...nightly.windows[0], days: 30, coverage: coverage(30, day(6), { full: false }) })
  const tile = tileModel(def('nightly_green'), new Map([['nightly_green', nightly]]), 7, null, 'en')
  expect(tile.value).toEqual({ main: '0 of 4', unit: 'nights green' })
  expect(tile.lineA.parts[0].text).toBe('Red 4 in a row')
  expect(tile.lineA.squares).toEqual(['none', 'none', 'bad', 'bad', 'bad', 'bad', 'none'])
  expect(tile.lineB).toBe('3 nights without a run')
})

it('a run of nights ends at a night without a run; only tonight may still be due', () => {
  expect(nightStreak(['ok', 'bad', 'bad', 'none'])).toEqual({ verdict: 'bad', count: 2 })
  expect(nightStreak(['bad', 'none', 'ok', 'ok'])).toEqual({ verdict: 'ok', count: 2 })
  expect(nightStreak(['bad', 'none', 'none'])).toBeNull()
  expect(nightStreak([])).toBeNull()
})

it('days after coverage ends stay "no data yet", including a night after the App disconnects', () => {
  // The source covered day(6) through day(3), then the App disconnected. Later empty days are unobserved.
  const daily = Array.from({ length: 30 }, (_, index) => {
    const date = day(29 - index), back = 29 - index
    return back === 6 || back === 5
      ? { date, status: 'ok' as const, n: 2, value: 12, p50: 12, p90: 18 }
      : { date, status: 'no_data' as const, n: 0, value: null, p50: null, p90: null }
  })
  const span = coverage(7, day(6), { full: false, covered_days: 4, buckets_covered: 4 })
  const subject = metric('pr_ci_wall', { daily }, { coverage: span })
  expect(bucketsOf(subject, 7, 'en').buckets.map(bucket => bucket.status)).toEqual(['ok', 'ok', 'empty', 'empty', 'no_data', 'no_data', 'no_data'])
  const model = chartModel(def('pr_ci_wall'), new Map([['pr_ci_wall', subject]]), 7, null, 'en')
  expect(model.readouts[2]).toBe('4 Oct · Nothing recorded')
  expect(model.readouts[4]).toBe('6 Oct · No data yet')
  expect(model.readouts[6]).toBe('Today · No data yet')
  const nights = daily.map(point => {
    const back = Math.round((Date.parse(`${TODAY}T00:00:00Z`) - Date.parse(`${point.date}T00:00:00Z`)) / 86_400_000)
    if (back === 6) return { ...point, n: 1, value: 100, p50: 100, p90: 100 }
    if (back === 5) return { ...point, n: 1, value: 0, p50: 0, p90: 0 }
    return point
  })
  const nightly = metric('nightly_green', { daily: nights, unit: 'percent' }, { n: 2, value: 50, coverage: span })
  const strip = chartModel(def('nightly_green'), new Map([['nightly_green', nightly]]), 7, null, 'en')
  expect(strip.nights).toEqual(['ok', 'bad', 'none', 'none', 'nodata', 'nodata', 'nodata'])
  expect(strip.readouts[2]).toBe('4 Oct · No run')
  expect(strip.readouts[4]).toBe('6 Oct · No data yet')
  expect(strip.readouts[6]).toBe('Today · No data yet')
  // A week that only starts after the covered span is unobserved, even though its end is after the coverage start.
  const weeks = [
    { days: 90, from: day(20), to: day(14), bucket: 'week' as const, status: 'no_data' as const, n: 0, value: null, p50: null, p90: null },
    { days: 90, from: day(13), to: day(7), bucket: 'week' as const, status: 'no_data' as const, n: 0, value: null, p50: null, p90: null },
  ]
  const long = metric('pr_ci_wall', { series: weeks }, { days: 90, coverage: coverage(90, day(20), { full: false, covered_days: 7, buckets_covered: 1 }) }, 90)
  expect(bucketsOf(long, 90, 'en').buckets.map(bucket => bucket.status)).toEqual(['empty', 'no_data'])
})

it('p90 stretches the duration scale at most to twice its room, else it is clipped and named', () => {
  expect(durationTop([16, 17, 18], [30, 33], 8)).toEqual({ top: 40, clippedTo: null })
  expect(durationTop([55, 56], [692], 12)).toEqual({ top: 100, clippedTo: 692 })
  expect(durationTop([], [], 24)).toEqual({ top: 80, clippedTo: null })
})

it('German numbers use the German decimal comma and impersonal words', () => {
  const metrics = new Map<MetricKey, Metric>([['queue_runs_per_pr', metric('queue_runs_per_pr', { unit: 'runs' }, { n: 61, value: 1.8, p50: 2, p90: 3, previous: { status: 'ok', n: 50, value: 2.1, p50: 2, p90: 3, coverage: coverage(7, day(13)) } })]])
  const tile = tileModel(def('queue_runs_per_pr'), metrics, 7, null, 'de')
  expect(tile.value).toEqual({ main: '1,8', unit: 'Läufe' })
  expect(tile.delta).toMatchObject({ cls: 'better', text: '−0,3', word: 'besser' })
  expect(tile.lineB).toBe('61 gemergte PRs')
})
