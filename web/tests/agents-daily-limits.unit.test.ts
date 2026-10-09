// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1036: the daily limit's words, bar and writes. The risk these guard: the dial saying "on pace"
// or "at the limit" for a reading it does not have, and a daily edit that rewrites the total or loses
// another harness's settings.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope } from 'vue'
import type { PlanSnapshot } from '../src/lib/agentsWorking'
import {
  atLimitWords, boostNumber, chipTip, dailyBar, dailyOf, dailyView, makeBoost, needsAttention, paceSummary, resetsLine, sameDaily, stateWords, todayLine, typedPercent, typedPoints, usageLine, weekParts,
  type DailyAccount, type DailySettings, type DailyState,
} from '../src/lib/dailyLimits'
import { DEFAULT_DIAL, readDialPrefs, selectedHarness, withDialDefaults } from '../src/lib/dialPrefs'
const mocks = vi.hoisted(() => ({ api: vi.fn(), queue: vi.fn() }))
vi.mock('../src/lib/api.ts', () => ({ api: mocks.api }))
vi.mock('../src/lib/workQueue.ts', () => ({ queueRequest: mocks.queue }))
vi.mock('../src/lib/usePolledData.ts', () => ({ usePoller: () => ({ start() {}, stop() {} }) }))
import { useAgentPlan } from '../src/lib/useAgentPlan'

const account = (fields: Partial<DailyAccount> = {}): DailyAccount => ({ account_id: 'a1', label: 'markus.barta', order: 1, used_pct: 47, left_pct: 53, start_of_day_used_pct: 40, limit_used_pct: 50, floor_pct: 0, resets_at: '2026-10-14T17:00:00Z', freshness: 'fresh', resets: null, can_edit: true, details_redacted: false, ...fields })
const state = (fields: Partial<DailyState> = {}, accounts: DailyAccount[] = [account()]): DailyState => ({ state: 'on_pace', limit_used_pct: 50, today_points_used: 7, today_points_allowed: 10, over_pace_points: 0, active_account_id: 'a1', next_on_ladder: null, accounts, ...fields })
const settings = (fields: Partial<DailySettings> = {}): DailySettings => ({ pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder', ...fields })
const view = (s: DailyState | undefined, d = settings()) => dailyView('claude', d, s)

describe('what the dial may say about a daily reading', () => {
  it('is on pace only with a fresh reading under today’s limit, and says nothing of a harness with no account', () => {
    expect(view(state()).kind).toBe('on_pace')
    expect(stateWords(view(state()))).toBe('On pace')
    expect(view(undefined).kind).toBe('none')
    expect(view(state({}, [])).kind).toBe('none')
    expect(needsAttention(view(state()))).toBe(false)
  })
  it('is over pace in gold with the points above today’s share, and at the limit in red', () => {
    const over = view(state({ state: 'over_pace', over_pace_points: 4, limit_used_pct: 60, today_points_used: 14, today_points_allowed: 20 }, [account({ used_pct: 54, limit_used_pct: 60 })]))
    expect(over.kind).toBe('over_pace'); expect(stateWords(over)).toBe('4 percentage points over pace'); expect(needsAttention(over)).toBe(true)
    expect(chipTip(over, 'at most 2')).toBe('Claude · at most 2 · ≤ 60 % used today · 4 percentage points over pace')
    const limit = view(state({ state: 'at_limit', next_on_ladder: 'codex', today_points_used: 11 }, [account({ used_pct: 51 })]))
    expect(limit.kind).toBe('at_limit'); expect(needsAttention(limit)).toBe(true)
    expect(stateWords(limit, true)).toBe('At today’s limit · Codex next')
    expect(stateWords(limit)).toBe('At today’s limit · Codex next on the model ladder')
    expect(chipTip(limit, 'at most 2')).toBe('Claude · at most 2 · at today’s limit · Codex next')
  })
  it('never reports the limit for a stale, missing or private reading, although the server also refuses starts then', () => {
    const stale = view(state({ state: 'at_limit' }, [account({ freshness: 'stale' })]))
    expect(stale.kind).toBe('unknown'); expect(needsAttention(stale)).toBe(false); expect(stateWords(stale)).toBe('Usage not measured right now')
    expect(view(state({ state: 'at_limit' }, [account({ used_pct: null, left_pct: null, limit_used_pct: null, freshness: 'unknown' })])).kind).toBe('unknown')
    expect(view(state({ state: 'at_limit' }, [account({ details_redacted: true, used_pct: null, limit_used_pct: null })])).kind).toBe('unknown')
    // One account at its limit and another unmeasured: the harness is not known to be at its limit.
    expect(view(state({ state: 'at_limit' }, [account({ used_pct: 51 }), account({ account_id: 'a2', used_pct: null, limit_used_pct: null })])).kind).toBe('unknown')
    expect(view(state({ state: 'at_limit' }, [account({ used_pct: 51 }), account({ account_id: 'a2', used_pct: 52 })])).kind).toBe('at_limit')
  })
  it('keeps the usage line to the limit, and says no daily limit for a billed-by-use harness and for everything', () => {
    expect(usageLine(view(state()))).toBe('≤ 50 % used today')
    expect(usageLine(view(state({ state: 'no_limit' }, [account({ no_daily_limit: true, used_pct: null })])))).toBe('no daily limit')
    expect(stateWords(view(state({ state: 'no_limit' }, [account({ no_daily_limit: true, used_pct: null })])))).toBe('Nothing to pace')
    const all = view(state({ limit_used_pct: 100 }), settings({ pace: { mode: 'everything', points_per_day: null } }))
    expect(all.kind).toBe('everything'); expect(usageLine(all)).toBe('no daily limit'); expect(stateWords(all)).toBe('Uses everything before the reset')
    expect(all.share).toBeNull()
  })
  it('words what happens at the limit without inventing a ladder', () => {
    const base = state({ state: 'at_limit' }, [account({ used_pct: 51 })])
    expect(atLimitWords(view(base))).toBe('new work follows the model ladder')
    expect(atLimitWords(view({ ...base, next_on_ladder: 'cursor' }))).toBe('Cursor next on the model ladder')
    expect(atLimitWords(view(base, settings({ at_limit: 'wait' })))).toBe('new work waits until midnight')
    expect(stateWords(view(base, settings({ at_limit: 'wait' })), true)).toBe('At today’s limit · new work waits')
  })
  it('lets only an owner who may manage every account change today’s limit', () => {
    expect(view(state()).editable).toBe(true)
    expect(view(state({}, [account(), account({ account_id: 'a2', can_edit: false })])).editable).toBe(false)
    expect(view(state({}, [account({ details_redacted: true })])).editable).toBe(false)
  })
})

describe('the numbers on the right', () => {
  it('reads week used and left, then today’s points with the share, in the person’s words', () => {
    const v = view(state({ today_points_used: 7, today_points_allowed: 10 }))
    expect(weekParts(v, 'UTC')).toEqual({ strong: '47 % used · 53 % left', rest: ' this week · resets Wed 14 Oct 17:00' })
    expect(todayLine(v)).toBe('Today: 7 of 10 percentage points used · 3 left today')
    expect(paceSummary(v)).toBe('Stay on pace · 10 percentage points a day')
    expect(weekParts(view(state({}, [account({ used_pct: null })])), 'UTC').rest).toBe('This week: not measured right now')
    expect(todayLine(view(state({ today_points_used: null })))).toBe('Today: not measured yet')
    const several = view(state({}, [account(), account({ account_id: 'a2', label: 'other' })]))
    expect(weekParts(several, 'UTC').rest.startsWith(' on markus.barta this week')).toBe(true)
  })
  it('draws today’s bar teal to the share, gold over it and red past the limit, and the week when today is unknown', () => {
    const on = dailyBar(view(state({ today_points_used: 7, today_points_allowed: 10 })))!
    expect(on.mode).toBe('today'); expect(on.teal).toBeCloseTo(70); expect(on.gold).toBe(0); expect(on.red).toBe(0); expect(on.free).toBeCloseTo(30); expect(on.tick).toBeNull()
    const over = dailyBar(view(state({ today_points_used: 14, today_points_allowed: 20, over_pace_points: 4 })))!
    expect(over.teal).toBeCloseTo(50); expect(over.gold).toBeCloseTo(20); expect(over.free).toBeCloseTo(30); expect(over.tick).toEqual({ at: 50, label: 'pace 10' })
    const past = dailyBar(view(state({ state: 'at_limit', today_points_used: 11, today_points_allowed: 10 }, [account({ used_pct: 51 })])))!
    expect(past.teal + past.gold + past.red + past.free).toBeCloseTo(100); expect(past.red).toBeCloseTo(100 / 11); expect(past.free).toBe(0)
    const week = dailyBar(view(state({ today_points_used: null })))!
    expect(week).toMatchObject({ mode: 'week', teal: 47, free: 53 })
    expect(dailyBar(view(state({}, [account({ used_pct: null })])))).toBeNull()
  })
  it('shows vendor-reported resets only', () => {
    expect(resetsLine(account({ resets: { count: 2, expires_at: ['2026-10-18T16:00:00Z', '2026-11-06T16:00:00Z'] } }), 'UTC')).toBe('2 resets · first expires Sun 18 Oct 16:00')
    expect(resetsLine(account({ resets: { count: 1, expires_at: ['2026-10-18T16:00:00Z'] } }), 'UTC')).toBe('1 reset · 1 expires Sun 18 Oct 16:00')
    expect(resetsLine(account({ resets: { count: 0, expires_at: [] } }))).toBe('No resets left')
    expect(resetsLine(account({ resets: null }))).toBe(''); expect(resetsLine(null)).toBe('')
    expect(resetsLine(account({ resets: { count: -1 } as never }))).toBe('')
  })
})

describe('Boost today and pace input', () => {
  it('stores the used percentage whichever way it was typed, and restates it both ways', () => {
    const used = makeBoost(60, 'used', '2026-10-09T22:00:00Z'), left = makeBoost(40, 'left', '2026-10-09T22:00:00Z')
    expect(used).toEqual({ limit_used_pct: 60, entered_as: 'used', until: '2026-10-09T22:00:00Z' })
    expect(left).toEqual({ limit_used_pct: 60, entered_as: 'left', until: '2026-10-09T22:00:00Z' })
    expect(boostNumber(left, 'left')).toBe(40); expect(boostNumber(left, 'used')).toBe(60)
    expect(makeBoost(0, 'left', 'x').limit_used_pct).toBe(100)
  })
  it('accepts only whole numbers in range and never clamps a wrong one into a right one', () => {
    expect(typedPercent('60')).toBe(60); expect(typedPercent(' 100 ')).toBe(100); expect(typedPercent('0')).toBe(0)
    for (const bad of ['', '101', '-1', '1.5', 'x', '6 0', '1000']) expect(typedPercent(bad), bad).toBeUndefined()
    expect(typedPoints('1')).toBe(1); expect(typedPoints('50')).toBe(50)
    for (const bad of ['0', '51', '', '2.5', 'ten']) expect(typedPoints(bad), bad).toBeUndefined()
  })
  it('compares settings by value, and a missing harness means the defaults', () => {
    expect(dailyOf(undefined, 'claude')).toEqual(settings())
    expect(sameDaily({ claude: settings() }, { claude: settings() })).toBe(true)
    expect(sameDaily({ claude: settings() }, { claude: settings({ at_limit: 'wait' }) })).toBe(false)
    expect(sameDaily(undefined, undefined)).toBe(true); expect(sameDaily({ claude: settings() }, undefined)).toBe(false)
  })
})

describe('what the dial remembers', () => {
  it('keeps only known keys with the right type', () => {
    expect(readDialPrefs({ info_open: true, selected: 'codex', folds: { 'codex:pace': true, 'codex:boost': false, 'x:other': true, 'codex:pace2': true, 'claude:boost': 'yes' }, extra: 1 }))
      .toEqual({ info_open: true, selected: 'codex', folds: { 'codex:pace': true, 'codex:boost': false } })
    expect(readDialPrefs({ info_open: 'yes', selected: 7 })).toEqual({})
    expect(readDialPrefs({ selected: '../etc' })).toEqual({})
    expect(readDialPrefs(null)).toEqual({}); expect(readDialPrefs([])).toEqual({})
    expect(withDialDefaults()).toEqual({ ...DEFAULT_DIAL, folds: {} })
  })
  it('selects the pick, else a harness over pace or at its limit, else the first with a daily limit, else the first row', () => {
    const rows = ['codex', 'claude', 'cursor'], no = () => false
    expect(selectedHarness(rows, 'cursor', k => k === 'claude', no)).toBe('cursor')
    expect(selectedHarness(rows, null, k => k === 'claude', () => true)).toBe('claude')
    expect(selectedHarness(rows, null, no, k => k !== 'codex')).toBe('claude')
    expect(selectedHarness(rows, null, no, no)).toBe('codex')
    expect(selectedHarness(rows, 'gemini', no, no)).toBe('codex')
    expect(selectedHarness([], 'codex', no, no)).toBeNull()
  })
})

// ---------- writes ----------
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const flush = () => new Promise<void>(resolve => setImmediate(resolve))
const snapshot = (): PlanSnapshot => ({
  total: 5, limits: { codex: 4 }, principal_id: 'p', running: {}, running_total: 0, source: 'plan', updated_at: '2026-10-09T10:00:00.000001Z',
  daily: { claude: settings(), codex: settings({ pace: { mode: 'pace', points_per_day: 12 } }) }, daily_state: {}, daily_default_points: 10, daily_timezone: 'UTC', daily_until: '2026-10-09T22:00:00Z',
})
let scope: EffectScope
beforeEach(() => {
  mocks.api.mockReset(); mocks.queue.mockReset()
  mocks.api.mockImplementation(async (path: string, init?: RequestInit) => init?.method === 'PUT' ? json({ updated_at: '2026-10-09T10:00:01.000001Z' }) : path === '/agents/plan' ? json(snapshot()) : json({ value: null }))
  mocks.queue.mockResolvedValue({ items: [], capacity: {} })
})
afterEach(() => scope?.stop())
function setup() {
  const viewer = ref('tenant:person')
  scope = effectScope()
  const control = scope.run(() => useAgentPlan(() => viewer.value, () => undefined))!
  return { control, viewer }
}
const puts = () => mocks.api.mock.calls.filter(([, init]) => init?.method === 'PUT').map(([, init]) => JSON.parse(init.body as string) as { value: { total: number; limits: Record<string, unknown>; daily?: Record<string, DailySettings> }; expected_updated_at: string | null })
describe('saving a daily setting', () => {
  it('carries every harness’s current settings and leaves the total and limits as they were', async () => {
    const { control } = setup(); await flush()
    control.saveDaily('claude', settings({ at_limit: 'wait' })); await flush()
    expect(puts()).toEqual([{ value: { total: 5, limits: { codex: 4 }, daily: { claude: settings({ at_limit: 'wait' }), codex: settings({ pace: { mode: 'pace', points_per_day: 12 } }) } }, expected_updated_at: '2026-10-09T10:00:00.000001Z' }])
  })
  it('never sends the effective daily map with a plain total or limit change', async () => {
    const { control } = setup(); await flush()
    expect(control.plan.value).toEqual({ total: 5, limits: { codex: 4 } })
    control.save({ total: 6, limits: { codex: 4 } }); await flush()
    expect(puts().map(p => Object.keys(p.value))).toEqual([['total', 'limits']])
  })
  it('shows the new setting at once, ignores an unchanged one, and restores the confirmed settings when the write fails', async () => {
    const { control } = setup(); await flush()
    control.saveDaily('claude', settings()); await flush()
    expect(puts()).toEqual([])
    mocks.api.mockImplementation(async (path: string, init?: RequestInit) => init?.method === 'PUT' ? json({ error: 'no' }, 403) : path === '/agents/plan' ? json(snapshot()) : json({ value: null }))
    control.saveDaily('claude', settings({ at_limit: 'wait' }))
    expect(control.daily.value?.claude?.at_limit).toBe('wait')
    await flush()
    expect(control.daily.value?.claude?.at_limit).toBe('ladder')
    expect(control.error.value).toBe('Couldn’t save the daily limit. Please try again.')
  })
  it('drops an edit made for another viewer', async () => {
    const { control, viewer } = setup(); await flush()
    const held = new Promise<Response>(() => {})
    mocks.api.mockImplementation((path: string, init?: RequestInit) => init?.method === 'PUT' ? held : Promise.resolve(path === '/agents/plan' ? json(snapshot()) : json({ value: null })))
    control.saveDaily('claude', settings({ at_limit: 'wait' }))
    viewer.value = 'tenant:other'; await flush()
    expect(control.daily.value?.claude?.at_limit).toBe('ladder')
  })
})
