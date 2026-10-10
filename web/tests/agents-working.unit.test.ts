// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { accountRoomCopy, accountRoomDetail, effectiveLimit, liveCopy, modeLimit, nextLimitMode, noOwnTip, nowCopy, setLimit, statusCopy, stepHarnessLimit, stepLimit, stepTotal, typedLimit, typedTotal, waitingCopy, workingAccountRoom, workingRows, type HarnessLimit, type PlanSnapshot } from '../src/lib/agentsWorking'
import { buildPools, buildRows, defaultSchedule, type AccountCapacity, type AccountInput, type CapacityRouting, type CapacityWindow } from '../src/lib/capacity'
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
      ['codex', 'max', 0, 'at most 0'], ['claude', 'none', 3, 'no own limit · up to 3 now'], ['cursor', 'off', 0, 'no new starts'],
    ])
    expect(stepLimit(input, 'claude', 3, 1).limits.claude).toBe('no_limit')
    expect(stepLimit(input, 'claude', 3, -1).limits.claude).toBe(2)
    expect(stepLimit(input, 'cursor', 0, 1).limits.cursor).toBe(1)
    expect(stepLimit({ total: 5, limits: { codex: 1 } }, 'codex', 5, -1).limits.codex).toBe('off')
    expect(stepLimit({ total: 5, limits: { codex: 30 } }, 'codex', 5, 1).limits.codex).toBe('no_limit')
  })
  it('caps a harness without its own limit by the total and measured account room', () => {
    expect(effectiveLimit(5, 1, 2)).toBe(3)
    expect(effectiveLimit(5, 12, 3)).toBe(5)
    expect(effectiveLimit(0, 12, 3)).toBe(0)
    expect(effectiveLimit(5, 0, null)).toBe(5)
    expect(noOwnTip('claude', 0)).toBe('Claude has no limit of its own; with 0 at once, nothing new starts.')
  })
  it('walks the entire ladder with the same ends in both views', () => {
    let value: HarnessLimit = 'off'
    expect(stepHarnessLimit(value, 5, -1)).toBe('off')
    for (let n = 1; n <= 30; n++) {
      value = stepHarnessLimit(value, 5, 1)
      expect(value).toBe(n)
    }
    expect(stepHarnessLimit(value, 5, 1)).toBe('no_limit')
    expect(stepHarnessLimit('no_limit', 5, 1)).toBe('no_limit')
    expect(stepHarnessLimit('no_limit', 5, -1)).toBe(4)
    expect(stepHarnessLimit(undefined, 30, -1)).toBe(29)
    expect(stepHarnessLimit('no_limit', 1, -1)).toBe('off')
    expect(stepHarnessLimit('no_limit', 0, -1)).toBe('off')
    for (let n = 30; n > 1; n--) expect(stepHarnessLimit(n, 5, -1)).toBe(n - 1)
    expect(stepHarnessLimit(1, 5, -1)).toBe('off')
    expect(stepHarnessLimit(0, 5, -1)).toBe(0)
    expect(stepHarnessLimit(0, 5, 1)).toBe(1)
    expect(workingRows({ total: 5, limits: { codex: 0 } }, snapshot, {}, null)[0]!.shown).toBe(0)
  })
  it('accepts typed limits and totals, clamps large values, and rejects invalid drafts', () => {
    expect(typedLimit('')).toBe('no_limit')
    expect(typedLimit('  ')).toBe('no_limit')
    expect(typedLimit('0')).toBe('off')
    expect(typedLimit('001')).toBe(1)
    expect(typedLimit(' 29 ')).toBe(29)
    expect(typedLimit('31')).toBe(30)
    expect(typedLimit('9'.repeat(64))).toBe(30)
    expect(typedTotal('0')).toBe(0)
    expect(typedTotal('30')).toBe(30)
    expect(typedTotal('100')).toBe(30)
    for (const invalid of ['-1', '+2', '1.5', '3e2', 'Infinity', '∞', 'off', '1 2', '9'.repeat(65), ' '.repeat(65)]) {
      expect(typedLimit(invalid)).toBeUndefined()
      expect(typedTotal(invalid)).toBeUndefined()
    }
    expect(typedTotal('')).toBeUndefined()
  })
  it('cycles no own limit, at most, and off, restoring a bounded remembered ceiling', () => {
    let value: HarnessLimit = 'no_limit'
    value = modeLimit(value, nextLimitMode(value), 7); expect(value).toBe(7)
    value = modeLimit(value, nextLimitMode(value), 7); expect(value).toBe('off')
    value = modeLimit(value, nextLimitMode(value), 7); expect(value).toBe('no_limit')
    expect(modeLimit(undefined, 'max')).toBe(2)
    expect(modeLimit('off', 'max', 50)).toBe(30)
    expect(modeLimit('off', 'max', 0)).toBe(1)
    expect(modeLimit(0, 'max', 7)).toBe(1)
  })
  it('uses the approved wind-down, zero, full and room wording', () => {
    expect(liveCopy(5, 16, 7)).toBe('16 running · winding down to 5')
    expect(nowCopy(5, 16, 7)).toBe('16 are running, 11 more than 5 at once; they finish before anything new starts.')
    expect(liveCopy(0, 2, 6)).toBe('2 running · nothing new starts')
    expect(nowCopy(0, 0, 6)).toBe('Nothing new starts, and nothing is running.')
    expect(liveCopy(8, 8, 2)).toBe('8 running · all 8 in use')
    expect(liveCopy(8, 3, 0, true)).toBe('3 running · accounts full')
    expect(statusCopy(8, 3, 0, true)).toBe('Room for 5 more in the total; all account start slots are occupied.')
    expect(nowCopy(8, 3, 2)).toBe('3 are running; 5 more would fit the 8 at once, but the accounts have room for 2.')
    expect(nowCopy(8, 3, null)).toContain('Account room is not measured yet.')
  })
  it('reports actual waiting work without simulating starts or inventing an empty queue', () => {
    const room = { codex: 3, claude: 2, cursor: 2 }
    expect(waitingCopy(snapshot, snapshot, room, null)).toBe('Waiting work is not available yet.')
    expect(waitingCopy(snapshot, snapshot, room, [])).toBe('No work queued.')
    expect(waitingCopy(snapshot, snapshot, room, [{}, {}])).toBe('2 waiting: winding down first')
    const under = { ...snapshot, running: { cursor: 0 }, running_total: 0 }
    expect(waitingCopy(snapshot, under, room, [{ harness: 'cursor' }])).toBe('1 waiting: Cursor is off')
    expect(waitingCopy(snapshot, under, room, [{}])).toBe('1 waiting: ready for a start')
    expect(waitingCopy(snapshot, under, { codex: 0, claude: 0, cursor: 0 }, [{}])).toBe('1 waiting for room on an account')
  })
  it('never calls missing account ownership or routing a full account', () => {
    const now = Date.parse('2026-10-05T12:00:00Z')
    const account = (slots: number, measured = true) => buildRows(
      [{ id: 'owned', label: 'Owned', harness: 'codex', host: 'workstation', state: 'available', last_probe_ok: true }],
      [{ account_id: 'owned', windows: [], schedule: defaultSchedule('UTC'), routing: measured ? { rank: 1, available_slots: slots } : undefined }],
    )
    expect(workingAccountRoom([], now, true, ['codex', 'claude']).total).toBeNull()
    expect(workingAccountRoom(account(17), now, false, ['codex']).total).toBeNull()
    expect(workingAccountRoom(account(0, false), now, true, ['codex']).total).toBeNull()
    const known = workingAccountRoom(account(17), now, true, ['codex', 'claude', 'cursor'])
    expect(known.room).toEqual({ codex: 17, claude: 0, cursor: 0 })
    expect(known.total).toBe(17)
    expect(known.full).toBe(false)
    expect(workingAccountRoom(account(0), now, true, ['codex']).total).toBe(0)
    expect(liveCopy(20, 3, known.total)).toBe('3 running · room for 17 more')
    expect(liveCopy(20, 3, 2)).toBe('3 running · room for 2 more')
    expect(liveCopy(20, 0, null)).toBe('0 running · account room not measured yet')
  })
  it('includes all configured harnesses and counts beyond a lowered ceiling', () => {
    const rows = workingRows({ total: 1, limits: { grok: 30, pi: 'off', gemini: 'no_limit', opencode: 0 } }, snapshot, {}, null)
    expect(rows.map(r => r.key)).toEqual(['codex', 'claude', 'grok', 'cursor', 'pi', 'gemini', 'opencode'])
    expect(rows[0]!.sub).toBe('12 running')
    expect(workingRows(snapshot, snapshot, {}, null)[0]!.sub).toBe('12 running, 8 above, finishing')
  })
})

describe('honest account room (AEON-720)', () => {
  const now = Date.parse('2026-10-05T12:00:00Z')
  const window: CapacityWindow = {
    reading: { window_kind: 'weekly', window_minutes: 10080, used_percent: 10, resets_at: '2026-10-08T12:00:00Z', source: 'harness', read_at: '2026-10-05T11:59:00Z' },
    starts_at: '2026-10-01T12:00:00Z', allowance: 100, remaining_percent: 90, freshness: 'fresh',
    pacing: { usable_hours: 40, percent_per_hour: 1, suggested_today_percent: 15 },
  }
  const input = (harness: string, id = harness): AccountInput => ({ id, label: id, harness, host: 'workstation', state: 'available', last_probe_ok: true })
  const cap = (account_id: string, routing?: CapacityRouting, windows = [window]): AccountCapacity => ({ account_id, routing, windows, schedule: defaultSchedule('UTC') })
  const ready = (slots: number): CapacityRouting => ({ rank: 1, available_slots: slots })

  it('carries real API room through accounts, pools and the dial when nothing runs', () => {
    const inputs = ['codex', 'claude', 'cursor'].map(h => input(h))
    const capacities = [cap('codex', ready(3)), cap('claude', ready(2)), cap('cursor', ready(1))]
    const room = workingAccountRoom(buildRows(inputs, capacities), now, true)
    expect(room.room).toEqual({ codex: 3, claude: 2, cursor: 1 })
    expect(room.total).toBe(6)
    expect(room.full).toBe(false)
    expect(accountRoomCopy(room)).toBe('Room for 6 more right now.')
    expect(accountRoomDetail(room, 'codex')).toBe('Codex room for 3 more')
    expect(liveCopy(8, 0, room.total)).toBe('0 running · room for 6 more')
  })
  it('never labels a missing capacity response or reading as zero or full', () => {
    for (const capacity of [[], [cap('codex')], [cap('codex', { ...ready(0), rank: 0, wait: { code: 'reading', run_now_allowed: false } }, [])]]) {
      const room = workingAccountRoom(buildRows([input('codex')], capacity), now, true)
      expect(room.room.codex).toBeNull()
      expect(room.total).toBeNull()
      expect(room.full).toBe(false)
      expect(accountRoomCopy(room)).toContain('not measured yet')
      expect(accountRoomDetail(room, 'codex')).toMatch(/not measured yet — .+/)
      expect(liveCopy(5, 0, room.total)).not.toMatch(/full|room for 5/)
    }
  })
  it('retains server start advice while explaining unmeasured usage', () => {
    const room = workingAccountRoom(buildRows([input('codex'), input('cursor')], [cap('codex', ready(2), []), cap('cursor', ready(3), [])]), now, true)
    expect(room.total).toBe(5)
    expect(accountRoomDetail(room, 'codex')).toContain('room for 2 more — quota not measured yet — a managed run must report current usage')
    expect(accountRoomDetail(room, 'cursor')).toContain('quota not measured yet — the harness does not report usage')
  })
  it('keeps known room when another harness has no current advice', () => {
    const room = workingAccountRoom(buildRows([input('codex'), input('claude')], [cap('codex', ready(2))]), now, true)
    expect(room.room.claude).toBeNull()
    expect(room.total).toBe(2)
    expect(accountRoomCopy(room)).toBe('Room for at least 2 more right now.')
  })
  it('explains routing blockers without calling scheduled, held or unapproved accounts full', () => {
    for (const code of ['schedule', 'hold', 'approval', 'models', 'offline', 'vendor'] as const) {
      const room = workingAccountRoom(buildRows([input('codex')], [cap('codex', { ...ready(0), rank: 0, wait: { code, run_now_allowed: false } })]), now, true)
      expect(room.total).toBe(0)
      expect(room.full).toBe(false)
      expect(accountRoomCopy(room)).toBe('No account starts are available right now.')
      expect(accountRoomDetail(room, 'codex')).toBe(`Codex ${room.reasons.codex}`)
      expect(room.reasons.codex).not.toBe('')
      const idle = { ...snapshot, total: 5, limits: {}, running: {}, running_total: 0 }
      expect(waitingCopy(idle, idle, room.room, [{ harness: 'codex' }], room.reasons)).toBe(`1 waiting: Codex: ${room.reasons.codex}`)
    }
    const unread = workingAccountRoom(buildRows([input('codex')], [cap('codex', { ...ready(0), rank: 0, wait: { code: 'schedule', run_now_allowed: true } }, [])]), now, true)
    expect(accountRoomDetail(unread, 'codex')).toContain('outside scheduled hours; quota not measured yet')
    expect(unread.total).toBe(0)
    expect(unread.full).toBe(false)
  })
  it('reserves full for explicitly occupied slots and keeps absent accounts distinct', () => {
    const room = workingAccountRoom(buildRows([input('codex')], [cap('codex', { ...ready(0), rank: 0, wait: { code: 'capacity', run_now_allowed: false } })]), now, true)
    expect(room.full).toBe(true)
    expect(accountRoomCopy(room)).toContain('all start slots are occupied')
    const absent = workingAccountRoom([], now, true)
    expect(absent.full).toBe(false)
    expect(accountRoomCopy(absent)).toBe('No linked accounts.')
    expect(accountRoomDetail(absent, 'codex')).toBe('Codex no linked accounts')
  })
  // Risk: context denial loses its reason in the working dial and queued work
  // because the shared wait-code map omits the new server reason.
  it('explains project context denial in account room and waiting work', () => {
    const room = workingAccountRoom(buildRows([input('codex')], [cap('codex', { ...ready(0), rank: 0, wait: { code: 'context', run_now_allowed: false } })]), now, true)
    expect(room.room.codex).toBe(0)
    expect(room.full).toBe(false)
    expect(room.reasons.codex).toBe('no account is allowed for this project’s context')
    expect(accountRoomDetail(room, 'codex')).toBe('Codex no account is allowed for this project’s context')
    const idle = { ...snapshot, total: 5, limits: {}, running: {}, running_total: 0 }
    expect(waitingCopy(idle, idle, room.room, [{ harness: 'codex' }], room.reasons)).toBe('1 waiting: Codex: no account is allowed for this project’s context')
  })
  it('rejects stale projections and never doubles shared quota aliases across groups', () => {
    const rows = buildRows([input('codex', 'a'), input('codex', 'b')], [
      { ...cap('a', ready(3)), quota_fingerprint: 'shared', group_id: 'one' },
      { ...cap('b', { ...ready(0), same_quota_as: 'a' }), quota_fingerprint: 'shared', group_id: 'two', same_quota_as: 'a' },
    ])
    expect(workingAccountRoom(rows, now, true).total).toBe(3)
    const stale = workingAccountRoom(rows, now, false)
    expect(stale.total).toBeNull()
    expect(accountRoomDetail(stale, 'codex')).toContain('account information is unavailable')
  })
  it('claims readiness only from confirmed room when unknown and zero room mix', () => {
    const idle = { ...snapshot, total: 5, limits: {}, running: {}, running_total: 0 }
    const room = workingAccountRoom(buildRows([input('codex')], [cap('codex', { ...ready(0), rank: 0, wait: { code: 'reading', run_now_allowed: false } }, [])]), now, true)
    expect(room.room).toEqual({ codex: null, claude: 0, cursor: 0 })
    const copy = waitingCopy(idle, idle, room.room, [{}], room.reasons)
    expect(copy).not.toContain('ready for a start')
    expect(copy).toBe('1 waiting: Codex: not measured yet — a current reading is missing; Claude: no linked accounts; Cursor: no linked accounts')
    expect(waitingCopy(idle, idle, { codex: null, claude: 0 }, [{}])).toBe('1 waiting: Codex: account room not measured yet; Claude: no start slots available')
    expect(waitingCopy(idle, idle, { codex: null, claude: 2, cursor: 0 }, [{}])).toBe('1 waiting: ready for a start')
  })
  it('resolves a shared-quota alias measurement from the canonical gauge', () => {
    const rows = buildRows([input('codex', 'a'), input('codex', 'b')], [
      { ...cap('a', ready(3)), quota_fingerprint: 'shared', group_id: 'one' },
      { ...cap('b', { ...ready(0), same_quota_as: 'a' }), quota_fingerprint: 'shared', group_id: 'two', same_quota_as: 'a' },
    ])
    expect(buildPools(rows, now).flatMap(p => p.rows).find(r => r.id === 'b')!.primary).toBeNull()
    const room = workingAccountRoom(rows, now, true)
    expect(room.total).toBe(3)
    expect(room.reasons.codex).toBe('')
    expect(accountRoomDetail(room, 'codex')).toBe('Codex room for 3 more')
    const unmeasured = buildRows([input('codex', 'a'), input('codex', 'b')], [
      { ...cap('a', ready(3), []), quota_fingerprint: 'shared', group_id: 'one' },
      { ...cap('b', { ...ready(0), same_quota_as: 'a' }, []), quota_fingerprint: 'shared', group_id: 'two', same_quota_as: 'a' },
    ])
    expect(workingAccountRoom(unmeasured, now, true).reasons.codex).toBe('quota not measured yet — a managed run must report current usage')
  })
  it('never turns unused ceiling positions into waiting work', () => {
    const idle = { ...snapshot, total: 5, limits: {}, running: {}, running_total: 0 }
    for (const room of [null, 0, 2, 5]) {
      expect(nowCopy(5, 0, room)).not.toMatch(/wait|the other/)
      expect(statusCopy(5, 0, room)).not.toContain('wait')
      expect(waitingCopy(idle, idle, { codex: room }, [])).toBe('No work queued.')
    }
    expect(waitingCopy(idle, idle, { codex: null }, [{ harness: 'codex' }])).toBe('1 waiting: account room is not measured yet')
  })
})
