// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { APIError, RequestFailure, StaleRequestError } from '../src/lib/api.ts'
import {
  accountPlan, confirmsSave, uncertainFailure, accountState, buildPools, buildRows, clampRate, dayProfile, daysLabel, defaultSchedule, fullPaceHours, gauge, gaugeModeFor,
  nightLabel, pickWindows, plainText, poolSentence, presetWeek, rateAt, sameShape, scheduleProblem, setGlobalMode, sourceLine, todayCell,
  toggleAccountMode, when, reserveLabel, reserveLevel, withReserve, workStart, zonedInstant, activeOverride, stripOverride,
  type AccountCapacity, type AccountInput, type CapacitySchedule, type CapacityWindow,
} from '../src/lib/capacity.ts'

const TZ = 'Europe/Vienna'
// Tue 29 Sep 2026 14:02 in Vienna (CEST, UTC+2).
const now = Date.parse('2026-09-29T12:02:00Z')
const iso = (s: string) => new Date(Date.parse(s)).toISOString()
const periodStart = iso('2026-09-29T06:00:00Z') // 08:00 local
const periodEnd = iso('2026-09-30T06:00:00Z')
const schedule = (fields: Partial<CapacitySchedule> = {}): CapacitySchedule => ({ ...defaultSchedule(TZ), ...fields })

function win(fields: { kind?: CapacityWindow['reading']['window_kind']; used: number; usedToday?: number; budget: number; reset: string; start?: string; tonight?: number; finish?: string; source?: 'harness' | 'agentd' | 'estimate'; readMin?: number; freshness?: CapacityWindow['freshness']; unused?: boolean; allowOff?: boolean; ahead?: boolean; plan?: string; kept?: number; keptUntil?: string; level?: number }): CapacityWindow {
  const usedToday = fields.usedToday ?? 0
  return {
    reading: { window_kind: fields.kind ?? 'weekly', window_minutes: 10080, used_percent: fields.used, resets_at: iso(fields.reset), source: fields.source ?? 'harness', read_at: new Date(now - (fields.readMin ?? 2) * 60_000).toISOString(), plan: fields.plan },
    starts_at: iso(fields.start ?? '2026-09-25T07:14:00Z'), allowance: 100, remaining_percent: 100 - fields.used, freshness: fields.freshness ?? 'fresh',
    pacing: {
      usable_hours: 40, percent_per_hour: 1, suggested_today_percent: Math.max(0, fields.budget - usedToday), budget_percent: fields.budget, used_today_percent: usedToday,
      tonight_percent: fields.tonight ?? 0, period_start: periodStart, period_end: periodEnd, finish: fields.finish ?? null, unused: fields.unused, allow_off: fields.allowOff, ahead: fields.ahead,
      ...(fields.kept !== undefined ? { reserve_percent: fields.level ?? 30, reserve_effective_percent: fields.kept, ...(fields.keptUntil ? { reserve_until: iso(fields.keptUntil) } : {}) } : {}),
    },
  }
}
const acct = (id: string, label: string, harness: string, fields: Partial<AccountInput> = {}): AccountInput => ({ id, label, harness, host: 'mbp2607', state: 'available', last_probe_ok: true, ...fields })
const cap = (id: string, windows: CapacityWindow[], s = schedule()): AccountCapacity => ({ account_id: id, schedule: s, windows })
const pools = (accounts: AccountInput[], capacity: AccountCapacity[]) => buildPools(buildRows(accounts, capacity), now)

test('presets, custom weeks and night labels read like the prototype', () => {
  assert.deepEqual(daysLabel(presetWeek(5)), { preset: 5, custom: 'Custom · 5 days', hint: 'Mon–Fri' })
  assert.equal(daysLabel(presetWeek(7)).hint, 'every day')
  const custom = presetWeek(7).map((d, i) => ({ ...d, on: i >= 1 && i <= 3, end: i === 3 ? 14 : 22 }))
  assert.deepEqual(daysLabel(custom), { preset: null, custom: 'Custom · 3 days', hint: 'Tue–Thu · own hours' })
  assert.equal(daysLabel(presetWeek(7).map((d, i) => ({ ...d, on: i === 0 || i === 4 }))).hint, 'Mon, Fri')
  // Hours of days that are off do not break a preset.
  const off = presetWeek(5); off[6] = { on: false, start: 3, end: 5 }
  assert.equal(daysLabel(off).preset, 5)
  assert.equal(nightLabel(schedule()), '22–08')
  assert.equal(nightLabel(schedule({ night: { start: 18, end: 6, k: 0.5 } })), '18–06')
  assert.equal(nightLabel(schedule({ model: 'shifts' })), '3 shifts')
  assert.equal(nightLabel(schedule({ model: 'blocks' })), 'Custom')
})

test('the day shape follows work hours, nights, shifts and blocks', () => {
  const s = schedule()
  assert.equal(rateAt(s, 1, 9), 1)
  assert.equal(rateAt(s, 1, 23), 0)
  const nights = schedule({ nights: true })
  assert.equal(rateAt(nights, 1, 23), 0.6)
  assert.equal(rateAt(nights, 1, 3), 0.6, 'a night belongs to the work day it starts on')
  assert.equal(rateAt(nights, 0, 3), 0, 'Monday 03:00 belongs to Sunday night, a day off')
  const shifts = schedule({ nights: true, model: 'shifts', shifts: { early: 6, late: 14, night: 22, k: [1, 0.8, 0.4] } })
  assert.deepEqual([rateAt(shifts, 1, 7), rateAt(shifts, 1, 15), rateAt(shifts, 1, 23), rateAt(shifts, 1, 3)], [1, 0.8, 0.4, 0.4])
  assert.equal(rateAt(shifts, 5, 10), 0, 'shifts only run on work days')
  const blocks = schedule({ nights: true, model: 'blocks', blocks: Array.from({ length: 24 }, (_, h) => (h < 6 ? 0.3 : 1)) })
  assert.equal(rateAt(blocks, 2, 2), 0.3)
  assert.equal(fullPaceHours(schedule(), 1), 14)
  assert.equal(Math.round(fullPaceHours(nights, 1) * 10) / 10, 20)
  // "Use what would expire" hatches days off that could still spend capacity.
  assert.ok(dayProfile(schedule(), 5).some(c => c.expire))
  assert.ok(!dayProfile(schedule({ off_days: 'rest' }), 5).some(c => c.expire))
})

test('schedule validation and pace steps match the server', () => {
  assert.equal(scheduleProblem({ ...schedule(), week: presetWeek(0) }, 'week'), 'Pick at least one day.')
  assert.equal(scheduleProblem(schedule({ nights: true, night: { start: 22, end: 22, k: 0.6 } }), 'night'), 'Night needs a start and an end.')
  assert.equal(scheduleProblem(schedule({ nights: true, model: 'shifts', shifts: { early: 6, late: 22, night: 14, k: [1, 1, 1] } }), 'night'), 'Shifts must run early, late, night.')
  assert.equal(scheduleProblem(schedule({ nights: true, model: 'shifts' }), 'night'), '')
  assert.deepEqual([0, 0.04, 0.05, 0.5, 0.94, 1].map(clampRate), [0, 0.1, 0.1, 0.5, 0.9, 1])
  assert.ok(sameShape({ ...schedule(), override: 'sprint', override_until: periodEnd }, schedule()))
  // The zone does not make a Hold carrier a deliberate schedule (server rule).
  assert.ok(sameShape({ ...schedule(), timezone: 'UTC', override: 'hold' }, schedule()))
  assert.ok(!sameShape({ ...schedule(), week: presetWeek(3) }, schedule()))
})

test('times read as a clock: today, tomorrow, weekday, date', () => {
  assert.equal(when('2026-09-29T14:40:00Z', now, TZ), '16:40')
  assert.equal(when('2026-09-30T16:02:00Z', now, TZ), 'tomorrow 18:02')
  assert.equal(when('2026-10-02T07:14:00Z', now, TZ), 'Fri 09:14')
  assert.equal(when('2026-10-06T11:10:00Z', now, TZ), 'Tue 6 Oct')
})

test('the long window is the bar and the 5-hour window the small line', () => {
  const weekly = win({ used: 63, budget: 10, reset: '2026-10-04T09:00:00Z' })
  const five = win({ kind: '5h', used: 40, budget: 60, reset: '2026-09-29T14:40:00Z' })
  const opus = { ...win({ used: 90, budget: 5, reset: '2026-10-04T09:00:00Z' }), reading: { ...weekly.reading, bucket: 'seven_day_opus' } }
  assert.deepEqual(pickWindows([five, weekly, opus]), { primary: weekly, five })
  assert.deepEqual(pickWindows([five]), { primary: five, five: null })
})

test('account states: sign-in only on a confirmed sign-out; other failed checks are quiet', () => {
  assert.equal(accountState(acct('a', 'x', 'codex', { last_probe_ok: false, probeFailure: 'auth_failed' }), true), 'signin')
  // AEON-299 review: timeouts, missing binaries and unreadable output are not a sign-out.
  assert.equal(accountState(acct('a', 'x', 'codex', { last_probe_ok: false }), true), 'unavailable')
  assert.equal(accountState(acct('a', 'x', 'codex', { last_probe_ok: false, probeFailure: 'unavailable' }), true), 'unavailable')
  assert.equal(accountState(acct('a', 'x', 'codex', { state: 'unavailable', last_probe_ok: false }), true), 'unavailable')
  // A stale auth_failed after a later successful probe is not a sign-in.
  assert.equal(accountState(acct('a', 'x', 'codex', { last_probe_ok: true, probeFailure: 'auth_failed' }), true), 'live')
  assert.equal(accountState(acct('a', 'x', 'codex', { last_probe_ok: null }), true), 'live')
  assert.equal(accountState(acct('a', 'x', 'codex', { connectivity: 'offline' }), true), 'offline')
  assert.equal(accountState(acct('a', 'x', 'codex', { state: 'draining' }), true), 'paused')
  assert.equal(accountState(acct('a', 'x', 'codex'), false), 'unread')
  assert.equal(accountState(acct('a', 'x', 'codex'), true), 'live')
})

test('two Codex accounts: soonest reset first, one plan sentence, gauges from server pacing', () => {
  const [codex] = pools(
    [acct('main', 'Main', 'codex', { plan: 'Pro' }), acct('spare', 'Spare', 'codex', { plan: 'Pro' }), acct('studio', 'Studio', 'codex', { host: 'studio', connectivity: 'offline', plan: 'Pro' })],
    [cap('main', [win({ used: 58, usedToday: 3, budget: 15, reset: '2026-10-02T07:14:00Z' })]), cap('spare', [win({ used: 91, usedToday: 2, budget: 6, reset: '2026-09-30T16:02:00Z', source: 'harness', readMin: 9 })]), cap('studio', [win({ used: 22, budget: 0, reset: '2026-10-05T05:40:00Z', source: 'agentd', readMin: 180, freshness: 'stale' })])],
  )
  assert.deepEqual(codex.rows.map(r => r.name), ['Spare', 'Main', 'Studio'])
  assert.equal(codex.plan, 'Pro · weekly')
  assert.equal(plainText(poolSentence(codex, now, TZ)), 'Today: ~6% of Spare, then ~15% of Main — soonest reset first, so each lands at 0% as it resets.')
  const spare = codex.rows[0]
  const plan = accountPlan(spare, now)!
  assert.deepEqual(gauge(spare, plan), { yours: 0, later: 5, today: 4, spent: 2, tick: 5, frozen: false })
  assert.deepEqual(todayCell(spare, plan), { kind: 'share', value: '~6%', tip: 'Plan for today ~6%: 2% used, 4% to go' })
  assert.equal(sourceLine(spare, now), 'Codex reported · 9 min ago')
  const studio = codex.rows[2]
  assert.equal(sourceLine(studio, now), 'Read on studio · 3 h ago · offline')
  assert.deepEqual(todayCell(studio, accountPlan(studio, now)), { kind: 'quiet', text: 'waits for studio' })
  assert.equal(gauge(studio, accountPlan(studio, now)).tick, null)
})

test('one account: by day and tonight, finish before reset, fresh week', () => {
  const nights = schedule({ nights: true })
  const [claude] = pools([acct('c', 'markus', 'claude')], [cap('c', [win({ used: 63, usedToday: 4, budget: 10, tonight: 2, reset: '2026-10-04T09:00:00Z', finish: '2026-10-02T20:00:00Z' })], nights)])
  assert.equal(plainText(poolSentence(claude, now, TZ)), 'Today: use up to ~8% by day and ~2% tonight (4% so far) — on track to finish at 0% by Fri 22:00, before it resets Sun 11:00.')
  const [grok] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 0, budget: 13, reset: '2026-10-06T11:10:00Z', start: '2026-09-29T11:10:00Z', finish: '2026-10-06T10:00:00Z' })])])
  assert.equal(plainText(poolSentence(grok, now, TZ)), 'Today: use up to ~13% of Grok (fresh week) — on track to finish at 0% right as it resets Tue 6 Oct.')
})

test('ahead of pace is gold, not red, and says what happens next', () => {
  const [cursor] = pools([acct('u', 'markus', 'cursor')], [cap('u', [win({ kind: 'monthly', used: 43, usedToday: 7, budget: 6, reset: '2026-10-14T07:00:00Z', ahead: true })])])
  const s = poolSentence(cursor, now, TZ)
  assert.equal(s.ahead, true)
  assert.equal(plainText(s), 'Ahead of pace: 7% used today, the plan was ~6%. Agents ease off Cursor until tomorrow.')
  assert.deepEqual(todayCell(cursor.rows[0], accountPlan(cursor.rows[0], now)), { kind: 'ahead', used: '7%', plan: '~6%' })
})

test('Sprint and Hold come from the pool schedule; an expired Sprint is the plan again', () => {
  const sprint = schedule({ override: 'sprint', override_until: '2026-09-30T16:02:00Z' })
  const [codex] = pools(
    [acct('spare', 'Spare', 'codex'), acct('main', 'Main', 'codex')],
    [cap('spare', [win({ used: 91, budget: 9, reset: '2026-09-30T16:02:00Z' })], sprint), cap('main', [win({ used: 58, budget: 42, reset: '2026-10-02T07:14:00Z' })], sprint)],
  )
  assert.equal(codex.override, 'sprint')
  assert.equal(plainText(poolSentence(codex, now, TZ)), 'Sprint: agents may use everything left on Spare (9%) and Main (42%) until tomorrow 18:02.')
  assert.deepEqual(todayCell(codex.rows[0], accountPlan(codex.rows[0], now)), { kind: 'sprint', text: 'all 9%' })
  const [held] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 10, budget: 0, reset: '2026-10-06T11:10:00Z' })], schedule({ override: 'hold' }))])
  assert.equal(plainText(poolSentence(held, now, TZ)), "On hold. Agents leave Grok alone until you resume; today's share moves to the coming days.")
  assert.equal(gauge(held.rows[0], accountPlan(held.rows[0], now)).tick, null)
  const [expired] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 10, budget: 12, reset: '2026-10-06T11:10:00Z' })], schedule({ override: 'sprint', override_until: '2026-09-29T10:00:00Z' }))])
  assert.equal(expired.override, '')
})

test('days off: rest, would expire, and unused capacity', () => {
  const sunday = presetWeek(5).map((d, i) => ({ ...d, on: i !== 1 })) // Tuesday off
  const [rest] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 30, budget: 0, reset: '2026-10-06T11:10:00Z' })], schedule({ week: sunday, off_days: 'rest' }))])
  assert.equal(plainText(poolSentence(rest, now, TZ)), 'Day off: agents rest. Grok continues tomorrow.')
  assert.deepEqual(todayCell(rest.rows[0], accountPlan(rest.rows[0], now)), { kind: 'quiet', text: 'day off' })
  const [expire] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 30, budget: 70, reset: '2026-09-29T20:00:00Z', allowOff: true })], schedule({ week: sunday }))])
  assert.equal(plainText(poolSentence(expire, now, TZ)), 'Day off, but ~70% of Grok would expire before your next work day, so agents use it today.')
  const [lost] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 30, budget: 0, reset: '2026-09-29T20:00:00Z', unused: true })], schedule({ week: sunday, off_days: 'rest' }))])
  assert.equal(plainText(poolSentence(lost, now, TZ)), 'Day off. 70% of Grok resets before your next work day and goes unused. Sprint to use it.')
  const [today] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 30, budget: 70, reset: '2026-09-29T20:00:00Z' })])])
  assert.equal(plainText(poolSentence(today, now, TZ)), 'Today: use all 70% left. Grok resets at 22:00.')
})

test('sign-in and offline pools pause with the one fixing step', () => {
  const [cursor] = pools([acct('u', 'markus', 'cursor', { last_probe_ok: false })], [{ ...cap('u', [win({ kind: 'monthly', used: 43, budget: 6, reset: '2026-10-14T07:00:00Z', readMin: 2 * 24 * 60 })]), probe_failure: 'auth_failed' }])
  assert.equal(plainText(poolSentence(cursor, now, TZ)), 'Paused until you sign in again on mbp2607. 57% left, resets Wed 14 Oct.')
  assert.deepEqual(todayCell(cursor.rows[0], null), { kind: 'signin', command: 'cursor-agent login' })
  assert.equal(sourceLine(cursor.rows[0], now), 'Sign-in expired · last read 2 d ago')
  const [quiet] = pools([acct('g', 'markus', 'grok', { last_probe_ok: false })], [{ ...cap('g', [win({ used: 10, budget: 12, reset: '2026-10-06T11:10:00Z' })]), probe_failure: 'unavailable' }])
  assert.equal(plainText(poolSentence(quiet, now, TZ)), 'Reading unavailable on mbp2607. Agents skip it until the next check succeeds.')
  assert.deepEqual(todayCell(quiet.rows[0], accountPlan(quiet.rows[0], now)), { kind: 'quiet', text: 'reading unavailable' })
  assert.equal(sourceLine(quiet.rows[0], now), 'Codex reported · 2 min ago'.replace('Codex', 'Grok'))
  const [none] = pools([acct('p', 'Pi on hsb1', 'pi')], [])
  assert.equal(plainText(poolSentence(none, now, TZ)), 'No reading yet — starts with the first run.')
  assert.equal(sourceLine(none.rows[0], now), 'No reading yet — starts with the first run')
  const [next] = pools([acct('p', 'Pi on hsb1', 'pi')], [{ ...cap('p', []), awaiting_reading: true }])
  assert.equal(plainText(poolSentence(next, now, TZ)), 'No reading yet — starts with the next run.')
  assert.equal(sourceLine(next.rows[0], now), 'No reading yet — starts with the next run')
})

test('gauges show % left or % used, globally and per account', () => {
  assert.equal(gaugeModeFor(null, 'a'), 'left')
  const used = toggleAccountMode(null, 'a')
  assert.deepEqual(used, { mode: 'left', accounts: { a: 'used' } })
  assert.equal(gaugeModeFor(used, 'a'), 'used')
  assert.equal(gaugeModeFor(used, 'b'), 'left')
  assert.deepEqual(toggleAccountMode(used, 'a'), { mode: 'left', accounts: {} })
  const global = setGlobalMode(used, 'used')
  assert.deepEqual(global, { mode: 'used', accounts: {} })
  assert.deepEqual(toggleAccountMode(global, 'b'), { mode: 'used', accounts: { b: 'left' } })
})

test('a Sprint ends at the pool\'s earliest limiting reset, 5-hour windows included', () => {
  const weekly = win({ used: 63, budget: 10, reset: '2026-10-04T09:00:00Z' })
  const [claude] = pools([acct('c', 'markus', 'claude')], [{ ...cap('c', [weekly]), limiting_reset: '2026-09-29T14:40:00Z' }])
  assert.equal(claude.sprintEnd, '2026-09-29T14:40:00Z')
  assert.equal(when(claude.sprintEnd, now, TZ), '16:40')
  const [codex] = pools([acct('a', 'Spare', 'codex'), acct('b', 'Main', 'codex')], [
    { ...cap('a', [win({ used: 91, budget: 9, reset: '2026-09-30T16:02:00Z' })]), limiting_reset: '2026-09-30T16:02:00Z' },
    { ...cap('b', [win({ used: 58, budget: 42, reset: '2026-10-02T07:14:00Z' })]), limiting_reset: '2026-09-29T13:00:00Z' },
  ])
  assert.equal(codex.sprintEnd, '2026-09-29T13:00:00Z')
})

test('a lost save answer is uncertain until the server confirms what it has', () => {
  assert.ok(uncertainFailure(new RequestFailure('network')))
  assert.ok(uncertainFailure(new RequestFailure('timeout')))
  assert.ok(uncertainFailure(new StaleRequestError('gap')))
  assert.ok(uncertainFailure(new APIError(504, 'gateway timeout')))
  assert.ok(!uncertainFailure(new APIError(400, 'invalid capacity schedule')))
  assert.ok(!uncertainFailure(new APIError(500, 'database unavailable')))
  const sent = { ...schedule(), week: presetWeek(7) }
  assert.ok(confirmsSave([{ scope: 'user', schedule: sent }, { scope: 'pool', pool: 'grok', schedule: { ...schedule(), override: 'hold' } }], sent))
  assert.ok(!confirmsSave([{ scope: 'user', schedule: schedule() }], sent))
  assert.ok(!confirmsSave([], sent))
  assert.ok(!confirmsSave([{ scope: 'user', schedule: { ...sent, timezone: 'UTC' } }], sent))
})

// ---------- Keep for you (AEON-375) ----------
test('Keep for you: labels, levels and the yours segment at the 0 end', () => {
  assert.equal(reserveLabel(null), 'Auto · ~30%')
  assert.equal(reserveLabel({ reserve: 'auto' }), 'Auto · ~30%')
  assert.equal(reserveLabel({ reserve: 'fixed', reserve_percent: 45 }), '45%')
  assert.equal(reserveLabel({ reserve: 'off' }), 'Nothing')
  assert.deepEqual([reserveLevel(null), reserveLevel({ reserve: 'fixed', reserve_percent: 20 }), reserveLevel({ reserve: 'off' })], [30, 20, 0])
  assert.deepEqual(withReserve(schedule({ reserve: 'fixed', reserve_percent: 20 }), 'off').reserve_percent, undefined)
  assert.equal(withReserve(schedule(), '').reserve, undefined)
  // Main: 42% left, 30% kept, today 15 of which 3 used: [yours 30][later 0][today 12].
  const [codex] = pools([acct('main', 'Main', 'codex')], [cap('main', [win({ used: 58, usedToday: 3, budget: 15, reset: '2026-10-02T07:14:00Z', kept: 30 })])])
  const plan = accountPlan(codex.rows[0], now)!
  assert.deepEqual(gauge(codex.rows[0], plan), { yours: 30, later: 0, today: 12, spent: 3, tick: 30, frozen: false })
  assert.equal(plainText(poolSentence(codex, now, TZ)), 'Today: use up to ~15% of Codex (3% so far) — on track to finish at 0% right as it resets Fri 09:14. Keeps ~30% for you while you work.')
  // Near the reset the reserve shrinks and says until when.
  const [late] = pools([acct('main', 'Main', 'codex')], [cap('main', [win({ used: 58, usedToday: 3, budget: 15, reset: '2026-10-02T07:14:00Z', kept: 3, keptUntil: '2026-10-02T07:14:00Z' })])])
  assert.equal(plainText(poolSentence(late, now, TZ)), 'Today: use up to ~15% of Codex (3% so far) — on track to finish at 0% right as it resets Fri 09:14. Keeps ~3% for you until Fri 09:14.')
  // Reserve off, or no reserve reported: release 11 text and gauge.
  const [off] = pools([acct('main', 'Main', 'codex')], [cap('main', [win({ used: 58, usedToday: 3, budget: 15, reset: '2026-10-02T07:14:00Z', kept: 0, level: 0 })])])
  assert.equal(gauge(off.rows[0], accountPlan(off.rows[0], now)).yours, 0)
  assert.equal(plainText(poolSentence(off, now, TZ)), 'Today: use up to ~15% of Codex (3% so far) — on track to finish at 0% right as it resets Fri 09:14.')
})

test('Keep for you: an account at your reserve leaves today\'s plan and reads kept for you', () => {
  const [codex] = pools(
    [acct('main', 'Main', 'codex'), acct('spare', 'Spare', 'codex')],
    [cap('main', [win({ used: 58, usedToday: 3, budget: 15, reset: '2026-10-02T07:14:00Z', kept: 30 })]), cap('spare', [win({ used: 91, usedToday: 2, budget: 6, reset: '2026-09-30T16:02:00Z', kept: 30, keptUntil: '2026-09-30T16:02:00Z' })])],
  )
  const spare = codex.rows[0]
  const plan = accountPlan(spare, now)!
  assert.equal(plan.atReserve, true)
  assert.equal(plan.reserve, 9, 'what is kept is never more than what is left')
  assert.equal(todayCell(spare, plan).kind, 'reserve')
  assert.deepEqual(gauge(spare, plan), { yours: 9, later: 0, today: 0, spent: 2, tick: 5, frozen: false })
  assert.equal(plainText(poolSentence(codex, now, TZ)), 'Today: ~15% of Main, keeping ~30% for you. Spare is kept for you until tomorrow 18:02.')
  // Every account at the reserve: agents wait.
  const [both] = pools([acct('spare', 'Spare', 'codex'), acct('s2', 'Spare 2', 'codex')], [cap('spare', [win({ used: 91, usedToday: 2, budget: 6, reset: '2026-09-30T16:02:00Z', kept: 30 })]), cap('s2', [win({ used: 90, usedToday: 1, budget: 5, reset: '2026-10-01T16:02:00Z', kept: 30 })])])
  const s = poolSentence(both, now, TZ)
  assert.equal(plainText(s), 'At your reserve: Spare and Spare 2 are kept for you. Agents wait.')
  assert.equal(s.ahead, true)
  // One account alone at its reserve.
  const [single] = pools([acct('spare', 'Spare', 'codex')], [cap('spare', [win({ used: 91, usedToday: 2, budget: 6, reset: '2026-09-30T16:02:00Z', kept: 30, keptUntil: '2026-09-30T16:02:00Z' })])])
  assert.equal(plainText(poolSentence(single, now, TZ)), 'At your reserve: the 9% left of Codex is kept for you until tomorrow 18:02. Agents wait.')
})

test("Keep for you: the 5-hour window's clause, Away and a dated Hold", () => {
  const five: CapacityWindow = { ...win({ kind: '5h', used: 40, budget: 60, reset: '2026-09-29T14:40:00Z', kept: 16, keptUntil: '2026-09-29T14:40:00Z' }) }
  const [claude] = pools([acct('c', 'markus', 'claude')], [cap('c', [win({ used: 63, usedToday: 4, budget: 10, reset: '2026-10-04T09:00:00Z', finish: '2026-10-02T20:00:00Z', kept: 30 }), five])])
  assert.equal(plainText(poolSentence(claude, now, TZ)), 'Today: use up to ~10% of Claude (4% so far) — on track to finish at 0% by Fri 22:00, before it resets Sun 11:00. Its 5-hour window keeps ~16% for you until 16:40.')
  const away = schedule({ override: 'away', override_until: '2026-10-05T06:00:00Z' })
  const [gone] = pools([acct('c', 'markus', 'claude')], [cap('c', [win({ used: 63, usedToday: 4, budget: 37, reset: '2026-10-04T09:00:00Z', kept: 0 })], away)])
  assert.equal(gone.override, 'away')
  assert.equal(plainText(poolSentence(gone, now, TZ)), "Away: agents may use all 37% of Claude until you're back.")
  assert.deepEqual(todayCell(gone.rows[0], accountPlan(gone.rows[0], now)), { kind: 'sprint', text: 'all 37%' })
  assert.equal(activeOverride(schedule({ override: 'away', override_until: '2026-09-29T10:00:00Z' }), now), '', 'Away ends at its date')
  const held = schedule({ override: 'hold', override_until: '2026-09-29T14:02:00Z' })
  const [h] = pools([acct('g', 'markus', 'grok')], [cap('g', [win({ used: 10, budget: 0, reset: '2026-10-06T11:10:00Z' })], held)])
  assert.equal(plainText(poolSentence(h, now, TZ)), "On hold until 16:02. Agents leave Grok alone until then; today's share moves to the coming days.")
  assert.equal(activeOverride(held, Date.parse('2026-09-29T14:02:00Z')), '', 'a dated Hold ends at its date')
})

test('Keep for you: wall times in the schedule zone, and saves confirm reserve and Away', () => {
  assert.equal(new Date(zonedInstant(2026, 9, 5, 8, TZ)).toISOString(), '2026-10-05T06:00:00.000Z')
  assert.equal(new Date(zonedInstant(2026, 9, 26, 8, TZ)).toISOString(), '2026-10-26T07:00:00.000Z', 'after the change to CET')
  assert.equal(new Date(zonedInstant(2026, 2, 29, 2.5, TZ)).toISOString(), '2026-03-29T01:30:00.000Z', 'a DST gap resolves forward')
  const s = schedule()
  assert.equal(new Date(workStart(s, now, 1)).toISOString(), '2026-09-30T06:00:00.000Z', 'tomorrow 08:00')
  assert.equal(new Date(workStart(s, now, 1, 0)).toISOString(), '2026-10-05T06:00:00.000Z', 'next Monday 08:00')
  const friday = Date.parse('2026-10-02T12:00:00Z')
  assert.equal(new Date(workStart(s, friday, 1)).toISOString(), '2026-10-05T06:00:00.000Z', "tomorrow's hours skip the weekend")
  const sent = withReserve(stripOverride(s), 'fixed', 40)
  assert.equal(confirmsSave([{ scope: 'user', schedule: sent }], sent), true)
  assert.equal(confirmsSave([{ scope: 'user', schedule: withReserve(sent, 'auto') }], sent), false, 'a different reserve is not the save')
  const away = { ...sent, override: 'away' as const, override_until: '2026-10-05T06:00:00Z' }
  assert.equal(confirmsSave([{ scope: 'user', schedule: { ...away, override_until: '2026-10-05T06:00:00.000Z' } }], away), true)
  assert.equal(confirmsSave([{ scope: 'user', schedule: sent }], away), false)
  assert.equal(sameShape(sent, s), true, 'Keep for you is not the shape')
})
