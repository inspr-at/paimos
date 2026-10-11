// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1172 (design AEON-1149): the quota bar's one pure function. Risk: the bar or
// its words claim room agents do not have, or blame the wrong stop. Every case names
// the stop that binds and the room before it.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { quotaBar, quotaCaption, quotaInput, quotaLabel, quotaSegments, quotaSentence, ruleLeft, type Said } from '../src/lib/quotaBar.ts'
import type { DailyAccount } from '../src/lib/dailyLimits.ts'
import type { CapacityWindow } from '../src/lib/capacity.ts'
import type { LimitUse } from '../src/lib/accountLimits.ts'

const NOW = Date.parse('2026-10-11T13:10:00Z')
const said = (parts: Said[]) => parts.map(p => (p.strong ? `**${p.text}**` : p.text)).join('')
const door = (over: Partial<DailyAccount> = {}): DailyAccount => ({
  account_id: 'a1', label: 'markus', order: 1, used_pct: 47, left_pct: 53, start_of_day_used_pct: 40, limit_used_pct: 50, floor_pct: 10,
  resets_at: '2026-10-14T17:00:00Z', read_at: '2026-10-11T13:08:00Z', freshness: 'fresh', resets: null, can_edit: true, ...over,
})
const window = (remaining: number, keep = 0, freshness: CapacityWindow['freshness'] = 'fresh'): CapacityWindow => ({
  reading: { window_kind: 'weekly', window_minutes: 10080, used_percent: 100 - remaining, resets_at: '2026-10-14T17:00:00Z', source: 'harness', read_at: '2026-10-11T13:08:00Z' },
  starts_at: '2026-10-07T17:00:00Z', allowance: 100, remaining_percent: remaining, freshness,
  pacing: { usable_hours: 10, percent_per_hour: 1, suggested_today_percent: 10, reserve_effective_percent: keep },
})

test('room: agents may use the points up to today’s limit, and the tick sits there', () => {
  const bar = quotaBar({ used: 47, limit: 57, floor: 10 })
  assert.deepEqual([bar.room, bar.by, bar.stopAt, bar.floorAt], [10, 'today', 57, 90])
  assert.deepEqual(quotaSegments(bar), { used: 47, room: { at: 47, width: 10 }, keep: 0, floor: 10, tick: { at: 57, label: 'today 57 %', edge: '' } })
  assert.equal(said(quotaSentence(bar, { vendor: 'Claude' })), 'Agents may use **10 more percentage points today**. They stop at today’s limit (57 %) first.')
  assert.deepEqual(quotaCaption(bar, { period: 'this week', resetsAt: '2026-10-14T17:00:00Z', now: NOW, timezone: 'Europe/Vienna' }), { text: '53 % left this week · resets Wed 19:00', tone: '', icon: '' })
  assert.equal(quotaLabel(bar, 'Claude · markus', 'this week'), 'Claude · markus: 47 % used this week; today’s limit 57 %; floor 10 %.')
})

test('at the limit: no room, and new work follows the harness’s own choice', () => {
  const bar = quotaBar({ used: 50, limit: 50, floor: 10 })
  assert.deepEqual([bar.room, bar.by], [0, 'today'])
  assert.equal(quotaSegments(bar).room, null)
  assert.deepEqual(quotaCaption(bar, { now: NOW }), { text: 'At today’s limit until midnight', tone: 'warn', icon: 'pause' })
  assert.equal(said(quotaSentence(bar, { vendor: 'Claude', atLimit: 'ladder' })), '**At today’s limit until midnight.** New Claude work follows the model ladder meanwhile.')
  assert.equal(said(quotaSentence(bar, { vendor: 'Claude', atLimit: 'wait' })), '**At today’s limit until midnight.** New Claude work waits until then.')
  // Over pace: used past the limit is still no room, never negative.
  assert.equal(quotaBar({ used: 53, limit: 50 }).room, 0)
})

test('Keep for you binds before today’s limit while you work', () => {
  const bar = quotaBar({ used: 40, limit: 80, floor: 10, keep: 30 })
  assert.deepEqual([bar.keepAt, bar.stopAt, bar.room, bar.by], [70, 70, 30, 'keep'])
  assert.equal(quotaSegments(bar).keep, 30)
  assert.equal(said(quotaSentence(bar, { vendor: 'Claude' })), 'Agents may use **30 more percentage points today**. They stop at what’s kept for you (from 70 %) first.')
  const stopped = quotaBar({ used: 71, limit: 80, keep: 30 })
  assert.deepEqual(quotaCaption(stopped, { now: NOW }), { text: 'Agents stopped · the rest is kept for you', tone: 'warn', icon: 'pause' })
  // Outside your hours R_eff is 0: nothing is kept and no zone is drawn.
  assert.equal(quotaBar({ used: 40, limit: 80, keep: 0 }).keepAt, null)
})

test('the floor binds when today’s limit is capped at it, and a low warning says so first', () => {
  // Use everything: the plan caps today's limit at 100 − floor; level with the floor means the floor binds.
  const bar = quotaBar({ used: 40, limit: 90, floor: 10 })
  assert.deepEqual([bar.stopAt, bar.room, bar.by], [90, 50, 'floor'])
  assert.equal(said(quotaSentence(bar, { vendor: 'Codex' })), 'Agents may use **50 more percentage points today**. They stop at the floor (90 %) first.')
  const low = quotaBar({ used: 92, limit: 90, floor: 10 })
  assert.deepEqual([low.room, low.by], [0, 'floor'])
  assert.deepEqual(quotaCaption(low, { now: NOW, low: true }), { text: 'Low · 8 % left · agents stopped at the floor', tone: 'warn', icon: 'gauge' })
  assert.equal(said(quotaSentence(low, { vendor: 'Codex' })), '**Agents have stopped using it.** Only the floor is left.')
  // A limit above 100 − floor never reaches past the floor.
  assert.equal(quotaBar({ used: 40, limit: 95, floor: 10 }).limit, 90)
})

test('Boost today sets today’s limit and names the tick', () => {
  const input = quotaInput({ daily: door({ used_pct: 40, limit_used_pct: 60, floor_pct: 10 }), window: window(60, 30), boost: true })
  const bar = quotaBar(input)
  assert.deepEqual([bar.boost, bar.limit, bar.keepAt, bar.by, bar.room], [true, 60, 70, 'today', 20])
  assert.equal(quotaSegments(bar).tick?.label, 'boost 60 %')
  assert.equal(said(quotaSentence(bar, { vendor: 'Claude' })), 'Agents may use **20 more percentage points today**. They stop at today’s Boost (60 %) first.')
  // Sprint and Away keep nothing for you.
  assert.equal(quotaBar(quotaInput({ daily: door(), window: window(53, 30), override: 'sprint' })).keepAt, null)
})

test('no reading: a dashed track and “Quota not reported”, never a guess', () => {
  const bar = quotaBar(quotaInput({}))
  assert.deepEqual([bar.known, bar.room, bar.limit], [false, null, null])
  assert.deepEqual(quotaCaption(bar, { now: NOW }), { text: 'Quota not reported', tone: '', icon: '' })
  assert.equal(quotaLabel(bar, 'Cursor · markus'), 'Cursor · markus: quota not reported')
  assert.match(said(quotaSentence(bar, { vendor: 'Cursor' })), /doesn’t report its quota/)
  // A redacted door is no reading of its own; the capacity window still is one.
  const redacted = quotaBar(quotaInput({ daily: door({ details_redacted: true, used_pct: null, limit_used_pct: null }) }))
  assert.equal(redacted.known, false)
  // An old reading claims no room: the server waits for a fresh one.
  const stale = quotaBar(quotaInput({ daily: door({ freshness: 'stale' }), window: window(53) }))
  assert.deepEqual([stale.known, stale.stale, stale.room], [true, true, null])
  assert.equal(said(quotaSentence(stale, { vendor: 'Claude' })), '**The last reading is out of date.** Agents wait for a fresh one before they use more.')
  // Outside the viewer's plan only the reading is known: no limit, no room.
  const other = quotaBar(quotaInput({ window: window(53) }))
  assert.deepEqual([other.used, other.limit, other.room], [47, null, null])
  assert.equal(said(quotaSentence(other, { vendor: 'Claude' })), '**53 % left.** Today’s limit comes from its owner’s plan.')
})

test('an older limit set by hand binds when it leaves less than today’s limit', () => {
  const rule: LimitUse = { id: 'r1', account_id: 'a1', amount: 20, unit: 'percent', period: 'day', created_at: '2026-09-01T00:00:00Z', used: 15, period_end: '2026-10-11T22:00:00Z' }
  assert.equal(ruleLeft(rule), 5)
  assert.equal(ruleLeft({ ...rule, unit: 'runs' }), null)
  const bar = quotaBar(quotaInput({ daily: door({ used_pct: 47, limit_used_pct: 57 }), window: window(53), rule }))
  assert.deepEqual([bar.ruleAt, bar.stopAt, bar.room, bar.by], [52, 52, 5, 'rule'])
  assert.equal(said(quotaSentence(bar, { vendor: 'Codex' })), 'Agents may use **5 more percentage points today**. They stop at the limit set by you (52 %) first.')
  const spent = quotaBar(quotaInput({ daily: door(), window: window(53), rule: { ...rule, used: 20 } }))
  assert.deepEqual([spent.room, spent.by], [0, 'rule'])
  assert.equal(said(quotaSentence(spent, { vendor: 'Codex' })), '**Agents have stopped using it.** The limit set by you is used up for now.')
  // A rule that leaves more than today's limit does not bind.
  assert.equal(quotaBar(quotaInput({ daily: door(), window: window(53), rule: { ...rule, used: 0 } })).by, 'today')
})

test('a Hold from before or the switch off leaves the account alone', () => {
  const held = quotaBar(quotaInput({ daily: door(), window: window(53), override: 'hold' }))
  assert.deepEqual([held.room, held.by], [0, 'held'])
  assert.equal(quotaCaption(held, { now: NOW }).text, 'Held · agents leave it alone')
  const off = quotaBar(quotaInput({ daily: door(), window: window(53), off: true }))
  assert.equal(said(quotaSentence(off, { vendor: 'Claude' })), '**Agents leave it alone** while “Agents may use it” is off. Running work finishes.')
})
