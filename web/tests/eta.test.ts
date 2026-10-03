// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { etaFromSession, etaFromTicket, formatEta, progressAccessibleName, progressReportedAt } from '../src/lib/eta.ts'

const now = Date.parse('2026-09-28T13:15:00Z')
const ready = { at: '2026-09-28T13:40:00Z', kind: 'Ready' as const, by: 'Ada', reported_at: '2026-09-28T13:10:00Z' }

test('a stale connection dims the last estimate without asserting overdue in any display mode or tooltip', () => {
  const eta = { live: { ...ready, at: '2026-09-28T13:00:00Z', kind: 'Live' as const }, progress: 60 }
  for (const mode of ['relative', 'clock', 'both'] as const) {
    const view = formatEta(eta, mode, now, 'UTC', true)!
    assert.equal(view.text, 'Last 13:00')
    assert.equal(view.overdue, false)
    assert.equal(view.stale, true)
    assert.equal(view.hover, null)
    assert.match(view.tip, /Showing the last successful update/)
    assert.doesNotMatch(view.tip, /overdue \d/)
  }
  assert.equal(formatEta(eta, 'relative', now, 'UTC')?.overdue, true)
})

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

test('progress names the percent and, when the estimate is stale, when it was reported', () => {
  const at = Date.parse('2026-09-28T12:00:00Z')
  assert.equal(progressAccessibleName(45, false, '2026-09-28T12:10:00Z', at, 'Europe/Vienna'), '45% done')
  assert.equal(progressAccessibleName(45, true, '2026-09-28T12:10:00Z', at, 'Europe/Vienna'), '45% done, estimate stale since 14:10')
  assert.equal(progressAccessibleName(10, true, null, at, 'Europe/Vienna'), '10% done, estimate stale')
  assert.equal(progressReportedAt({ progress_pct: 10, ready_stale: true, ready_reported_at: '2026-09-28T12:10:00Z', eta_stale: true }), '2026-09-28T12:10:00Z')
  assert.equal(progressReportedAt({ progress_pct: 10, live_stale: true, live_reported_at: '2026-09-28T11:00:00Z', eta_stale: true }), '2026-09-28T11:00:00Z')
  assert.equal(progressReportedAt({ progress_pct: 10 }), null)
})

test('at 100% nothing is overdue or stale; only the server completion evidence reads Done (AEON-437)', () => {
  const late = { at: '2026-09-28T13:00:00Z', kind: 'Ready' as const, by: 'Beau', reported_at: '2026-09-28T12:00:00Z', stale: true }
  const finished = formatEta({ ready: late, progress: 100, stale: true, finished: { at: '2026-09-28T12:00:00Z', by: 'Beau' } }, 'relative', now, 'Europe/Vienna')
  assert.equal(finished?.done, true)
  assert.equal(finished?.text, null)
  assert.equal(finished?.overdue, false)
  assert.equal(finished?.stale, false)
  assert.equal(finished?.tip, 'All work reported by Beau at 14:00')
  assert.equal(formatEta({ progress: 100, finished: {} }, 'relative', now)?.tip, 'All work reported')
  // The server keeps no estimate for a finished ticket, so the evidence alone is enough to show Done.
  assert.equal(formatEta(etaFromTicket({ finished: true, finished_at: '2026-09-28T12:00:00Z', finished_by: 'Ada', progress_pct: 100 }), 'relative', now, 'Europe/Vienna')?.tip, 'All work reported by Ada at 14:00')
  assert.equal(formatEta(etaFromTicket({ finished: true }), 'relative', now)?.done, true)
  // 100% without that evidence is a full percent, never Done, with or without a working session:
  // a yielded or stopping session is not a working one, yet it is not gone either.
  for (const has_working_session of [true, false, undefined]) {
    const open = formatEta(etaFromTicket({ eta_ready_at: late.at, progress_pct: 100, has_working_session }), 'relative', now, 'Europe/Vienna')
    assert.deepEqual([open?.done, open?.progress, open?.text, open?.overdue, open?.stale, open?.tip], [false, '100%', null, false, false, '100% done'])
  }
  const behind = formatEta({ ready: late, progress: 99 }, 'relative', now, 'Europe/Vienna')
  assert.deepEqual([behind?.done, behind?.overdue, behind?.text], [false, true, 'overdue 15 min'])
  assert.equal(formatEta(etaFromSession({ eta_ready_at: late.at, progress_pct: 100 }), 'relative', now)?.done, false)
})
