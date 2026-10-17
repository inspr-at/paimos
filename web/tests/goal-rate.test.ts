// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1457 (r5 amendment): the Goals view's average rate, its closed signal line,
// the ETA popover's projection and the chart's fitted domain. Risk: a rate that
// counts history the goal never had, a unit that changes per range, or a projection
// shown where Tick's rules omit it (≤ 0.05 %/h, fewer than three values, done).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { availablePoints, averageRate, chartShape, fittedDomain, historySince, pointText, projectionText, rateText, signalText, stallText, type RatePoint } from '../src/lib/goalRate.ts'

const TZ = 'Europe/Vienna'
const HOUR = 3_600_000
// 24 hourly buckets ending in the one that holds "now" (Sat 17 Oct 2026, 10:30 Vienna = 08:30 UTC).
const NOW = Date.parse('2026-10-17T08:30:00Z')
const FIRST = Date.parse('2026-10-16T09:00:00Z')
function hourly(values: number[], start = FIRST, step = HOUR): RatePoint[] {
  return values.map((pct, i) => ({ t: new Date(start + i * step).toISOString(), pct, delta: pct - (i ? values[i - 1]! : pct) }))
}

test('the rate is the change over the window, per hour for 24 h and per day for 7 d and 30 d', () => {
  // 10 % at the window start, 29.6 % now: 19.6 points in 23.5 hours.
  const points = hourly([10, ...Array(10).fill(10), 15, 15, 15, 20, 20, 24, 24, 24, 24, 26, 26, 28, 29.6].slice(0, 24))
  const rate = averageRate(points, NOW)!
  assert.equal(rate.current, 29.6)
  assert.ok(Math.abs(rate.perHour - 19.6 / 23.5) < 1e-9, `per hour ${rate.perHour}`)
  assert.equal(signalText(rate, '24h', 'en'), 'Ø 0.8 %/h · 24 h')
  assert.equal(signalText(rate, '7d', 'en'), 'Ø 20.0 %/day · 7 d')
  assert.equal(signalText(rate, '30d', 'en'), 'Ø 20.0 %/day · 30 d')
  // The decimal follows the person's locale; Ø and the unit stay.
  assert.equal(rateText(rate, '24h', 'de-AT'), 'Ø 0,8 %/h')
  // The value before the first bucket counts as the start: a jump inside it is progress made.
  const jump = averageRate([{ t: new Date(FIRST).toISOString(), pct: 30, delta: 20 }, ...hourly([30, 30], FIRST + HOUR).slice(1)], FIRST + 2 * HOUR)!
  assert.equal(jump.perHour, 10)
  // Losing ground reads as a negative rate with a real minus sign; nothing reads as −0.0.
  assert.equal(rateText({ perHour: -0.4 }, '24h', 'en'), 'Ø −0.4 %/h')
  assert.equal(rateText({ perHour: -0.01 }, '24h', 'en'), 'Ø 0.0 %/h')
})

test('a goal younger than the window counts only from its creation and starts from what it held', () => {
  const createdAt = Date.parse('2026-10-17T05:40:00Z')
  // Pre-creation buckets read 0; the goal was created holding 30 % (work done before it).
  const points = hourly([...Array(20).fill(0), 30, 30, 40, 50])
  const shown = availablePoints(points, createdAt)
  assert.equal(shown.length, 4, 'from the bucket holding 05:40 UTC on')
  assert.equal(shown[0]!.t, '2026-10-17T05:00:00.000Z')
  const rate = averageRate(points, NOW, createdAt)!
  // 30 → 50 since 05:40, 2 h 50 min ago; the 30 % it was created with is not progress.
  assert.ok(Math.abs(rate.perHour - 20 / (170 / 60)) < 1e-9, `per hour ${rate.perHour}`)
  assert.equal(rate.from, createdAt)
  assert.equal(rate.values, 3, 'its start and two changes')
  // A creation before the window changes nothing.
  assert.deepEqual(availablePoints(points, FIRST - 5 * HOUR), points)
  assert.equal(historySince(createdAt, NOW, TZ), 'History since 07:40')
  assert.equal(historySince(createdAt - 2 * 24 * HOUR, NOW, TZ), 'History since Thu 15 Oct 07:40')
})

test('the projection lands at the pace shown, and is omitted where Tick’s rules say so', () => {
  const base = { from: FIRST, values: 3 }
  // 60 % now at 4 %/h: 10 hours to go from 10:30 → 20:30 today.
  assert.equal(projectionText({ ...base, perHour: 4, current: 60 }, '24h', NOW, false, { timeZone: TZ, locale: 'en' }), 'At Ø 4.0 %/h this lands ~20:30')
  // Rounded to five minutes: 40 points at 0.8 %/h is 50 h → Mon 19 Oct (past today: a date).
  assert.equal(projectionText({ ...base, perHour: 0.8, current: 60 }, '24h', NOW, false, { timeZone: TZ, locale: 'en' }), 'At Ø 0.8 %/h this lands ~Mon 19 Oct')
  // The unit follows the range.
  assert.equal(projectionText({ ...base, perHour: 0.8, current: 60 }, '7d', NOW, false, { timeZone: TZ, locale: 'en' }), 'At Ø 19.2 %/day this lands ~Mon 19 Oct')
  // More than 30 days out.
  assert.equal(projectionText({ ...base, perHour: 0.051, current: 0 }, '30d', NOW, false, { timeZone: TZ, locale: 'en' }), 'At Ø 1.2 %/day this lands beyond 30 days')
  // Omitted: at or below 0.05 %/h, with fewer than three values, once done, or at 100 %.
  assert.equal(projectionText({ ...base, perHour: 0.05, current: 60 }, '24h', NOW, false), null)
  assert.equal(projectionText({ ...base, perHour: -1, current: 60 }, '24h', NOW, false), null)
  assert.equal(projectionText({ ...base, values: 2, perHour: 4, current: 60 }, '24h', NOW, false), null)
  assert.equal(projectionText({ ...base, perHour: 4, current: 60 }, '24h', NOW, true), null)
  assert.equal(projectionText({ ...base, perHour: 4, current: 100 }, '24h', NOW, false), null)
  assert.equal(projectionText(null, '24h', NOW, false), null)
})

test('the stall line counts whole hours; the point line names the bucket and what changed', () => {
  assert.equal(stallText(NOW - 6.7 * HOUR, NOW), 'no progress for 6 h')
  const at = Date.parse('2026-10-17T11:00:00Z')
  assert.equal(pointText('24h', { t: new Date(at).toISOString(), pct: 20, delta: 7, changed_keys: ['AEON-1445', 'AEON-1441', 'AEON-1439'] }, TZ), '13:00 · 20 % (+7) · AEON-1445, AEON-1441 +1 changed')
  assert.equal(pointText('7d', { t: new Date(at - 7 * HOUR).toISOString(), pct: 13, delta: 0, changed_keys: [] }, TZ), 'Sat 06:00 · 13 % · no change')
  assert.equal(pointText('30d', { t: '2026-10-13T22:00:00Z', pct: 13, delta: -2, keys: ['OPS-1'] }, TZ), '14 Oct · 13 % (−2) · OPS-1 changed')
})

test('the chart fits its domain, keeps at least 20 points of height and starts where the goal did', () => {
  assert.deepEqual(fittedDomain([40, 60]), [35, 65])
  assert.deepEqual(fittedDomain([50, 52]), [41, 61], 'at least 20 points tall, centred')
  assert.deepEqual(fittedDomain([0, 3]), [0, 20], 'clamped at 0')
  assert.deepEqual(fittedDomain([98, 100]), [80, 100], 'clamped at 100')
  const shape = chartShape([null, null, 2, 2, 10])
  assert.equal(shape.first, 2)
  assert.match(shape.path, /^M39\.80 /, 'the line begins at the third bucket')
  assert.equal(shape.refs.length, 1, 'only the 0 line falls inside [0, 20]')
  assert.equal(shape.end.x, 95)
})
