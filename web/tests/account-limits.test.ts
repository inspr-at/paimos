// SPDX-License-Identifier: AGPL-3.0-only
// AEON-384: the Advanced sentence, limits set by hand before it, and the words
// Settings / Accounts uses for readings and money.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import {
  amountText, dateRange, draftFor, draftWrite, limitBinds, limitSummary, limitUseLine, nameClashes, noWindowLine, putLimit, readingSupport,
  recentReadings, removeLimit, removeWindow, renameAccount, renameProblem, repeatWindow, sameLimit, setByYou, setByYouText, spendLine,
  unitChoices, windowLabel, type LimitUse,
} from '../src/lib/accountLimits.ts'
import type { AllowanceWindow } from '../src/lib/agents.ts'
import type { CapacityReading, CapacityWindow } from '../src/lib/capacity.ts'

const TZ = 'Europe/Vienna'
const NOW = Date.parse('2026-09-29T12:02:00Z') // Tue 14:02 in Vienna
const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

test('the sentence round-trips through its draft for every unit', () => {
  for (const rule of [
    { amount: 20, unit: 'percent', period: 'day' }, { amount: 5, unit: 'runs', period: 'week' },
    { amount: 50_000_000, unit: 'cost_micros', period: 'month' }, { amount: 12_345_670, unit: 'cost_micros', period: 'day' },
    { amount: 300, unit: 'requests', period: 'month' }, { amount: 2_000_000, unit: 'tokens', period: 'week' },
  ] as const) {
    const draft = draftFor(rule, ['percent', 'runs'])
    const back = draftWrite(draft)
    assert.equal(back.ok, true, JSON.stringify(rule))
    if (back.ok) assert.ok(sameLimit(back.body, rule), `${JSON.stringify(rule)} -> ${JSON.stringify(back.body)}`)
  }
  assert.deepEqual(draftFor(null, ['percent', 'runs']), { amount: '', unit: 'percent', period: 'day' })
  assert.deepEqual(draftWrite({ amount: '$12.5', unit: 'dollars', period: 'month' }), { ok: true, body: { amount: 12_500_000, unit: 'cost_micros', period: 'month' } })
})

test('a bad amount names the one thing to fix', () => {
  assert.deepEqual(draftWrite({ amount: '0', unit: 'percent', period: 'day' }), { ok: false, message: 'Enter a whole number from 1 to 100.' })
  assert.deepEqual(draftWrite({ amount: '101', unit: 'percent', period: 'day' }), { ok: false, message: 'Enter a whole number from 1 to 100.' })
  assert.deepEqual(draftWrite({ amount: '2.5', unit: 'runs', period: 'day' }), { ok: false, message: 'Enter a whole number above 0.' })
  assert.deepEqual(draftWrite({ amount: '12.3.4', unit: 'dollars', period: 'day' }), { ok: false, message: 'Enter an amount in dollars, like 50.' })
  assert.deepEqual(draftWrite({ amount: '99999999999999999999', unit: 'tokens', period: 'day' }), { ok: false, message: 'Enter a smaller number.' })
})

test('the unit menu offers what the account can count, likeliest first', () => {
  assert.deepEqual(unitChoices({ harness: 'codex', measured: false, money: false }), ['percent', 'runs'])
  assert.deepEqual(unitChoices({ harness: 'grok', measured: false, money: false }), ['runs'])
  assert.deepEqual(unitChoices({ harness: 'pi', measured: false, money: true }), ['dollars', 'runs'])
  assert.deepEqual(unitChoices({ harness: 'pi', measured: false, money: false }), ['runs'])
  assert.deepEqual(unitChoices({ harness: 'pi', measured: false, money: false, current: 'cost_micros' }), ['runs'])
  assert.deepEqual(unitChoices({ harness: 'cursor', measured: true, money: false, current: 'requests' }), ['percent', 'runs', 'requests'])
})

test('limits read as a person says them', () => {
  assert.equal(limitSummary({ amount: 20, unit: 'percent', period: 'day' }), '20% a day')
  assert.equal(limitSummary({ amount: 1, unit: 'runs', period: 'week' }), '1 run a week')
  assert.equal(limitSummary({ amount: 50_000_000, unit: 'cost_micros', period: 'month' }), '$50 a month')
  assert.equal(amountText(2_000_000, 'tokens'), '2,000,000 tokens')
  assert.equal(amountText(12_400_000, 'cost_micros'), '$12.40')
  const use = (fields: Partial<LimitUse>): LimitUse => ({ id: 'r', account_id: 'a', created_at: '', amount: 20, unit: 'percent', period: 'day', used: 12, period_end: '2026-09-29T22:00:00Z', ...fields })
  assert.equal(limitUseLine(use({}), NOW, TZ), '12% used today')
  assert.equal(limitUseLine(use({ used: 20 }), NOW, TZ), '20% used today · reached until tomorrow 00:00')
  assert.equal(limitUseLine(use({ unit: 'runs', amount: 5, used: 3, period: 'week' }), NOW, TZ), '3 of 5 runs this week')
  assert.equal(limitUseLine(use({ unit: 'cost_micros', amount: 50e6, used: 12.4e6, period: 'month' }), NOW, TZ), '$12.40 of $50 this month')
})

test('the sentence binds before the plan only when it leaves less for today', () => {
  const primary = { pacing: { budget_percent: 15, used_today_percent: 3, suggested_today_percent: 12 } } as unknown as CapacityWindow
  const use = { id: 'r', account_id: 'a', created_at: '', amount: 20, unit: 'percent', period: 'day', used: 12, period_end: '' } as LimitUse
  assert.equal(limitBinds(use, primary), true) // 8 left under the sentence, 12 in the plan
  assert.equal(limitBinds({ ...use, used: 2 }, primary), false)
  assert.equal(limitBinds({ ...use, unit: 'runs' }, primary), false)
  assert.equal(limitBinds(undefined, primary), false)
})

test('limits set by hand before the sentence show while they still apply', () => {
  const w = (fields: Partial<AllowanceWindow>): AllowanceWindow => ({ id: 'w', account_id: 'a', starts_at: '2026-09-28T06:00:00Z', ends_at: '2026-10-28T07:00:00Z', unit: 'requests', allowance: 300, used: 40, reserved: 0, pace_model: 'unrestricted', burst_ratio: 0, set_by_you: true, ...fields })
  const vendor = w({ id: 'v', set_by_you: undefined, unit: 'percent' })
  const ended = w({ id: 'e', ends_at: '2026-09-29T10:00:00Z' })
  assert.deepEqual(setByYou([vendor, ended, w({})], NOW).map(x => x.id), ['w'])
  assert.equal(setByYouText(w({}), TZ), '300 requests · 28 Sep – 28 Oct')
  assert.equal(dateRange('2026-10-01T00:00:00+02:00', '2026-11-01T00:00:00+01:00', TZ), '1–31 Oct')
  assert.equal(dateRange('2026-12-20T00:00:00Z', '2027-01-10T00:00:00Z', TZ), '20 Dec 2026 – 10 Jan 2027')
  assert.equal(spendLine('12.40', undefined, NOW, TZ), '$12.40 this month · no limit')
})

test('how an account is read, and its windows and last readings', () => {
  assert.equal(readingSupport('codex'), 'Reads every 5 min')
  assert.equal(readingSupport('claude'), 'Reads during runs')
  assert.equal(readingSupport('cursor'), "Doesn't show its limit")
  assert.equal(noWindowLine('claude', 'mbp2607'), 'Reads with its first run. Agents can start now.')
  assert.equal(noWindowLine('codex', 'mbp2607'), 'Reading your limits on mbp2607…')
  const win = (kind: CapacityReading['window_kind'], bucket = '') => ({ reading: { window_kind: kind, bucket } } as CapacityWindow)
  assert.equal(windowLabel(win('weekly', 'seven_day_opus'), 'claude'), 'Weekly · Opus')
  assert.equal(windowLabel(win('5h', 'codex'), 'codex'), '5-hour')
  const r = (at: string, used: number, source: CapacityReading['source'] = 'agentd', kind: CapacityReading['window_kind'] = 'weekly') => ({ window_kind: kind, window_minutes: 10080, used_percent: used, resets_at: '2026-10-02T07:14:00Z', source, read_at: at }) as CapacityReading
  const readings = [r('2026-09-29T11:00:00Z', 57), r('2026-09-29T12:00:00Z', 58, 'harness'), r('2026-09-29T12:00:00Z', 58), r('2026-09-29T07:00:00Z', 56), r('2026-09-29T06:00:00Z', 55), r('2026-09-29T11:30:00Z', 40, 'harness', '5h'), r('2026-09-29T11:59:00Z', 60, 'estimate')]
  assert.deepEqual(recentReadings(readings, { reading: { window_kind: 'weekly', bucket: '' } } as CapacityWindow).map(x => x.used_percent), [58, 57, 56])
})

test('names that repeat within one vendor ask to be named', () => {
  const clashes = nameClashes([{ id: '1', harness: 'codex', label: 'Main' }, { id: '2', harness: 'codex', label: 'main ' }, { id: '3', harness: 'claude', label: 'Main' }])
  assert.deepEqual([...clashes].sort(), ['1', '2'])
  assert.equal(renameProblem('  '), 'Give the account a name.')
  assert.equal(renameProblem('x'.repeat(129)), 'Keep the name to 128 characters.')
  assert.equal(renameProblem('Spare'), null)
})

test('the sentence, Remove, Make this repeat and rename use the account routes', async () => {
  const calls: { url: string; method: string; body: unknown }[] = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined })
    return init?.method === 'DELETE' ? new Response(null, { status: 204 }) : Response.json({})
  }
  await putLimit('a/1', { amount: 20, unit: 'percent', period: 'day' })
  await removeLimit('a/1')
  await removeWindow('a/1', 'w/1')
  await repeatWindow('a/1', 'w/1')
  await renameAccount('a/1', 'Spare')
  assert.deepEqual(calls, [
    { url: '/api/agent-accounts/a%2F1/limit', method: 'PUT', body: { amount: 20, unit: 'percent', period: 'day' } },
    { url: '/api/agent-accounts/a%2F1/limit', method: 'DELETE', body: undefined },
    { url: '/api/agent-accounts/a%2F1/windows/w%2F1', method: 'DELETE', body: undefined },
    { url: '/api/agent-accounts/a%2F1/windows/w%2F1/repeat', method: 'POST', body: undefined },
    { url: '/api/agent-accounts/a%2F1/label', method: 'PUT', body: { label: 'Spare' } },
  ])
})
