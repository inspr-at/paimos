// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { effectiveLimit, liveCopy, noOwnTip, nowCopy, setLimit, statusCopy, stepExpandedLimit, stepLimit, stepTotal, waitingCopy, workingRows, type PlanSnapshot } from '../src/lib/agentsWorking'
const snapshot: PlanSnapshot = { total: 5, limits: { codex: 4, claude: 2, cursor: 'off' }, principal_id: 'owner', running: { codex: 12, claude: 1, cursor: 3 }, running_total: 16, source: 'plan', updated_at: null }
describe('the one dial', () => {
  it('changes only the ceiling, preserving running agents and independent harness limits', () => {
    const lower = stepTotal(snapshot, -1)
    expect(lower).toEqual({ total: 4, limits: snapshot.limits })
    expect(snapshot.running_total).toBe(16)
    expect(snapshot.running).toEqual({ codex: 12, claude: 1, cursor: 3 })
    expect(stepTotal({ total: 0, limits: {} }, -1).total).toBe(0)
    expect(stepTotal({ total: 30, limits: {} }, 1).total).toBe(30)
    expect(setLimit(lower, 'claude', 30)).toEqual({ total: 4, limits: { codex: 4, claude: 30, cursor: 'off' } })
  })
  it('keeps no own limit, numeric zero and off distinct', () => {
    const input = { total: 5, limits: { codex: 0, claude: 'no_limit' as const, cursor: 'off' as const } }
    const rows = workingRows(input, snapshot, { codex: 3, claude: 2, cursor: 2 }, null)
    expect(rows.map(r => [r.key, r.mode, r.shown, r.words])).toEqual([
      ['codex', 'max', 0, 'at most 0'], ['claude', 'none', 3, 'up to 3 · no own limit'], ['cursor', 'off', 0, 'no new starts'],
    ])
    expect(stepLimit(input, 'claude', 3, 1).limits.claude).toBe(4)
    expect(stepLimit(input, 'cursor', 0, 1).limits.cursor).toBe(1)
    expect(stepLimit({ total: 5, limits: { codex: 1 } }, 'codex', 5, -1).limits.codex).toBe('off')
    expect(stepLimit({ total: 5, limits: { codex: 30 } }, 'codex', 5, 1).limits.codex).toBe(30)
  })
  it('caps a harness without its own limit by the total and measured account room', () => {
    expect(effectiveLimit(5, 1, 2)).toBe(3)
    expect(effectiveLimit(5, 12, 3)).toBe(5)
    expect(effectiveLimit(0, 12, 3)).toBe(0)
    expect(effectiveLimit(5, 0, null)).toBe(5)
    expect(noOwnTip('claude', 0)).toBe('Claude has no limit of its own; with 0 at once, nothing new starts.')
  })
  it('expanded limits stop at one, while a stored API zero stays visible until increased', () => {
    const input = { total: 5, limits: { codex: 1 } }
    expect(stepExpandedLimit(input, 'codex', 1, -1).limits.codex).toBe(1)
    expect(stepExpandedLimit({ ...input, limits: { codex: 0 } }, 'codex', 0, 1).limits.codex).toBe(1)
    expect(workingRows({ ...input, limits: { codex: 0 } }, snapshot, {}, null)[0]!.shown).toBe(0)
    expect(stepLimit(input, 'codex', 5, -1).limits.codex).toBe('off')
  })
  it('uses the approved wind-down, zero, full and room wording', () => {
    expect(liveCopy(5, 16, 7)).toBe('16 running · winding down to 5')
    expect(nowCopy(5, 16, 7)).toBe('16 are running, 11 more than 5 at once; they finish before anything new starts.')
    expect(liveCopy(0, 2, 6)).toBe('2 running · nothing new starts')
    expect(nowCopy(0, 0, 6)).toBe('Nothing new starts, and nothing is running.')
    expect(liveCopy(8, 8, 2)).toBe('8 running · all 8 in use')
    expect(liveCopy(8, 3, 0)).toBe('3 running · accounts full')
    expect(statusCopy(8, 3, 0)).toBe('Room for 5 more, but the accounts are full: new starts wait for room.')
    expect(nowCopy(8, 3, 2)).toBe('3 are running; 5 more would fit the 8 at once, but the accounts have room for 2.')
    expect(nowCopy(8, 3, null)).toContain('Account room is not available yet.')
  })
  it('reports actual waiting work without simulating starts or inventing an empty queue', () => {
    const room = { codex: 3, claude: 2, cursor: 2 }
    expect(waitingCopy(snapshot, snapshot, room, null)).toBe('Waiting work is not available yet.')
    expect(waitingCopy(snapshot, snapshot, room, [])).toBe('No work waiting.')
    expect(waitingCopy(snapshot, snapshot, room, [{}, {}])).toBe('2 waiting: winding down first')
    const under = { ...snapshot, running: { cursor: 0 }, running_total: 0 }
    expect(waitingCopy(snapshot, under, room, [{ harness: 'cursor' }])).toBe('1 waiting: Cursor is off')
    expect(waitingCopy(snapshot, under, room, [{}])).toBe('1 waiting: ready for a start')
    expect(waitingCopy(snapshot, under, { codex: 0, claude: 0, cursor: 0 }, [{}])).toBe('1 waiting for room on an account')
  })
  it('includes all configured harnesses and counts beyond a lowered ceiling', () => {
    const rows = workingRows({ total: 1, limits: { grok: 30, pi: 'off', gemini: 'no_limit', opencode: 0 } }, snapshot, {}, null)
    expect(rows.map(r => r.key)).toEqual(['codex', 'claude', 'grok', 'cursor', 'pi', 'gemini', 'opencode'])
    expect(rows[0]!.sub).toBe('12 running')
    expect(workingRows(snapshot, snapshot, {}, null)[0]!.sub).toBe('12 running, 8 above, finishing')
  })
})
