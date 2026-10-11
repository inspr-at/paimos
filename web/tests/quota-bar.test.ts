// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1172 (design AEON-1149): the quota bar's one pure function. Risk: the bar or
// its words claim room agents do not have, or blame the wrong stop. Every case names
// the stop that binds and the room before it.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { capacityCaption, DAILY_FRESH_MS, nextStale, quotaBar, quotaCaption, quotaInput, quotaLabel, quotaSegments, quotaSentence, ruleLeft, widestSentences, type Said } from '../src/lib/quotaBar.ts'
import type { DailyAccount, DailyPlanFields } from '../src/lib/dailyLimits.ts'
import type { CapacityWindow } from '../src/lib/capacity.ts'
import type { LimitUse } from '../src/lib/accountLimits.ts'

const NOW = Date.parse('2026-10-11T13:10:00Z')
const iso = (t: number) => new Date(t).toISOString()
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

// AEON-1172 fix round 2: admission refuses once an older rule has counted all it
// allows, whatever it counts. The bar must not promise room in percentage points then.
test('a used-up older limit stops agents in any unit, runs, tokens or money as much as percent', () => {
  const base: LimitUse = { id: 'r1', account_id: 'a1', amount: 5, unit: 'runs', period: 'week', created_at: '2026-09-01T00:00:00Z', used: 5, period_end: '2026-10-12T22:00:00Z' }
  for (const rule of [base, { ...base, unit: 'tokens' as const, amount: 2_000_000, used: 2_000_400 }, { ...base, unit: 'cost_micros' as const, amount: 50e6, used: 50e6 }, { ...base, unit: 'requests' as const, amount: 300, used: 301 }]) {
    const bar = quotaBar(quotaInput({ daily: door(), window: window(53), rule }))
    assert.deepEqual([bar.room, bar.by, bar.stopAt, bar.ruleAt], [0, 'rule', 47, null], rule.unit)
    assert.equal(quotaSegments(bar).room, null, rule.unit)
    assert.equal(said(quotaSentence(bar, { vendor: 'Codex' })), '**Agents have stopped using it.** The limit set by you is used up for now.', rule.unit)
    assert.deepEqual(quotaCaption(bar, { now: NOW }), { text: 'Agents stopped at the limit set by you', tone: 'warn', icon: 'pause' }, rule.unit)
    assert.equal(quotaLabel(bar, 'Codex · markus'), 'Codex · markus: 47 % used; today’s limit 50 %; the limit set by you is used up; floor 10 %.', rule.unit)
  }
  // One run left still leaves today's room; the rule's own unit does not map onto the bar.
  assert.deepEqual(((b) => [b.room, b.by])(quotaBar(quotaInput({ daily: door(), window: window(53), rule: { ...base, used: 4 } }))), [3, 'today'])
  // A used-up rule stops agents even while the reading is old or missing: never "agents can't pace it".
  const stale = quotaBar(quotaInput({ daily: door({ freshness: 'stale' }), window: window(53), rule: base }))
  assert.deepEqual([stale.room, stale.by], [0, 'rule'])
  assert.equal(quotaCaption(stale, { now: NOW }).text, 'Agents stopped at the limit set by you')
  // A percent rule that still leaves room binds first, yet an old reading claims none and says so.
  const percent: LimitUse = { ...base, unit: 'percent', amount: 20, used: 18 }
  const old = quotaBar(quotaInput({ daily: door({ freshness: 'stale' }), window: window(53), rule: percent }))
  assert.deepEqual([old.room, old.by], [null, 'rule'])
  assert.equal(quotaCaption(old, { now: NOW }).text, '53 % left · reading out of date')
  const blind = quotaBar(quotaInput({ rule: base }))
  assert.deepEqual([blind.known, blind.room, blind.by], [false, 0, 'rule'])
  assert.deepEqual(quotaCaption(blind, { now: NOW }), { text: 'Agents stopped at the limit set by you', tone: 'warn', icon: 'pause' })
  assert.equal(said(quotaSentence(blind, { vendor: 'Grok' })), '**Agents have stopped using it.** The limit set by you is used up for now.')
  // The panel reserves both sentences of an account without a reading, so Remove moves nothing below.
  assert.deepEqual(widestSentences('Grok', false).map(said), [said(quotaSentence(quotaBar({ used: null }), { vendor: 'Grok' })), said(quotaSentence(blind, { vendor: 'Grok' }))])
  // A Hold or the switch still says why first.
  assert.equal(quotaBar(quotaInput({ daily: door(), window: window(53), rule: base, override: 'hold' })).by, 'held')
})

// AEON-1172 fix round 2: a plan read that fails keeps the last plan. Its doors were
// fresh when it arrived; the server trusts a reading for two minutes, so the bar
// stops promising room then, at the door's reset, or when the plan's day ends.
// Here the reading was taken as the plan was asked for (fix round 3 measures from it).
test('a plan that could not refresh goes stale on time and claims no room', () => {
  const arrived = NOW, source = { daily: door({ read_at: iso(arrived) }), window: window(53), clock: { asked: arrived }, dayEnds: '2026-10-11T22:00:00Z' }
  const at = (now: number, over: Partial<typeof source> = {}) => quotaBar(quotaInput({ ...source, ...over, now }))
  assert.deepEqual(((b) => [b.stale, b.room])(at(arrived + DAILY_FRESH_MS - 1)), [false, 3])
  const old = at(arrived + DAILY_FRESH_MS)
  assert.deepEqual([old.stale, old.room, old.stopAt], [true, null, 50])
  assert.equal(said(quotaSentence(old, { vendor: 'Codex' })), '**The last reading is out of date.** Agents wait for a fresh one before they use more.')
  assert.equal(quotaCaption(old, { now: arrived + DAILY_FRESH_MS }).text, '53 % left · reading out of date')
  // The door's reset or the end of the plan's day comes first.
  assert.equal(at(Date.parse('2026-10-11T13:11:00Z'), { daily: door({ resets_at: '2026-10-11T13:11:00Z' }) }).stale, true)
  assert.equal(at(Date.parse('2026-10-11T13:11:00Z'), { dayEnds: '2026-10-11T13:11:00Z' }).stale, true)
  // A new plan with a new reading is fresh again; without a clock the server's word stands.
  const later = arrived + 2 * DAILY_FRESH_MS + 1
  assert.equal(at(arrived + 3 * DAILY_FRESH_MS, { clock: { asked: later }, daily: door({ read_at: iso(later) }) }).stale, false)
  assert.equal(quotaBar(quotaInput({ daily: door(), window: window(53) })).stale, false)
})

// AEON-1172 fix round 3: the server trusts a reading for two minutes from when it was
// taken (read_at), not from when the plan reached this browser. A reading nearly two
// minutes old on arrival must stop promising room when admission stops trusting it,
// even while every refresh fails. The clock is injected; nothing waits on real time.
test('a reading nearly two minutes old goes stale when the server stops trusting it, not two minutes after the plan arrived', () => {
  const read = NOW - 119_000, source = { daily: door({ read_at: iso(read) }), window: window(53), clock: { asked: NOW }, dayEnds: '2026-10-11T22:00:00Z' }
  const at = (now: number, over: Partial<typeof source> = {}) => quotaBar(quotaInput({ ...source, ...over, now }))
  assert.deepEqual(((b) => [b.stale, b.room])(at(read + DAILY_FRESH_MS - 1)), [false, 3])
  // The refresh after it fails: no new plan, so the same clock; the reading's own age decides.
  for (const now of [read + DAILY_FRESH_MS, NOW + 2_000, NOW + DAILY_FRESH_MS - 1]) {
    const old = at(now)
    assert.deepEqual([old.stale, old.room], [true, null], `at ${now - NOW} ms`)
    assert.equal(said(quotaSentence(old, { vendor: 'Codex' })), '**The last reading is out of date.** Agents wait for a fresh one before they use more.')
  }
  // The server's clock in its answer places the reading, so this browser's own clock running
  // a minute behind or ahead stretches nothing: read 100 s before the server answered, 19 s
  // remain (one second kept back because the Date header counts whole seconds).
  const server = Date.parse('2026-10-11T13:10:00Z')
  for (const skew of [-60_000, 0, 60_000]) {
    const asked = server + skew - 200, clock = { asked, server }, daily = door({ read_at: iso(server - 100_000) })
    assert.deepEqual(((b) => [b.stale, b.room])(at(asked + 19_000 - 1, { clock, daily })), [false, 3], `skew ${skew}`)
    assert.equal(at(asked + 19_000, { clock, daily }).stale, true, `skew ${skew}`)
  }
  // A reading time ahead of the server's clock, or none at all, is no fresh reading.
  assert.equal(at(NOW, { clock: { asked: NOW, server }, daily: door({ read_at: iso(server + 1_500) }) }).stale, true)
  assert.equal(at(NOW, { daily: door({ read_at: null }) }).stale, true)
  // The page moves its clock at the next door to go stale, not only at the next read.
  const plan = (accounts: DailyAccount[]): DailyPlanFields => ({ daily_until: '2026-10-11T22:00:00Z', daily_state: { codex: { state: 'on_pace', limit_used_pct: 50, today_points_used: 0, today_points_allowed: 10, over_pace_points: 0, active_account_id: 'a1', next_on_ladder: null, accounts } } })
  const doors = [door({ read_at: iso(NOW - 30_000) }), door({ account_id: 'a2', read_at: iso(read) }), door({ account_id: 'a3', freshness: 'stale', read_at: iso(NOW - 200_000) }), door({ account_id: 'a4', details_redacted: true })]
  assert.equal(nextStale(plan(doors), { asked: NOW }, NOW), read + DAILY_FRESH_MS)
  assert.equal(nextStale(plan(doors), { asked: NOW }, read + DAILY_FRESH_MS), NOW + 90_000)
  assert.equal(nextStale(plan(doors), { asked: NOW }, NOW + 90_000), null)
  assert.equal(nextStale(plan(doors), null, NOW), null)
})

// AEON-1175: Capacity's caption under each account's bar. Risk: it claims room the plan does not give.
test('Capacity’s caption says the room left today, or what stopped agents and what new work does at today’s limit', () => {
  const o = { period: 'this week', resetsAt: '2026-10-14T17:00:00Z', now: NOW, timezone: 'Europe/Vienna', vendor: 'Claude' }
  assert.deepEqual(capacityCaption(quotaBar({ used: 47, limit: 57, floor: 10 }), o), { text: '47 % used · 10 more percentage points today · resets Wed 19:00', tone: '', icon: '' })
  assert.equal(capacityCaption(quotaBar({ used: 56, limit: 57 }), o).text, '56 % used · 1 more percentage point today · resets Wed 19:00')
  assert.deepEqual(capacityCaption(quotaBar({ used: 50, limit: 50 }), { ...o, atLimit: 'ladder' }), { text: 'At today’s limit until midnight · new Claude work follows Models', tone: 'warn', icon: 'pause' })
  assert.equal(capacityCaption(quotaBar({ used: 50, limit: 50 }), { ...o, atLimit: 'wait' }).text, 'At today’s limit until midnight · new Claude work waits')
  // Kept for you, the floor, a low warning, an old reading and no reading say what the list says.
  assert.equal(capacityCaption(quotaBar({ used: 71, limit: 80, keep: 30 }), o).text, 'Agents stopped · the rest is kept for you')
  assert.equal(capacityCaption(quotaBar({ used: 91, limit: 90, floor: 10 }), { ...o, low: true }).text, 'Low · 9 % left · agents stopped at the floor')
  assert.equal(capacityCaption(quotaBar({ used: 22, limit: 32, stale: true }), o).text, '78 % left · reading out of date')
  assert.equal(capacityCaption(quotaBar({ used: null }), o).text, 'Quota not reported')
  // Outside the person's plan there is no today's limit: the reading only, never a guessed room.
  assert.equal(capacityCaption(quotaBar({ used: 30 }), o).text, '70 % left this week · resets Wed 19:00')
})
