// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1175: pace is one stored value with two editors (Settings › Capacity and the
// Agents dial). Risk: the editors store it differently, or a write overwrites a change
// made elsewhere. Both go through storedPace and putAgentsPlan.
import { beforeEach, describe, expect, it, vi } from 'vitest'
const mocks = vi.hoisted(() => ({ api: vi.fn() }))
vi.mock('../src/lib/api.ts', () => ({ api: mocks.api }))
import { paceChoice, PlanChanged, putAgentsPlan, storedPace, withDaily, writePace, type DailyMap, type PlanRead } from '../src/lib/dailyLimits'

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const daily: DailyMap = {
  claude: { pace: { mode: 'pace', points_per_day: 15 }, boost_today: { limit_used_pct: 60, entered_as: 'used', until: '2026-09-29T22:00:00Z' }, at_limit: 'wait' },
  codex: { pace: { mode: 'pace', points_per_day: null }, boost_today: null, at_limit: 'ladder' },
}
const plan: PlanRead = { total: 12, limits: { codex: 6, claude: 2 }, updated_at: '2026-10-11T08:00:00.000001Z', daily, daily_default_points: 12 }
beforeEach(() => { mocks.api.mockReset() })

describe('the stored pace', () => {
  it('follows the default when the number equals it, keeps the number under "use everything", and reads back as the choice', () => {
    const own = { mode: 'pace' as const, points_per_day: 15 }
    expect(storedPace(own, { mode: 'own', points: 12 }, 12)).toEqual({ mode: 'pace', points_per_day: null })
    expect(storedPace(own, { mode: 'own', points: 14 }, 12)).toEqual({ mode: 'pace', points_per_day: 14 })
    expect(storedPace(own, { mode: 'everything' }, 12)).toEqual({ mode: 'everything', points_per_day: 15 })
    expect(storedPace(own, { mode: 'default' }, 12)).toEqual({ mode: 'pace', points_per_day: null })
    expect(paceChoice({ mode: 'everything', points_per_day: 15 })).toEqual({ mode: 'everything' })
    expect(paceChoice({ mode: 'pace', points_per_day: null })).toEqual({ mode: 'default' })
    expect(paceChoice(own)).toEqual({ mode: 'own', points: 15 })
  })
  it('changes one harness and copies the others, never sharing their objects', () => {
    const next = withDaily(daily, 'codex', { ...daily.codex!, pace: { mode: 'everything', points_per_day: null } })
    expect(next.codex!.pace).toEqual({ mode: 'everything', points_per_day: null })
    expect(next.claude).toEqual(daily.claude)
    expect(next.claude).not.toBe(daily.claude)
    expect(daily.codex!.pace).toEqual({ mode: 'pace', points_per_day: null })
  })
})

describe('the guarded write', () => {
  it('writes the plan as read with one pace changed, guarded by the revision it read', async () => {
    mocks.api.mockResolvedValue(json({ key: 'agents.working', updated_at: '2026-10-11T08:01:00.000002Z' }))
    await expect(writePace(plan, 'codex', storedPace(daily.codex!.pace, { mode: 'own', points: 20 }, 12))).resolves.toBe('2026-10-11T08:01:00.000002Z')
    const [path, init] = mocks.api.mock.calls[0]!
    expect(path).toBe('/preferences/agents.working')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body)).toEqual({
      expected_updated_at: '2026-10-11T08:00:00.000001Z',
      value: { total: 12, limits: { codex: 6, claude: 2 }, daily: { ...daily, codex: { pace: { mode: 'pace', points_per_day: 20 }, boost_today: null, at_limit: 'ladder' } } },
    })
  })
  it('a harness the plan has no settings for starts from the defaults', async () => {
    mocks.api.mockResolvedValue(json({ updated_at: '2026-10-11T08:01:00Z' }))
    await writePace({ ...plan, daily: undefined }, 'grok', storedPace({ mode: 'pace', points_per_day: null }, { mode: 'everything' }, 12))
    expect(JSON.parse(mocks.api.mock.calls[0]![1].body).value.daily).toEqual({ grok: { pace: { mode: 'everything', points_per_day: null }, boost_today: null, at_limit: 'ladder' } })
  })
  it('a 409 is PlanChanged, any other failure or an unconfirmed save an error, never a success', async () => {
    mocks.api.mockResolvedValueOnce(json({ error: 'changed' }, 409))
    await expect(writePace(plan, 'codex', storedPace(daily.codex!.pace, { mode: 'default' }, 12))).rejects.toBeInstanceOf(PlanChanged)
    mocks.api.mockResolvedValueOnce(json({ error: 'nope' }, 500))
    await expect(putAgentsPlan({ total: 1, limits: {} }, null, { failure: 'Couldn’t save the total and limits.' })).rejects.toThrow('Couldn’t save the total and limits.')
    mocks.api.mockResolvedValueOnce(json({ key: 'agents.working' }))
    await expect(putAgentsPlan({ total: 1, limits: {} }, null)).rejects.toThrow('Couldn’t confirm the save. Please try again.')
    expect(new PlanChanged().message).toBe('Changed elsewhere; shown as it is now.')
  })
})
