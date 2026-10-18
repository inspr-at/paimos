// SPDX-License-Identifier: AGPL-3.0-only
// AEON-384: limits set by hand before, and the words Settings → Accounts uses for
// readings and money. AEON-1172 (design AEON-1149) retires the Advanced sentence and
// Make this repeat: nothing in the web creates or repeats a limit any more.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import * as limits from '../src/lib/accountLimits.ts'
import {
  amountText, dateRange, nameClashes, noWindowLine, readingSupport, recentReadings, removeLimit, removeWindow, renameAccount, renameProblem,
  ruleWords, setByYou, setByYouText, spendLine, windowLabel,
} from '../src/lib/accountLimits.ts'
import type { AllowanceWindow } from '../src/lib/agents.ts'
import type { CapacityReading, CapacityWindow } from '../src/lib/capacity.ts'

const TZ = 'Europe/Vienna'
const NOW = Date.parse('2026-09-29T12:02:00Z') // Tue 14:02 in Vienna
const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })

test('older limits read as a person says them', () => {
  assert.equal(ruleWords({ amount: 20, unit: 'percent', period: 'day' }), 'at most 20 % a day')
  assert.equal(ruleWords({ amount: 1, unit: 'runs', period: 'week' }), 'at most 1 run a week')
  assert.equal(ruleWords({ amount: 50_000_000, unit: 'cost_micros', period: 'month' }), 'at most $50 a month')
  assert.equal(amountText(2_000_000, 'tokens'), '2,000,000 tokens')
  assert.equal(amountText(12_400_000, 'cost_micros'), '$12.40')
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
  assert.equal(noWindowLine('claude', 'build-7'), 'Reads with its first run. Agents can start now.')
  assert.equal(noWindowLine('codex', 'build-7'), 'Reading your limits on build-7…')
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

test('Remove and rename use the account routes; nothing creates or repeats a limit', async () => {
  assert.equal('putLimit' in limits, false)
  assert.equal('repeatWindow' in limits, false)
  const calls: { url: string; method: string; body: unknown }[] = []
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined })
    return init?.method === 'DELETE' ? new Response(null, { status: 204 }) : Response.json({})
  }
  await removeLimit('a/1')
  await removeWindow('a/1', 'w/1')
  await renameAccount('a/1', 'Spare')
  assert.deepEqual(calls, [
    { url: '/api/agent-accounts/a%2F1/limit', method: 'DELETE', body: undefined },
    { url: '/api/agent-accounts/a%2F1/windows/w%2F1', method: 'DELETE', body: undefined },
    { url: '/api/agent-accounts/a%2F1/label', method: 'PUT', body: { label: 'Spare' } },
  ])
})
