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
import { glanceItems, glanceSummary, onlineComputers, signinStatus } from '../src/lib/accountsGlance'

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
