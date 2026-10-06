// SPDX-License-Identifier: AGPL-3.0-only
// AEON-782: the Agents page's status line for Accounts and computers. The words
// are the design's ("All 3 ready · 2 computers online", "2 of 3 ready · Claude
// needs verifying on mbp2607 · +1") and the count is honest: an account is ready
// only with a Ready sign-in and nothing to verify, an old report on an offline
// computer is not a fault, and a transient hold is not a request for action.
import { describe, expect, it } from 'vitest'
import type { PairingView } from '../src/lib/agentPairing'
import type { OverviewAccount } from '../src/lib/accountsOverview'
import { glanceItems, glanceSummary } from '../src/lib/accountsGlance'

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
  const mbp = computer('mbp2607', { claude: 'ready', codex: 'ready' }), studio = computer('mbp2606', { codex: 'ready', cursor: 'ready' })
  const calm = [account('claude', [mbp, studio]), account('codex', [mbp, studio], 60), account('cursor', [mbp, studio])]

  it('says all ready with the computers online when nothing blocks agents', () => {
    const items = glanceItems(calm, [mbp, studio], LOW, NOW)
    expect(items).toEqual([])
    expect(glanceSummary(calm, [mbp, studio], items)).toMatchObject({ tone: 'ok', text: 'All 3 ready · 2 computers online' })
  })

  it('names the first thing that blocks agents and counts the rest, so the line is the whole answer', () => {
    const expired = computer('mbp2607', { claude: 'ready', codex: 'ready' })
    Object.assign(expired.enrollments[0]!, { verification_state: 'expired', verification_expired_ready: false })
    const accounts = [account('claude', [expired, studio]), account('codex', [expired, studio], 9), account('cursor', [expired, studio])]
    const items = glanceItems(accounts, [expired, studio], LOW, NOW)
    expect(items.map(i => i.name)).toEqual(['Claude needs verifying on mbp2607', 'Codex is low: 9% left this week'])
    expect(glanceSummary(accounts, [expired, studio], items)).toMatchObject({ tone: 'warn', text: '2 of 3 ready · Claude needs verifying on mbp2607 · +1' })
  })

  it('does not call a computer that stopped reporting broken, and does not ask for action while a repin settles', () => {
    const offline = computer('mbp2607', { claude: 'blocked' }, { connectivity: 'offline', harness_details: { claude: { state: 'blocked', reason: 'dependency_invalid' } } } as never)
    const settling = computer('mbp2606', { codex: 'blocked' }, { harness_details: { codex: { state: 'blocked', reason: 'repin_pending' } } } as never)
    const accounts = [account('claude', [offline]), account('codex', [settling])]
    expect(glanceItems(accounts, [offline, settling], LOW, NOW)).toEqual([])
    expect(glanceSummary(accounts, [offline, settling], [])).toMatchObject({ tone: 'warn', text: '0 of 2 ready · 2 not ready' })
  })
})
