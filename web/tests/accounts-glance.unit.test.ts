// SPDX-License-Identifier: AGPL-3.0-only
// AEON-782: the Agents page's status line for Accounts and computers. The words
// are the design's ("All 3 ready · 2 computers online", "2 of 3 ready · Claude
// needs verifying on build-7 · +1") and the count is honest: an account is ready
// only with a connected sign-in that says Ready and nothing to verify. A missing
// report and a revoked snapshot are not ready. An old report on an offline
// computer is not a fault, and a transient hold is not a request for action.
import { describe, expect, it } from 'vitest'
import type { PairingView } from '../src/lib/agentPairing'
import type { OverviewAccount } from '../src/lib/accountsOverview'
import { initialRefreshStatus, refreshStatus } from '../src/lib/usePolledData'
import { readiness } from '../src/lib/computerAccounts'
import { glanceAvailability, glanceChipText, glanceFlag, glanceItems, glanceSummary, glanceTiles, onlineComputers, signinStatus } from '../src/lib/accountsGlance'

function door(id: string, over: Record<string, unknown> = {}) {
  return {
    id, name: id, host: 'build-7', harness: 'claude', state: 'live', primary: null, five: null, schedule: null, plan: '',
    limitingReset: '', awaitingReading: false, fingerprint: '', groupId: '', groupName: '', hosts: ['build-7'], sameQuotaAs: '',
    ...over,
  }
}

const NOW = Date.parse('2026-10-06T12:00:00Z')
const LOW = { early_percent: 10, urgent_percent: 3 }

function computer(name: string, harnesses: Record<string, string>, over: Partial<PairingView> = {}): PairingView {
  return {
    computer_id: `c-${name}`, computer_name: name, computer_state: 'connected', connectivity: 'online', revision: 1,
    harness_statuses: harnesses, harness_details: Object.fromEntries(Object.entries(harnesses).map(([h, state]) => [h, { state }])),
    enrollments: Object.keys(harnesses).map(h => ({ account_id: `a-${h}`, harness: h, label: h, state: 'connected', verification_state: 'completed' })),
    ...over,
  } as unknown as PairingView
}
function account(harness: string, computers: PairingView[], remaining?: number): OverviewAccount {
  return {
    id: `a-${harness}`, vendor: harness[0]!.toUpperCase() + harness.slice(1), harness, identity: harness, records: [{ id: `a-${harness}` }], rows: [],
    signins: computers.flatMap(c => c.enrollments.filter(e => e.account_id === `a-${harness}`).map(enrollment => ({ computer: c, enrollment }))),
    windows: remaining === undefined ? [] : [{ remaining_percent: remaining, freshness: 'fresh', reading: { window_kind: 'weekly', resets_at: '2026-10-09T08:00:00Z', read_at: '2026-10-06T11:55:00Z' } }],
  } as unknown as OverviewAccount
}

describe('the status line', () => {
  const mbp = computer('build-7', { claude: 'ready', codex: 'ready' }), studio = computer('build-6', { codex: 'ready', cursor: 'ready' })
  const calm = [account('claude', [mbp, studio]), account('codex', [mbp, studio], 60), account('cursor', [mbp, studio])]

  it('says all ready with the computers online when nothing blocks agents', () => {
    const items = glanceItems(calm, [mbp, studio], LOW, NOW)
    expect(items).toEqual([])
    expect(glanceSummary(calm, [mbp, studio], items, NOW)).toMatchObject({ tone: 'ok', text: 'All 3 ready · 2 computers online' })
  })

  it('names the first thing that blocks agents and counts the rest, so the line is the whole answer', () => {
    const expired = computer('build-7', { claude: 'ready', codex: 'ready' })
    Object.assign(expired.enrollments[0]!, { verification_state: 'expired', verification_expired_ready: false })
    const accounts = [account('claude', [expired, studio]), account('codex', [expired, studio], 9), account('cursor', [expired, studio])]
    const items = glanceItems(accounts, [expired, studio], LOW, NOW)
    expect(items.map(i => i.name)).toEqual(['Claude needs verifying on build-7', 'Codex is low: 9% left this week'])
    expect(glanceSummary(accounts, [expired, studio], items, NOW)).toMatchObject({ tone: 'warn', text: '2 of 3 ready · Claude needs verifying on build-7 · +1' })
  })

  it('does not call a computer that stopped reporting broken, and does not ask for action while a repin settles', () => {
    const offline = computer('build-7', { claude: 'blocked' }, { connectivity: 'offline', harness_details: { claude: { state: 'blocked', reason: 'dependency_invalid' } } } as never)
    const settling = computer('build-6', { codex: 'blocked' }, { harness_details: { codex: { state: 'blocked', reason: 'repin_pending' } } } as never)
    const accounts = [account('claude', [offline]), account('codex', [settling])]
    expect(glanceItems(accounts, [offline, settling], LOW, NOW)).toEqual([])
    expect(glanceSummary(accounts, [offline, settling], [], NOW)).toMatchObject({ tone: 'warn', text: '0 of 2 ready · 2 not ready' })
  })

  it('does not count a draining account or an account on Hold as ready', () => {
    const mbp = computer('build-7', { claude: 'ready' })
    // A draining account is projected as paused while its sign-in can still say Ready.
    const draining = account('claude', [mbp])
    draining.rows = [door('a-claude', { state: 'paused' })] as never
    expect(glanceSummary([draining], [mbp], [], NOW)).toMatchObject({ ready: 0, total: 1, text: '0 of 1 ready · 1 not ready' })
    const held = account('claude', [mbp])
    held.rows = [door('a-claude', { schedule: { override: 'hold' } })] as never
    expect(glanceSummary([held], [mbp], [], NOW)).toMatchObject({ ready: 0, total: 1, text: '0 of 1 ready · 1 not ready' })
    const routed = account('claude', [mbp])
    routed.rows = [door('a-claude', { routing: { rank: 0, available_slots: 0, wait: { code: 'hold' } } })] as never
    expect(glanceSummary([routed], [mbp], [], NOW)).toMatchObject({ ready: 0, total: 1, text: '0 of 1 ready · 1 not ready' })
  })

  it('keeps a shared login as one account, ready when one door still is', () => {
    const mbp = computer('build-7', { claude: 'ready' })
    const studio = computer('build-6', { claude: 'ready' })
    mbp.enrollments[0]!.account_id = 'door-1'
    studio.enrollments[0]!.account_id = 'door-2'
    const signins = [
      { computer: mbp, enrollment: mbp.enrollments[0]! },
      { computer: studio, enrollment: studio.enrollments[0]! },
    ]
    const held = account('claude', [])
    held.id = 'claude:shared'
    held.signins = signins as never
    held.rows = [door('door-1', { schedule: { override: 'hold' } }), door('door-2', { schedule: { override: 'hold' } })] as never
    expect(glanceSummary([held], [mbp, studio], [], NOW)).toMatchObject({ ready: 0, total: 1, text: '0 of 1 ready · 1 not ready' })
    const mixed = account('claude', [])
    mixed.id = 'claude:shared'
    mixed.signins = signins as never
    mixed.rows = [door('door-1', { state: 'paused' }), door('door-2')] as never
    expect(glanceSummary([mixed], [mbp, studio], [], NOW)).toMatchObject({ ready: 1, total: 1, text: 'All 1 ready · 2 computers online' })
  })

  it('lists a profile-permissions block in Needs you', () => {
    const mbp = computer('build-7', { pi: 'blocked' }, { harness_details: { pi: { state: 'blocked', reason: 'profile_permissions' } } } as never)
    const accounts = [account('pi', [mbp])]
    const items = glanceItems(accounts, [mbp], LOW, NOW)
    expect(items).toEqual([expect.objectContaining({ kind: 'attention', name: 'Pi needs attention on build-7', detail: 'Profile permissions need repair' })])
    expect(glanceSummary(accounts, [mbp], items, NOW).text).toBe('0 of 1 ready · Pi needs attention on build-7')
  })

  it('does not treat a missing sign-in report as ready', () => {
    const quiet = computer('build-7', {}, {
      enrollments: [{ account_id: 'a-claude', harness: 'claude', label: 'claude', state: 'connected', verification_state: 'completed' }],
    })
    const missing = account('claude', [quiet])
    missing.rows = [door('a-claude')] as never
    expect(signinStatus(quiet, 'a-claude')).toBe('Not reported')
    const items = glanceItems([missing], [quiet], LOW, NOW)
    expect(items).toEqual([])
    expect(glanceSummary([missing], [quiet], items, NOW)).toMatchObject({ ready: 0, total: 1, tone: 'warn', text: '0 of 1 ready · 1 not ready' })
  })

  it('does not treat a revoked computer snapshot as ready', () => {
    const revoked = computer('build-7', { claude: 'ready' }, { computer_state: 'revoked' })
    const gone = account('claude', [revoked])
    gone.rows = [door('a-claude')] as never
    expect(signinStatus(revoked, 'a-claude')).toBe('Blocked')
    expect(onlineComputers([revoked])).toBe(0)
    expect(glanceSummary([gone], [revoked], [], NOW)).toMatchObject({ ready: 0, total: 1, tone: 'warn', text: '0 of 1 ready · 1 not ready' })
  })

  it('does not treat a revoked enrollment snapshot as ready', () => {
    const live = computer('build-7', { claude: 'ready' })
    live.enrollments[0]!.state = 'revoked'
    const blocked = account('claude', [live])
    blocked.rows = [door('a-claude')] as never
    expect(signinStatus(live, 'a-claude')).toBe('Blocked')
    expect(onlineComputers([live])).toBe(1)
    expect(glanceSummary([blocked], [live], [], NOW)).toMatchObject({ ready: 0, total: 1, tone: 'warn', text: '0 of 1 ready · 1 not ready' })
  })

  it('rejects an estimated quota, a reading older than ten minutes, and a window that already reset', () => {
    const mbp = computer('build-7', { codex: 'ready' })
    const estimated = account('codex', [mbp], 2)
    estimated.windows[0]!.reading.source = 'estimate'
    expect(glanceItems([estimated], [mbp], LOW, NOW).map(item => item.kind)).not.toContain('quota')
    const reset = account('codex', [mbp], 2)
    reset.windows[0]!.reading.source = 'harness'
    reset.windows[0]!.reading.read_at = new Date(NOW - 60_000).toISOString()
    reset.windows[0]!.reading.resets_at = new Date(NOW - 25 * 60_000).toISOString()
    expect(glanceItems([reset], [mbp], LOW, NOW)).toEqual([])
    const stale = account('codex', [mbp], 2)
    stale.windows[0]!.reading.source = 'harness'
    stale.windows[0]!.reading.read_at = new Date(NOW - 10 * 60_000 - 1).toISOString()
    expect(glanceItems([stale], [mbp], LOW, NOW)).toEqual([])
    const future = account('codex', [mbp], 2)
    future.windows[0]!.reading.source = 'harness'
    future.windows[0]!.reading.read_at = new Date(NOW + 1_000).toISOString()
    expect(glanceItems([future], [mbp], LOW, NOW)).toEqual([])
    const boundary = account('codex', [mbp], 2)
    boundary.windows[0]!.reading.source = 'harness'
    boundary.windows[0]!.reading.read_at = new Date(NOW - 10 * 60_000).toISOString()
    expect(glanceItems([boundary], [mbp], LOW, NOW).map(item => item.name)).toEqual(['Codex is low: 2% left this week'])
  })
})

// Risk: Settings says Ready while the Agents count and resolver wait on a cap.
it('uses the same readiness words and ready count for unknown quota and a measured cap', () => {
 const c = computer('build-7', { claude: 'ready' })
 const a = account('claude', [c])
 const row = door('a-claude', { state: 'unread', routing: { rank: 1, available_slots: 1 } })
 a.rows = [row] as never
 expect(signinStatus(c, 'a-claude', a.rows[0], NOW)).toBe(readiness(a.rows[0]!, c, NOW).text)
 expect(glanceSummary([a], [c], [], NOW).ready).toBe(1)
 Object.assign(row, { routing: { rank: 0, available_slots: 0, wait: { code: 'allowance', until: '2026-10-06T14:00:00Z' } } })
 expect(signinStatus(c, 'a-claude', a.rows[0], NOW)).toBe(readiness(a.rows[0]!, c, NOW).text)
 expect(signinStatus(c, 'a-claude', a.rows[0], NOW)).toMatch(/^At limit until /)
 expect(glanceSummary([a], [c], [], NOW).ready).toBe(0)
})

// AEON-1209 (AEON-1176 P6): the accounts chip in the capacity row and its three tiles. Risk: a
// tile claims all is well while a sign-in or a low quota needs a person, or a tile leads nowhere.
describe('the accounts chip and its tiles', () => {
  const studio = computer('studio', { claude: 'ready', codex: 'ready' })
  const tiles = (accounts: OverviewAccount[], computers: PairingView[], signins = 0, over: Partial<Parameters<typeof glanceTiles>[0]> = {}) => {
    const items = glanceItems(accounts, computers, LOW, NOW)
    return glanceTiles({ accounts, computers, items, summary: glanceSummary(accounts, computers, items, NOW), signins, pacing: '5 days · keep auto · no nights', away: '', showAccounts: true, ...over })
  }
  it('names what there is, and flags sign-ins before anything else', () => {
    expect(glanceChipText(5, 3, true)).toBe('5 accounts · 3 computers')
    expect(glanceChipText(1, 1, true)).toBe('1 account · 1 computer')
    expect(glanceChipText(4, 2, false)).toBe('2 computers')
    expect(glanceChipText(0, 0, true)).toBe('No accounts yet')
    const low = account('codex', [studio], 4)
    const items = glanceItems([low], [studio], LOW, NOW)
    expect(items.map(i => i.kind)).toEqual(['quota'])
    expect(glanceFlag(1, items)).toBe('1 sign-in')
    expect(glanceFlag(0, items)).toBe('1 needs you')
    expect(glanceFlag(0, [])).toBe('')
  })
  it('opens the three parts of Settings, each with its own state words', () => {
    const all = tiles([account('claude', [studio]), account('codex', [studio])], [studio])
    expect(all.map(t => [t.part, t.to])).toEqual([['accounts', '/settings/accounts'], ['computers', '/settings/computers'], ['capacity', '/settings/capacity']])
    expect(all.map(t => t.line)).toEqual(['All 2 ready', '1 of 1 online', '5 days · keep auto · no nights'])
    expect(all.some(t => t.warn)).toBe(false)
  })
  it('puts a low quota on Accounts and a sign-in problem on Computers, both as warnings', () => {
    const broken = computer('build-7', { claude: 'needs_login' })
    const t = tiles([account('codex', [studio], 4), account('claude', [broken])], [studio, broken], 1)
    const [acc, comp] = t
    expect(acc!.warn).toBe(true)
    expect(acc!.line).toContain('1 needs a sign-in')
    expect(acc!.detail).toMatch(/^Codex is low: 4% left/)
    expect(comp!.warn).toBe(true)
    expect(comp!.line).toMatch(/build-7$/)
  })
  it('shows only Computers to someone who may not read accounts, and Away on Capacity', () => {
    expect(tiles([], [studio], 0, { showAccounts: false }).map(t => t.part)).toEqual(['computers'])
    expect(tiles([account('claude', [studio])], [studio], 0, { away: 'until 18:00' })[2]!.line).toBe('Away until 18:00')
  })
})

// AEON-1209 G7 · risk: missing or forbidden evidence is presented as zero/ready.
it('AEON-1209: a multi-source claim requires every read, preserves stale timestamps and hides forbidden values', () => {
  const ready = refreshStatus(initialRefreshStatus(), { ok: true, at: NOW })
  const failure = refreshStatus(initialRefreshStatus(), { ok: false, error: 'accounts unavailable' })
  expect(glanceAvailability([ready, initialRefreshStatus()], true, NOW).state).toBe('loading')
  expect(glanceAvailability([ready, failure], true, NOW)).toEqual({ state: 'error', updatedAt: null, reason: 'accounts unavailable' })
  expect(glanceAvailability([ready], true, NOW).state).toBe('empty')
  const lastGood = refreshStatus(initialRefreshStatus(), { ok: true, at: NOW - 10_000 })
  const stale = refreshStatus(lastGood, { ok: false, error: 'computers unavailable' })
  expect(glanceAvailability([ready, stale], false, NOW)).toEqual({ state: 'stale', updatedAt: NOW - 10_000, reason: 'computers unavailable' })
  expect(glanceAvailability([ready], false, NOW + 45_001).state).toBe('stale')
  const denied = refreshStatus(lastGood, { ok: false, error: 'access denied', forbidden: true })
  expect(glanceAvailability([ready, denied], false, NOW)).toEqual({ state: 'forbidden', updatedAt: null, reason: 'access denied' })
  const stillDenied = refreshStatus(denied, { ok: false, error: 'offline' })
  expect(glanceAvailability([stillDenied], false, NOW).state).toBe('forbidden')
  expect(glanceAvailability([refreshStatus(stillDenied, { ok: true, at: NOW })], false, NOW).state).toBe('ready')
})
