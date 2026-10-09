// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1037: the Resets card says only what the vendor reported and what the plan holds.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { RESET_PLAN_WIDEST, resetPlanLine, resetsSummary } from '../src/lib/accountResets.ts'

const TZ = 'Europe/Vienna'
const NOW = Date.parse('2026-09-29T12:02:00Z') // Tue 14:02 in Vienna
const credits = (...expires_at: string[]) => ({ count: expires_at.length, expires_at, source: 'vendor' as const })

test('the summary counts credits per expiry day and never hides later ones', () => {
  assert.equal(resetsSummary(credits(), NOW, TZ), 'No resets left')
  assert.equal(resetsSummary(credits('2026-10-04T08:00:00Z'), NOW, TZ), '1 reset · 1 expires Sun')
  assert.equal(resetsSummary(credits('2026-10-04T08:00:00Z', '2026-11-06T09:00:00Z'), NOW, TZ), '2 resets · 1 expires Sun, 1 on 6 Nov')
  assert.equal(resetsSummary(credits('2026-09-29T20:00:00Z', '2026-09-30T08:00:00Z', '2026-09-30T09:00:00Z'), NOW, TZ), '3 resets · 1 expires today, 2 tomorrow')
  assert.equal(resetsSummary(credits('2026-10-01T08:00:00Z', '2026-10-02T08:00:00Z', '2026-10-03T08:00:00Z', '2026-11-06T09:00:00Z', '2026-12-01T09:00:00Z'), NOW, TZ), '5 resets · 1 expires Thu, 1 on Fri, 1 on Sat, 2 later')
})

test('the plan line is honest about off, nothing planned and the planned moment', () => {
  const plan = { planned_at: '2026-10-03T16:00:00Z', raised_pace_points: 17.6, raised_pace_until: '2026-10-03T22:00:00Z' }
  assert.deepEqual(resetPlanLine('suggest', plan, NOW, TZ), { lead: '', rest: 'Off: PAIMOS only suggests.' })
  assert.match(resetPlanLine('auto_before_expiry', null, NOW, TZ).rest, /^On: nothing planned yet\./)
  assert.deepEqual(resetPlanLine('auto_before_expiry', plan, NOW, TZ), { lead: 'Planned: Sat ~18:00', rest: ', then +18 percentage points a day until Sun.' })
  assert.deepEqual(resetPlanLine('auto_before_expiry', { ...plan, raised_pace_points: 0 }, NOW, TZ), { lead: 'Planned: Sat ~18:00', rest: '.' })
})

test('the reserved plan slot is at least as wide as every line the card can show', () => {
  const [off, unplanned, planned] = RESET_PLAN_WIDEST
  assert.deepEqual(off, resetPlanLine('suggest', null, NOW, TZ))
  assert.deepEqual(unplanned, resetPlanLine('auto_before_expiry', null, NOW, TZ))
  for (const day of ['2026-09-29T20:00:00Z', '2026-09-30T08:00:00Z', '2026-10-03T16:00:00Z', '2026-11-06T09:00:00Z']) for (const points of [0, 1, 18, 100]) {
    const line = resetPlanLine('auto_before_expiry', { planned_at: day, raised_pace_points: points, raised_pace_until: day }, NOW, TZ)
    assert.ok(line.lead.length <= planned!.lead.length && line.rest.length <= planned!.rest.length, `${line.lead}${line.rest}`)
  }
})
