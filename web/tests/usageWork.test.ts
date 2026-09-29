// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildPools, buildRows, type AccountCapacity, type AccountInput } from '../src/lib/capacity.ts'
import { bandReadings, bandRows, breakdown, coverage, duration, since, sumTokens, tiles, tokenCoverage, wasteWords } from '../src/lib/usageWork.ts'
import { NOW, parts, usageDashboard } from './usage-data.ts'

test('durations read like a clock, never zero for a short session', () => {
  assert.equal(duration(30), '<1 min')
  assert.equal(duration(25 * 60), '25 min')
  assert.equal(duration(3590), '1 h')
  assert.equal(duration(4 * 3600 + 12 * 60), '4 h 12 min')
  assert.equal(duration(3 * 3600), '3 h')
  assert.equal(duration(612.4 * 3600), '612 h')
  assert.equal(duration(1234 * 3600), '1,234 h')
  assert.equal(since(new Date(NOW - 42 * 60_000).toISOString(), NOW), '42 min')
  assert.equal(since(new Date(NOW - 50 * 3600_000).toISOString(), NOW), '2 d')
})

test('coverage says how many reported, and never divides by nothing', () => {
  assert.equal(coverage(0, 357), 'reported by 0 of 357 sessions')
  assert.equal(coverage(12, 12), 'reported by all 12 sessions')
  assert.equal(coverage(1, 1), 'reported by the one session')
  assert.equal(coverage(349, 357, 'timed for'), 'timed for 349 of 357 sessions')
  assert.equal(coverage(0, 0), '')
  assert.equal(sumTokens([null, null]), null)
  assert.equal(sumTokens(['900000000000', null, '200000000000']), '1100000000000')
})

test('token coverage is per component, so a partial report never reads as complete', () => {
  const c = (sessions: number, reported: number, input: number, output: number, cached = 0, provisional = 0) => ({
    sessions, usage_reported_sessions: reported, ...parts('100', 0),
    input_reported_sessions: input, output_reported_sessions: output, cached_input_reported_sessions: cached, usage_provisional_sessions: provisional,
  })
  // Complete: the plain sentence.
  assert.equal(tokenCoverage(c(12, 12, 12, 12)), 'reported by all 12 sessions')
  assert.equal(tokenCoverage(c(7, 5, 5, 5)), 'reported by 5 of 7 sessions')
  assert.equal(tokenCoverage(c(7, 5, 5, 5, 5)), 'reported by 5 of 7 sessions')
  // Codex review repro: one complete session, one with input only, provisional.
  assert.equal(tokenCoverage(c(2, 1, 2, 1, 1, 1)), 'input from all 2 sessions, output and cached from 1 of 2, 1 still provisional')
  assert.equal(tokenCoverage(c(2, 1, 2, 1)), 'input from all 2 sessions, output from 1 of 2')
  assert.equal(tokenCoverage(c(7, 5, 5, 5, 2)), 'input and output from 5 of 7 sessions, cached from 2 of 7')
  assert.equal(tokenCoverage(c(3, 0, 1, 0)), 'input from 1 of 3 sessions, output from none')
  assert.equal(tokenCoverage(c(1, 0, 1, 0, 0, 1)), 'input from the one session, output from none, still provisional')
  assert.equal(tokenCoverage(c(4, 4, 4, 4, 0, 4)), 'reported by all 4 sessions, all still provisional')
  // Same counts from different sessions: only 3 reported both, so the components are named.
  assert.equal(tokenCoverage(c(5, 3, 4, 4)), 'input and output from 4 of 5 sessions')
  assert.equal(tokenCoverage(c(0, 0, 0, 0)), '')
})

test('a partial token total on the cost tile says what is missing', () => {
  const d = usageDashboard('reported')
  Object.assign(d.work, { input_reported_sessions: 230, output_reported_sessions: 212, usage_provisional_sessions: 3 })
  const cost = tiles(d)[1]
  assert.equal(cost.note, 'input from 230 of 357 sessions, output from 212 of 357, 3 still provisional')
  assert.match(cost.tip ?? '', /left out, not counted as zero/)
})

test('nothing reported: the cost tile becomes its reason, the other tiles stay figures', () => {
  const [done, cost, time, waste] = tiles(usageDashboard('unreported'))
  assert.deepEqual([done.value, done.label, done.detail, done.note], ['41', 'tickets done', '6 released · 1% rework', '64 tickets worked on'])
  assert.match(done.tip ?? '', /Rework: 3 of 280 deliveries were flagged/)
  assert.equal(cost.value, null)
  assert.equal(cost.label, 'Cost is not measured yet')
  assert.equal(cost.detail, 'reported by 0 of 357 sessions')
  assert.deepEqual([time.value, time.label, time.detail, time.note], ['612 h', 'agent time', 'across 357 sessions', 'timed for 349 of 357 sessions'])
  assert.equal(waste.value, '9')
  assert.equal(waste.detail.replace(/ /g, ' '), '1 stuck · 4 no commit · 2 lost contact · 1 failed · 1 retried')
  for (const tile of [done, cost, time, waste]) for (const text of [tile.value ?? '', tile.label, tile.detail, tile.note]) {
    assert.doesNotMatch(text, /^[-–—]$|Unknown|Not reported/, `${tile.key}: ${text}`)
  }
})

test('partly reported: tokens with their coverage, API spend as the only money', () => {
  const [, cost] = tiles(usageDashboard('reported'))
  assert.equal(cost.value, '100M')
  assert.equal(cost.label, 'tokens')
  assert.equal(cost.detail, '12.40 USD at API list price')
  assert.equal(cost.note, 'reported by 212 of 357 sessions')
  const noApi = usageDashboard('reported')
  noApi.totals.cost_known_rows = 0
  noApi.totals.estimated_cost_usd = null
  assert.equal(tiles(noApi)[1].detail, 'Claude 61.4M · Codex 38.9M')
})

test('a quiet range: zero is a known zero, and waste says none', () => {
  const d = usageDashboard('empty')
  const [done, , time, waste] = tiles(d)
  assert.equal(done.value, '0')
  assert.equal(done.note, '')
  d.ratings = undefined
  assert.equal(tiles(d)[0].detail, '')
  assert.equal(time.value, null)
  assert.equal(waste.value, '0')
  assert.equal(waste.detail, 'none stuck, failed or empty')
  assert.equal(d.work.days.length, 30)
})

test('waste words name the reason and the one place to fix it', () => {
  const rows = usageDashboard('unreported').work.waste.rows
  assert.deepEqual(wasteWords(rows[0]!, NOW), { title: 'Tried 4 times', detail: 'not done yet', action: 'ticket' })
  assert.deepEqual(wasteWords(rows[2]!, NOW), { title: 'Stuck', detail: 'silent for 42 min', action: 'session' })
  assert.equal(wasteWords(rows[1]!, NOW).title, 'Nothing to show')
  assert.equal(wasteWords(rows[5]!, NOW).title, 'Lost contact')
})

test('the model tab says how many sessions registered a model', () => {
  const w = usageDashboard('unreported').work
  assert.equal(breakdown(w, 'harness').note, '')
  assert.equal(breakdown(w, 'model').note, 'Model registered by 331 of 357 sessions.')
  assert.equal(breakdown(w, 'project').rows[0]!.label, 'Aeon')
})

function capacityRow(id: string, harness: string, windows: { kind: '5h' | 'weekly'; used: number; reset: string; today?: number; budget?: number }[]): { input: AccountInput; cap: AccountCapacity } {
  return {
    input: { id, label: id, harness, host: 'mbp', state: 'available', last_probe_ok: true },
    cap: {
      account_id: id, schedule: { timezone: 'UTC', week: Array.from({ length: 7 }, (_, i) => ({ on: i < 5, start: 8, end: 22 })), off_days: 'expire', nights: false, model: 'daynight', night: { start: 22, end: 8, k: 0.6 }, shifts: { early: 6, late: 14, night: 22, k: [1, 1, 0.5] }, blocks: [] },
      windows: windows.map(w => ({
        reading: { window_kind: w.kind, window_minutes: w.kind === '5h' ? 300 : 10080, used_percent: w.used, resets_at: w.reset, source: 'harness', read_at: new Date(NOW - 60_000).toISOString() },
        starts_at: new Date(NOW - 3600_000).toISOString(), allowance: 100, remaining_percent: 100 - w.used, freshness: 'fresh',
        pacing: { usable_hours: 40, percent_per_hour: 1, suggested_today_percent: (w.budget ?? 10) - (w.today ?? 0), budget_percent: w.budget ?? 10, used_today_percent: w.today ?? 0, period_start: '2026-09-29T06:00:00Z', period_end: '2026-09-30T06:00:00Z' },
      })),
    },
  }
}

test('capacity band: one pooled row per vendor, what resets first comes first', () => {
  const rows = [
    capacityRow('a', 'codex', [{ kind: 'weekly', used: 90, reset: '2026-10-02T07:00:00Z', today: 2, budget: 6 }]),
    capacityRow('b', 'codex', [{ kind: 'weekly', used: 40, reset: '2026-09-30T16:00:00Z', today: 4, budget: 14 }]),
    capacityRow('c', 'claude', [{ kind: 'weekly', used: 63, reset: '2026-10-04T09:00:00Z' }, { kind: '5h', used: 40, reset: '2026-09-29T14:40:00Z' }]),
    capacityRow('d', 'grok', []),
  ]
  const pools = buildPools(buildRows(rows.map(r => r.input), rows.map(r => r.cap)), NOW)
  const band = bandRows(pools, NOW)
  assert.deepEqual(band.map(r => r.pool.id), ['claude', 'codex', 'grok'])
  const [claude, codex, grok] = band
  // Claude has two window kinds, so its soonest reset names the 5-hour window.
  assert.equal(claude!.reset!.window, '5-hour')
  assert.equal(codex!.reset!.window, '')
  assert.equal(codex!.reset!.at, '2026-09-30T16:00:00Z')
  // The pool figure is the plain mean of its accounts, never one account's number.
  assert.equal(codex!.left, 35)
  assert.equal(codex!.accounts, 2)
  assert.deepEqual(codex!.today, { used: '3%', share: '~10%', ahead: false })
  assert.equal(grok!.left, null)
  assert.equal(grok!.gauge, null)
  assert.equal(grok!.reset, null)
})

test('capacity band: a pool measured by some of its accounts counts only those', () => {
  const rows = [
    capacityRow('a', 'codex', [{ kind: 'weekly', used: 40, reset: '2026-09-30T16:00:00Z' }]),
    capacityRow('b', 'codex', []),
  ]
  const [codex] = bandRows(buildPools(buildRows(rows.map(r => r.input), rows.map(r => r.cap)), NOW), NOW)
  assert.equal(codex!.accounts, 2)
  assert.equal(codex!.measured, 1)
  assert.equal(codex!.left, 60)
  assert.equal(bandReadings(codex!), 'readings from 1 of 2 accounts')
  assert.equal(bandReadings({ accounts: 2, measured: 2 }), '')
  assert.equal(bandReadings({ accounts: 2, measured: 0 }), '')
})
