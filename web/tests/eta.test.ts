// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { etaFromSession, etaFromTicket, formatEta } from '../src/lib/eta.ts'

const now = Date.parse('2026-09-28T13:15:00Z')
const ready = { at: '2026-09-28T13:40:00Z', kind: 'Ready' as const, by: 'Ada', reported_at: '2026-09-28T13:10:00Z' }

test('the headline is relative by default; the tooltip names the other form, who and when', () => {
  const relative = formatEta({ ready, progress: 40 }, 'relative', now, 'Europe/Vienna')
  assert.equal(relative?.text, '~25 min')
  assert.equal(relative?.kind, 'Ready')
  assert.equal(relative?.progress, '40%')
  assert.equal(relative?.hover, null)
  assert.equal(relative?.overdue, false)
  assert.equal(relative?.tip, 'Ready at 15:40, in ~25 min\nEstimated by Ada at 15:10\n40% done')
  const clock = formatEta({ ready }, 'clock', now, 'Europe/Vienna')
  assert.equal(clock?.text, '15:40')
  assert.match(clock?.tip ?? '', /in ~25 min/)
  const both = formatEta({ ready, live: { at: '2026-09-28T15:15:00Z', kind: 'Live', by: 'Beau' } }, 'both', now, 'Europe/Vienna')
  assert.equal(both?.text, '~25 min')
  assert.equal(both?.hover, '15:40')
  assert.match(both?.tip ?? '', /Live at 17:15, in ~2 h\nEstimated by Beau/)
})

test('only a live estimate leads with it; a clock time on another day names the day', () => {
  const live = formatEta({ live: { at: '2026-09-29T08:00:00Z', kind: 'Live' } }, 'clock', now, 'Europe/Vienna')
  assert.equal(live?.kind, 'Live')
  assert.equal(live?.text, 'Tue 10:00')
})

test('a past estimate is overdue and an unknown estimate stays empty', () => {
  const overdue = formatEta({ ready: { at: '2026-09-28T13:10:00Z', kind: 'Ready' } }, 'relative', now)
  assert.equal(overdue?.text, 'overdue 5 min')
  assert.equal(overdue?.overdue, true)
  assert.match(overdue?.tip ?? '', /^Ready was due at 13:10, overdue 5 min/)
  assert.equal(formatEta({ ready: { at: '2026-09-28T13:14:40Z', kind: 'Ready' } }, 'relative', now)?.text, 'due now')
  assert.equal(formatEta(null, 'relative', now), null)
  assert.equal(formatEta({}, 'relative', now), null)
  assert.equal(etaFromTicket({}), null)
  assert.equal(etaFromTicket({ progress_pct: 0 })?.progress, 0)
  const pctOnly = formatEta(etaFromTicket({ progress_pct: 0 }), 'relative', now)
  assert.equal(pctOnly?.text, null)
  assert.equal(pctOnly?.progress, '0%')
})

test('a stale report is flagged with its age and author', () => {
  assert.equal(etaFromTicket({ eta_ready_at: '2026-09-28T13:40:00Z', eta_stale: true })?.stale, true)
  assert.equal(formatEta(etaFromTicket({ eta_ready_at: '2026-09-28T13:40:00Z', eta_stale: true }), 'relative', now)?.stale, true)
  const session = etaFromSession({ eta_ready_at: '2026-09-28T13:40:00Z', eta_reported_at: '2026-09-28T12:41:00Z', eta_stale: true, agent: { name: 'wren' } })
  const view = formatEta(session, 'relative', now, 'UTC')
  assert.equal(view?.stale, true)
  assert.match(view?.tip ?? '', /^Estimate from 12:41 by wren is 34 min old\nReady at 13:40, in ~25 min/)
})
