// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { etaFromTicket, formatEta } from '../src/lib/eta.ts'

const now = Date.parse('2026-09-28T13:15:00Z')
const ready = { at: '2026-09-28T13:40:00Z', kind: 'Ready' as const, by: 'Ada', reported_at: '2026-09-28T13:10:00Z' }

test('estimates format as a relative duration and as a clock time', () => {
  const relative = formatEta({ ready, progress: 40 }, 'relative', now, 'Europe/Vienna')
  assert.equal(relative?.ready, '~25 min')
  assert.equal(relative?.progress, '40%')
  assert.equal(relative?.readyHover, null)
  assert.match(relative?.tip ?? '', /Ready · Ada/)
  const clock = formatEta({ ready }, 'clock', now, 'Europe/Vienna')
  assert.equal(clock?.ready, '15:40')
  const both = formatEta({ ready, live: { at: '2026-09-28T15:15:00Z', kind: 'Live', by: 'Beau' } }, 'both', now, 'Europe/Vienna')
  assert.equal(both?.ready, '~25 min')
  assert.equal(both?.readyHover, '15:40')
  assert.equal(both?.live, '~2 h')
  assert.equal(both?.liveHover, '17:15')
})

test('a past estimate is overdue and an unknown estimate stays empty', () => {
  const overdue = formatEta({ ready: { at: '2026-09-28T12:50:00Z', kind: 'Ready' } }, 'relative', now)
  assert.equal(overdue?.ready, '25 min overdue')
  assert.match(overdue?.ready ?? '', /overdue/)
  assert.equal(formatEta({ ready: { at: '2026-09-28T13:14:40Z', kind: 'Ready' } }, 'relative', now)?.ready, 'overdue')
  assert.equal(formatEta(null, 'relative', now), null)
  assert.equal(formatEta({}, 'relative', now), null)
  assert.equal(etaFromTicket({}), null)
  assert.equal(etaFromTicket({ progress_pct: 0 })?.progress, 0)
  assert.equal(etaFromTicket({ eta_ready_at: '2026-09-28T13:40:00Z', eta_stale: true })?.stale, true)
  assert.equal(formatEta(etaFromTicket({ eta_ready_at: '2026-09-28T13:40:00Z', eta_stale: true }), 'relative', now)?.stale, true)
})
