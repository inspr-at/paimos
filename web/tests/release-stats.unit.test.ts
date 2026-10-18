// SPDX-License-Identifier: AGPL-3.0-only
// AEON-488: the release history header in numbers. Every stat, its chips and
// its visual's data, and the cadence over each range, on local calendar days.
import { describe, expect, it } from 'vitest'
import type { Release } from '../src/lib/releases'
import { axis, cadence, clockSince, distinctTickets, rangeOf, rate, releaseRangeKey, releaseStats, roughly, times, windowOf, type Stat } from '../src/lib/releaseStats'

const MIN = 60_000, HOUR = 60 * MIN, DAY = 24 * HOUR
const local = (m: number, d: number, h = 0, min = 0) => new Date(2026, m - 1, d, h, min).getTime()
let seq = 0
function rel(at: number, extra: Partial<Release> = {}): Release {
  const iso = new Date(at).toISOString()
  seq++
  return {
    version: `v${seq}`, tag: '', release_channel: 'stable', release_sequence: seq, codename: `Name ${seq}`, state: 'published',
    reserved_at: iso, tagged_at: iso, published_at: iso, headline: '', tickets: [], changes: [], changes_omitted: 0,
    evidence: { source_commit: '', source_url: '', image: null, ci: null, release_run: null, release_url: '', unavailable: [] }, ...extra,
  }
}
// A release that tells these tickets as features or fixes.
function told(at: number, items: [string, 'features' | 'fixes'][]): Release {
  return rel(at, { notes: { source: 'release-notes', snapshot_sha256: '', captured_at: null, release_revision: 1, gaps: [], hidden: 0,
    items: items.map(([key, group]) => ({ id: '', key, group, pill_en: `Pill ${key}`, pill_de: '', benefit_en: '', benefit_de: '' })) } })
}

// The canvas: AEON's first nine days. First release Wed 23 Sep 15:46, then
// 25, 16, 22, 11, 9, 13 and 5 a day, and one this morning at 09:56.
const NOW = local(10, 1, 13, 0) // Thursday
const DAYS: [number, number, number][] = [[9, 23, 11], [9, 24, 25], [9, 25, 16], [9, 26, 22], [9, 27, 11], [9, 28, 9], [9, 29, 13], [9, 30, 5], [10, 1, 1]]
function canvas(): Release[] {
  seq = 0
  const out: Release[] = []
  for (const [m, d, n] of DAYS) {
    const begin = m === 9 && d === 23 ? local(9, 23, 15, 46) : m === 10 ? local(10, 1, 9, 56) : local(m, d, 1, 0)
    const room = (m === 9 && d === 23 ? 8 * HOUR : 22 * HOUR) / n
    for (let i = 0; i < n; i++) out.push(rel(begin + Math.round(i * room)))
  }
  out.push(rel(local(9, 30, 20), { state: 'reserved', published_at: null }))
  return out
}
const byKey = (stats: Stat[]) => Object.fromEntries(stats.map(s => [s.key, s]))

describe('numbers as people say them', () => {
  it('rounds rates, large figures and multiples honestly', () => {
    expect([rate(100.3), rate(14.33), rate(7), rate(2.46), rate(0.04), rate(0)]).toEqual(['100', '14', '7', '2.5', '0.04', '0'])
    expect([rate(0.002747), rate(0.00000123)]).toEqual(['0.0027', '0.0000012'])
    expect([roughly(5235), roughly(4297), roughly(380.4), roughly(12.6)]).toEqual(['5,200', '4,300', '380', '13'])
    expect([times(1.99), times(3.04), times(3.2), times(1.43), times(2)]).toEqual(['2×', '3×', '≈ 3×', '≈ 1.4×', '2×'])
  })
  it('says when something started as short as the day allows', () => {
    expect(clockSince(local(10, 1, 12, 15), NOW)).toBe('12:15')
    expect(clockSince(local(9, 29, 8, 5), NOW)).toBe('Tue 08:05')
    expect(clockSince(local(9, 20, 8, 5), NOW)).toBe('20 Sep 08:05')
  })
})

describe('the seven stats', () => {
  const stats = releaseStats(canvas(), NOW)
  const s = byKey(stats)

  it('come in the card order, each with a value, a short sub-line and one or two chips', () => {
    expect(stats.map(x => x.key)).toEqual(['perweek', 'features', 'since', 'median', 'week', 'streak', 'busiest'])
    for (const x of stats) {
      expect(x.value).not.toBe('')
      expect(x.sub.length).toBeLessThanOrEqual(48)
      expect(x.chips.length).toBeGreaterThanOrEqual(1)
      expect(x.chips.length).toBeLessThanOrEqual(2)
    }
  })

  it('releases per week average since the first release while the history is shorter than 52 weeks', () => {
    // 113 published releases plus one reservation in 7 days 21 h 14 min.
    expect(s.perweek.value).toBe('101')
    expect(s.perweek.sub).toBe('on average since the first release, 23 Sep')
    expect(s.perweek.chips).toEqual(['≈ 14 a day', '≈ 5,300 a year at this pace'])
    const viz = s.perweek.viz
    expect(viz.kind).toBe('cumulative')
    if (viz.kind === 'cumulative') {
      expect(viz.points).toEqual([11, 36, 52, 74, 85, 94, 107, 113, 114])
      expect(viz.total).toBe(114)
    }
  })

  it('releases per week are a rolling 52-week average once the history is longer', () => {
    const now = local(10, 1, 12)
    // One release a week for two years: the window holds 52 of them.
    const releases = Array.from({ length: 104 }, (_, i) => rel(now - (i + 0.5) * 7 * DAY))
    expect(windowOf(now - 104 * 7 * DAY, now).short).toBe(false)
    const perweek = byKey(releaseStats(releases, now)).perweek
    expect(perweek.value).toBe('1')
    expect(perweek.sub).toBe('on average over the last 52 weeks')
    // Under one a day, the chip speaks per month: 52 a year is 4.3 a month.
    expect(perweek.chips).toEqual(['≈ 4.3 a month', '≈ 52 a year at this pace'])
  })

  it('features count each linked ticket once, at the release that first told it, apart from fixes', () => {
    const at = (h: number) => local(9, 28, h)
    const releases = [
      told(at(8), [['AEON-1', 'features'], ['AEON-2', 'features'], ['AEON-9', 'fixes']]),
      told(at(10), [['AEON-1', 'features'], ['AEON-3', 'features']]),
      // Told again as a fix: it stays the feature it was.
      told(at(12), [['AEON-2', 'fixes'], ['AEON-8', 'fixes']]),
    ]
    const list = releases.map(release => ({ release, at: Date.parse(release.published_at!) }))
    const t = distinctTickets(list)
    expect(t.features.length).toBe(3)
    expect(t.fixes.length).toBe(2)
    // Short history under a day: the pace counts a full day, 3 features × 7.
    const features = byKey(releaseStats(releases, at(13))).features
    expect(features.value).toBe('21')
    expect(features.sub).toBe('on average since 28 Sep')
    expect(features.chips).toEqual(['≈ 3 a day', '+ 14 fixes a week'])
    expect(features.viz.kind === 'stacked' && features.viz.bars).toEqual([{ features: 3, fixes: 2 }])
    // Releases that tell no tickets say so rather than "≈ 0 a day".
    expect(byKey(releaseStats([rel(at(8))], at(13))).features.chips).toEqual(['no feature tickets told yet'])
  })

  it('one feature over 52 weeks keeps its daily pace below a hundredth', () => {
    const releases = [rel(NOW - 53 * 7 * DAY), told(NOW - DAY, [['AEON-1', 'features']])]
    const features = byKey(releaseStats(releases, NOW)).features
    expect(features.value).toBe('0.019')
    expect(features.chips).toEqual(['≈ 0.0027 a day'])
  })

  it('since the last release names it and compares the wait with the median gap', () => {
    expect(s.since.value).toBe('3 h 4 min')
    expect(s.since.sub).toBe('Name 113, published today at 09:56')
    expect(s.since.chips).toHaveLength(1)
    expect(s.since.chips[0]).toMatch(/^(≈ )?\d+(\.\d)?× the median gap$/)
    const viz = s.since.viz
    if (viz.kind !== 'timeline') throw new Error(viz.kind)
    expect(viz.lastName).toBe('Name 113')
    expect(viz.gap).toBe('3 h 4 min')
    // 40 hours back: 30 Sep and 1 Oct begin inside the strip.
    expect(viz.marks.map(m => m.label)).toEqual(['30 Sep', '1 Oct'])
    expect(viz.last).toBeCloseTo(1 - (3 * HOUR + 4 * MIN) / (40 * HOUR), 5)
  })

  it('a release older than the timeline window has a bounded, explicitly older marker', () => {
    const since = byKey(releaseStats([rel(NOW - 30 * DAY)], NOW)).since
    const viz = since.viz
    if (viz.kind !== 'timeline') throw new Error(viz.kind)
    expect(viz.last).toBe(0)
    expect(viz.lastBeforeWindow).toBe(true)
    expect(viz.aria).toContain('before this window')
    expect(viz.gap).toBe('30 days')
    expect(viz.marks.every(m => m.at >= 0 && m.at <= 1)).toBe(true)
  })

  it('the median gap comes with the mean and the 90th percentile, and a histogram that marks it', () => {
    const now = local(10, 1, 12)
    // Gaps of 10, 20, 40, 50, 90 and 300 minutes.
    let at = now - 600 * MIN
    const releases = [rel(at)]
    for (const gap of [10, 20, 40, 50, 90, 300]) { at += gap * MIN; releases.push(rel(at)) }
    const median = byKey(releaseStats(releases, now)).median
    expect(median.value).toBe('45 min')
    expect(median.chips).toEqual(['mean 1 h 25 min', '9 in 10 within 5 h'])
    const viz = median.viz
    if (viz.kind !== 'histogram') throw new Error(viz.kind)
    expect(viz.buckets).toEqual([{ label: '<15m', n: 1 }, { label: '15–30m', n: 1 }, { label: '30–60m', n: 2 }, { label: '1–2h', n: 1 }, { label: '2–4h', n: 0 }, { label: '>4h', n: 1 }])
    expect(viz.median).toBeCloseTo(2.5, 5)
    expect(viz.medianLabel).toBe('median 45 min')
  })

  it('a single release has no gap yet', () => {
    const one = byKey(releaseStats([rel(local(10, 1, 9))], local(10, 1, 12)))
    expect(one.median.value).toBe('—')
    expect(one.median.chips).toEqual([])
    expect(one.since.chips).toEqual([])
  })

  it('this week counts since Monday, per day so far, and last week from the first release', () => {
    expect(s.week.value).toBe('29')
    expect(s.week.sub).toBe('releases since Monday')
    expect(s.week.chips).toEqual(['7.3 a day so far', 'last week 85 (from Wed)'])
    const viz = s.week.viz
    if (viz.kind !== 'week') throw new Error(viz.kind)
    expect(viz.days.map(d => d.n)).toEqual([9, 13, 6, 1, null, null, null])
    expect(viz.days.findIndex(d => d.today)).toBe(3)
    expect(viz.aria).toBe('Releases per day this week: Monday 9, Tuesday 13, Wednesday 6, Thursday 1 so far')
  })

  it('the streak runs every day since the first release, with its releases', () => {
    expect(s.streak.value).toBe('9 days')
    expect(s.streak.chips).toEqual(['every day since 23 Sep', '114 releases'])
    const viz = s.streak.viz
    if (viz.kind !== 'streak') throw new Error(viz.kind)
    expect(viz.days).toEqual([11, 25, 16, 22, 11, 9, 13, 6, 1])
    expect([viz.from, viz.to]).toEqual(['23 Sep', 'today'])
  })

  it('a streak survives a day that has not released yet, and names a longer one before it', () => {
    const now = local(10, 1, 9)
    const releases = [local(9, 20, 10), local(9, 21, 10), local(9, 22, 10), local(9, 23, 10), local(9, 29, 10), local(9, 30, 10), local(9, 30, 11)].map(t => rel(t))
    const streak = byKey(releaseStats(releases, now)).streak
    expect(streak.value).toBe('2 days')
    expect(streak.chips).toEqual(['every day since 29 Sep', 'longest 4 days'])
    expect(streak.viz.kind === 'streak' && streak.viz.to).toBe('yesterday')
    const broken = byKey(releaseStats(releases, local(10, 2, 9))).streak
    expect(broken.value).toBe('0 days')
    expect(broken.sub).toBe('no release yesterday or today')
  })

  it('the busiest day is the peak against the daily average', () => {
    expect(s.busiest.value).toBe('25')
    expect(s.busiest.sub).toBe('releases on Thu, 24 Sep')
    // Against the pace the other chips use, including the reservation.
    expect(s.busiest.chips).toEqual(['1.7× the daily average'])
    const viz = s.busiest.viz
    if (viz.kind !== 'days') throw new Error(viz.kind)
    expect(viz.days).toEqual([11, 25, 16, 22, 11, 9, 13, 6, 1])
    expect(viz.peak).toBe(1)
    expect(viz.peakLabel).toBe('Thu 24 Sep')
  })

  it('reservations count even without published versions, using their reservation time', () => {
    const releases = [rel(NOW - HOUR, { state: 'reserved', published_at: null, tagged_at: null })]
    const stats = byKey(releaseStats(releases, NOW))
    expect(stats.perweek.viz.kind === 'cumulative' && stats.perweek.viz.total).toBe(1)
    expect(stats.since.sub).toContain('reserved today')
    expect(cadence(releases, NOW, '7d').total).toBe(1)
    expect(releaseStats([], NOW)).toEqual([])
  })
})

describe('cadence over a range', () => {
  const releases = canvas()

  it('7 days: a bar a day, weekday labels, weekends marked, today last and still filling', () => {
    const c = cadence(releases, NOW, '7d')
    expect(c.slots.map(x => x.n)).toEqual([16, 22, 11, 9, 13, 6, 1])
    expect(c.slots.map(x => x.label)).toEqual(['Fri 25', 'Sat 26', 'Sun 27', 'Mon 28', 'Tue 29', 'Wed 30', 'Today'])
    expect(c.slots.map(x => x.weekend)).toEqual([false, true, true, false, false, false, false])
    expect(c.slots.at(-1)).toMatchObject({ current: true, full: 'Today', first: 'Name 113 09:56', last: '' })
    expect(c.slots[1]).toMatchObject({ full: 'Sat 26 Sep', first: 'Name 53 01:00', last: 'Name 74 22:00' })
    expect(c.total).toBe(78)
    expect(c.peak).toBe(1)
    // 78 in 6 days 13 hours, including the reservation: about 12 a day.
    expect(c.avgLabel).toBe('Ø 12 a day')
    expect(c.chips).toEqual(['≈ 12 a day', '≈ 4,400 a year at this pace', 'peak 22 · Sat 26 Sep'])
    expect(c.aria).toBe('78 releases in the last 7 days')
  })

  it('14 days: the days before the first release are named, not zero', () => {
    const c = cadence(releases, NOW, '14d')
    expect(c.slots.filter(x => x.before).map(x => x.label)).toEqual(['Fri 18', 'Sat 19', 'Sun 20', 'Mon 21', 'Tue 22'])
    expect(c.slots[5]).toMatchObject({ before: false, n: 11 })
    expect(c.total).toBe(114)
    expect(c.chips).toEqual(['≈ 14 a day since 23 Sep', 'peak 25 · Thu 24 Sep'])
  })

  it('30 days: every fifth day labelled back from today', () => {
    const c = cadence(releases, NOW, '30d')
    expect(c.slots).toHaveLength(30)
    expect(c.slots.map(x => x.label).filter(Boolean)).toEqual(['6 Sep', '11', '16', '21', '26', 'Today'])
  })

  it('90 days: thirteen weeks from Monday, this week last', () => {
    const c = cadence(releases, NOW, '90d')
    expect(c.slots).toHaveLength(13)
    expect(c.slots.slice(-2).map(x => [x.full, x.n])).toEqual([['Week of 21 Sep', 85], ['This week', 29]])
    expect(c.slots.at(-2)).toMatchObject({ first: 'Name 1 (Wed)', last: 'Name 85 (Sun)' })
    expect(c.slots.at(-1)!.last).toBe('Name 113 (today)')
    expect(c.slots.filter(x => x.before)).toHaveLength(11)
    expect(c.chips).toEqual(['≈ 101 a week since 23 Sep', 'peak 85 · week of 21 Sep'])
  })

  it('1 year: twelve months, the current one so far', () => {
    const c = cadence(releases, NOW, '1y')
    expect(c.slots.map(x => x.label)).toEqual(['Nov', 'Dec', 'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct'])
    expect(c.slots.slice(-2).map(x => [x.full, x.n])).toEqual([['September', 113], ['October so far', 1]])
    expect(c.slots[0]!.full).toBe('November 2025')
    expect(c.chips).toEqual(['≈ 440 a month at this pace', 'first release 23 Sep'])
  })

  it('a peak tie goes to the later slot; an empty history draws no average', () => {
    const now = local(10, 1, 12)
    const c = cadence([rel(local(9, 29, 9)), rel(local(9, 30, 9))], now, '7d')
    expect(c.peak).toBe(5)
    expect(cadence([], now, '7d')).toMatchObject({ total: 0, peak: -1, avg: 0, avgLabel: '', chips: [] })
  })

  it('the y axis steps by 1, 2 or 5 times a power of ten to a round top', () => {
    expect(axis(25)).toEqual({ top: 30, step: 10 })
    expect(axis(85)).toEqual({ top: 100, step: 50 })
    expect(axis(112)).toEqual({ top: 150, step: 50 })
    expect(axis(3)).toEqual({ top: 4, step: 2 })
    expect(axis(1)).toEqual({ top: 2, step: 1 })
    expect(axis(0)).toEqual({ top: 1, step: 1 })
  })

  it('a remembered range is one of the five; anything else is the default', () => {
    expect([rangeOf('90d'), rangeOf('2y'), rangeOf(null)]).toEqual(['90d', '7d', '7d'])
    expect(releaseRangeKey('p-1')).toBe('aeon.release-history.range.p-1')
  })
})

it('a candidate reservation is not reported as a tag or publication', () => {
  const candidate = rel(NOW - HOUR, { state: 'candidate', tagged_at: null, published_at: null })
  const since = releaseStats([candidate], NOW).find(stat => stat.key === 'since')!
  expect(since.sub).toContain('reserved')
  expect(since.sub).not.toContain('tagged')
  expect(since.sub).not.toContain('published')
})
