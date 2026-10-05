// SPDX-License-Identifier: AGPL-3.0-only
// AEON-499: the computer-first Accounts and computers mapping. The three
// approved states (offline, ready without readings, ready with readings), plus
// the rules that keep the view honest: one readiness per account, no "ready"
// on an offline computer, the legend only where a bar is drawn.
import { describe, expect, it } from 'vitest'
import type { PairingView } from '../src/lib/agentPairing'
import { buildRows, defaultSchedule, type AccountCapacity, type AccountInput, type CapacityWindow } from '../src/lib/capacity'
import { buildComputerCards, computerCaption, middleEllipsis, pacingSummary, readySummary } from '../src/lib/computerAccounts'

const NOW = Date.parse('2026-10-01T12:30:00Z')
const CODEX = 'ac000000-0000-4000-8000-000000000001'
const CURSOR = 'ac000000-0000-4000-8000-000000000002'
const COMPUTER = 'c0000000-0000-4000-8000-000000000001'

function computer(over: Partial<PairingView> = {}): PairingView {
  return {
    request_id: 'r1', tenant_id: 't1', tenant_name: 'Barta', state: 'redeemed', request_digest: 'd', expires_at: '2026-10-02T00:00:00Z',
    computer_name: 'mbp2607', platform: 'darwin', arch: 'arm64', workspace_path: '/Users/markus/Code', capabilities: [], requested_accounts: [], verification: null,
    computer_id: COMPUTER, computer_state: 'connected', principal_id: 'p1', daemon_id: 'd1', runtime_prefix: null, local_cleanup: 'pending', local_processes: 'unconfirmed',
    enrollments: [
      { account_id: CODEX, account_key: 'k1', harness: 'codex', label: 'admin@augmentoring.com', model_profile_id: '', state: 'connected', local_cleanup: 'pending', verification_run_id: null, active_run_ids: [] },
      { account_id: CURSOR, account_key: 'k2', harness: 'cursor', label: 'markus@barta.com', model_profile_id: '', state: 'connected', local_cleanup: 'pending', verification_run_id: null, active_run_ids: [] },
    ],
    setup_state: 'connected', connectivity: 'online', last_seen_at: new Date(NOW - 20_000).toISOString(),
    harness_statuses: { codex: 'ready', cursor: 'ready' },
    ...over,
  }
}
function inputs(connectivity: 'online' | 'offline'): AccountInput[] {
  return [
    { id: CODEX, label: 'admin@augmentoring.com', harness: 'codex', host: 'mbp2607', state: 'available', last_probe_ok: true, connectivity },
    { id: CURSOR, label: 'markus@barta.com', harness: 'cursor', host: 'mbp2607', state: 'available', last_probe_ok: true, connectivity },
  ]
}
function weekly(left: number): CapacityWindow {
  return {
    reading: { window_kind: 'weekly', window_minutes: 10080, used_percent: 100 - left, resets_at: '2026-10-01T16:00:00Z', source: 'harness', read_at: new Date(NOW - 60_000).toISOString() },
    starts_at: '2026-09-24T16:00:00Z', allowance: 100, remaining_percent: left, freshness: 'fresh',
    pacing: { usable_hours: 40, percent_per_hour: 1.5, suggested_today_percent: 10, budget_percent: 14, used_today_percent: 0, period_start: '2026-10-01T06:00:00Z', period_end: '2026-10-01T20:00:00Z' },
  }
}
function five(left: number): CapacityWindow {
  return { ...weekly(left), reading: { ...weekly(left).reading, window_kind: '5h', window_minutes: 300 } }
}
function capacity(windows: CapacityWindow[] = []): AccountCapacity[] {
  const schedule = defaultSchedule('Europe/Vienna')
  return [{ account_id: CODEX, schedule, windows }, { account_id: CURSOR, schedule, windows: [], learning: { windows: [], tokens: 0, cost_micros: 0, runs: 0, limit_hits: 0 } }]
}

describe('offline computer', () => {
  const view = computer({ connectivity: 'offline', last_seen_at: '2026-10-01T10:25:00Z' })
  const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('offline'), capacity()), now: NOW })

  it('says offline once, with the reason and the one fix', () => {
    expect(cards).toHaveLength(1)
    expect(cards[0].status?.tone).toBe('warn')
    expect(cards[0].status?.text).toMatch(/^Offline since \d\d:\d\d$/)
    expect(cards[0].notice?.title).toMatch(/^The agent on this Mac stopped reporting at \d\d:\d\d\.$/)
    expect(cards[0].notice?.command).toBe('aeon-agentd status')
  })
  it('never calls an account on it ready, and shows no bar or legend', () => {
    for (const line of cards[0].accounts) {
      expect(line.readiness.kind).toBe('offline')
      expect(line.readiness.text).toBe('Paused · computer offline')
      expect(line.capacity.kind).not.toBe('bar')
    }
    expect(cards[0].accounts[0].capacity.kind).toBe('offline')
    expect(cards[0].legend).toBe(false)
  })
  it('a stale harness report on an offline computer does not leak through as ready', () => {
    const stale = computer({ connectivity: 'offline', harness_statuses: { codex: 'ready', cursor: 'ready' } })
    const [card] = buildComputerCards({ computers: [stale], rows: buildRows(inputs('offline'), capacity()), now: NOW })
    expect(card.accounts.map(a => a.readiness.text)).toEqual(['Paused · computer offline', 'Paused · computer offline'])
  })
  it('the header pill names the computer', () => {
    expect(readySummary(cards)).toEqual({ text: '0 of 2 ready · mbp2607 offline', tone: 'warn' })
  })
})

describe('online, no readings yet', () => {
  const cards = buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), capacity()), now: NOW })

  it('is online, seen just now, with the full identities', () => {
    expect(cards[0].status).toEqual({ text: 'Online · seen just now', tone: 'ok', live: true })
    expect(cards[0].notice).toBeNull()
    expect(cards[0].accounts.map(a => [a.vendor, a.identity])).toEqual([['Codex', 'admin@augmentoring.com'], ['Cursor', 'markus@barta.com']])
  })
  it('each account is ready, and capacity says honestly that nothing was read', () => {
    expect(cards[0].accounts.map(a => a.readiness.kind)).toEqual(['ready', 'ready'])
    expect(cards[0].accounts[0].capacity).toEqual({ kind: 'none' })
    // Cursor has no quota reader, independently of whether learning has begun.
    expect(cards[0].accounts[1].capacity).toEqual({ kind: 'quiet', text: "Cursor doesn't show its limit · one run at a time by day" })
    expect(cards[0].legend).toBe(false)
    expect(readySummary(cards)).toEqual({ text: '2 of 2 ready', tone: 'ok' })
  })
})

describe('online with readings', () => {
  const cards = buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), capacity([weekly(62), five(80)])), now: NOW })

  it('draws the bar with % left, the reset and the 5-hour window, and the legend', () => {
    const cell = cards[0].accounts[0].capacity
    expect(cell.kind).toBe('bar')
    if (cell.kind !== 'bar') return
    expect(cell.left).toBe(62)
    expect(cell.window).toBe('this week')
    expect(cell.five).toBe(80)
    expect(cell.dim).toBe(false)
    expect(cards[0].legend).toBe(true)
  })
  it('a window used up is "at limit until" its reset, not ready', () => {
    const [card] = buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), capacity([weekly(0)])), now: NOW })
    expect(card.accounts[0].readiness.kind).toBe('limit')
    expect(card.accounts[0].readiness.text).toMatch(/^At limit until /)
    expect(readySummary([card])?.text).toBe('1 of 2 ready')
  })
  it('the server routing wait decides when it gives one', () => {
    const cap = capacity([weekly(40)])
    cap[0].routing = { rank: 0, available_slots: 0, wait: { code: 'vendor', until: '2026-10-01T18:00:00Z', run_now_allowed: false } }
    const [card] = buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), cap), now: NOW })
    expect(card.accounts[0].readiness).toMatchObject({ kind: 'limit', tone: 'warn' })
  })
})

describe('waits from the server routing', () => {
  // AEON-499 review: a capacity or reading wait (rank 0, no slot, run_now_allowed false) showed "Ready".
  const waiting = (code: 'capacity' | 'reading' | 'residency') => {
    const cap = capacity([weekly(40)])
    cap[0].routing = { rank: 0, available_slots: 0, wait: { code, run_now_allowed: false } }
    return buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), cap), now: NOW })
  }
  it('a run still working makes the account busy, not ready', () => {
    const cards = waiting('capacity')
    expect(cards[0].accounts[0].readiness).toMatchObject({ kind: 'busy', text: 'Busy · run in progress', tone: 'mute', tip: 'Waiting for the current run to finish' })
    expect(readySummary(cards)).toEqual({ text: '1 of 2 ready', tone: 'mute' })
  })
  it('a reading on its way makes the account wait, not ready', () => {
    const cards = waiting('reading')
    expect(cards[0].accounts[0].readiness).toMatchObject({ kind: 'waiting', text: 'Waiting for a reading', tone: 'mute' })
    expect(readySummary(cards)).toEqual({ text: '1 of 2 ready', tone: 'mute' })
  })
  it('a residency wait explains allowed providers and keeps the account out of the ready count', () => {
    const cards = waiting('residency')
    expect(cards[0].accounts[0].readiness).toMatchObject({ kind: 'attention', text: 'Outside allowed providers', tone: 'warn', tip: 'Waiting for an account within the allowed providers' })
    expect(readySummary(cards)).toEqual({ text: '1 of 2 ready', tone: 'warn' })
    expect(cards[0].accounts[0].capacity).toMatchObject({ kind: 'bar', dim: true })
  })
  it('the only account busy is "0 of 1 ready", and a warning elsewhere still warns', () => {
    const cap = capacity([weekly(40)]).slice(0, 1)
    cap[0].routing = { rank: 0, available_slots: 0, wait: { code: 'capacity', run_now_allowed: false } }
    const view = computer({ enrollments: computer().enrollments.slice(0, 1) })
    const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('online').slice(0, 1), cap), now: NOW })
    expect(readySummary(cards)).toEqual({ text: '0 of 1 ready', tone: 'mute' })
    const signin = computer({ harness_statuses: { codex: 'ready', cursor: 'login_required' }, harness_details: { cursor: { state: 'login_required', reason: 'login_required' } } })
    const mixed = capacity([weekly(40)])
    mixed[0].routing = { rank: 0, available_slots: 0, wait: { code: 'reading', run_now_allowed: false } }
    expect(readySummary(buildComputerCards({ computers: [signin], rows: buildRows(inputs('online'), mixed), now: NOW }))).toEqual({ text: '0 of 2 ready', tone: 'warn' })
  })
  it('a ranked account with a free slot stays ready', () => {
    const cap = capacity([weekly(40)])
    cap[0].routing = { rank: 1, available_slots: 2 }
    const [card] = buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), cap), now: NOW })
    expect(card.accounts[0].readiness.kind).toBe('ready')
  })
})

describe('one state per account', () => {
  it('a confirmed sign-out asks for the vendor sign-in command', () => {
    const cap = capacity()
    cap[0].probe_failure = 'auth_failed'
    const rows = buildRows(inputs('online').map(a => (a.id === CODEX ? { ...a, last_probe_ok: false } : a)), cap)
    const [card] = buildComputerCards({ computers: [computer()], rows, now: NOW })
    expect(card.accounts[0].readiness).toMatchObject({ kind: 'signin', text: 'Signed out', command: 'codex login' })
  })
  it('a harness that needs attention on its computer carries its fix', () => {
    const view = computer({ harness_statuses: { codex: 'login_required', cursor: 'ready' }, harness_details: { codex: { state: 'login_required', reason: 'login_required' } } })
    const [card] = buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), capacity()), now: NOW })
    expect(card.accounts[0].readiness).toMatchObject({ kind: 'signin', command: 'codex login' })
    expect(card.accounts[1].readiness.kind).toBe('ready')
  })
  it('a pool on hold says so on its accounts, Sprint is a note on the bar', () => {
    const cap = capacity([weekly(40)])
    cap[0].schedule = { ...cap[0].schedule, override: 'hold', override_until: '2026-10-01T14:30:00Z' }
    const [held] = buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), cap), now: NOW })
    expect(held.accounts[0].readiness).toMatchObject({ kind: 'hold', tone: 'mute' })
    expect(held.accounts[0].readiness.text).toMatch(/^On hold until \d\d:\d\d$/)
    cap[0].schedule = { ...cap[0].schedule, override: 'sprint', override_until: '2026-10-01T16:00:00Z' }
    const [sprint] = buildComputerCards({ computers: [computer()], rows: buildRows(inputs('online'), cap), now: NOW })
    expect(sprint.accounts[0].readiness.kind).toBe('ready')
    const cell = sprint.accounts[0].capacity
    expect(cell.kind === 'bar' && cell.note).toMatch(/^Sprint: all of it until \d\d:\d\d$/)
  })
  it('an online computer with a computer-wide login flag stays online; its accounts are judged on their own', () => {
    const [card] = buildComputerCards({ computers: [computer({ setup_state: 'login_required' })], rows: buildRows(inputs('online'), capacity()), now: NOW })
    expect(card.status?.text).toBe('Online · seen just now')
    expect(card.accounts.map(a => a.readiness.kind)).toEqual(['ready', 'ready'])
  })
  it('a disconnecting computer pauses its accounts', () => {
    const [card] = buildComputerCards({ computers: [computer({ computer_state: 'draining' })], rows: buildRows(inputs('online'), capacity()), now: NOW })
    expect(card.status?.text).toBe('Disconnecting')
    expect(card.accounts.every(a => a.readiness.kind === 'paused')).toBe(true)
  })
  it('revoked computers stay out of the cards; accounts without a listed computer group by host', () => {
    const rows = buildRows([...inputs('online'), { id: 'ac000000-0000-4000-8000-000000000009', label: 'ops@barta.com', harness: 'claude', host: 'studio', state: 'available', last_probe_ok: true }], capacity())
    const cards = buildComputerCards({ computers: [computer(), computer({ computer_id: 'c2', computer_name: 'old', computer_state: 'revoked', enrollments: [] })], rows, now: NOW })
    expect(cards.map(c => c.name)).toEqual(['mbp2607', 'studio'])
    expect(cards[1].computer).toBeNull()
  })
})

describe('words', () => {
  it('caption, pacing summary and middle ellipsis', () => {
    expect(computerCaption({ platform: 'darwin', arch: 'arm64', workspace_path: '/Users/markus/Code' })).toBe('macOS · Apple silicon · ~/Code')
    expect(computerCaption({ platform: 'linux', arch: 'amd64', workspace_path: '/srv/work' })).toBe('Linux · amd64 · /srv/work')
    const s = { ...defaultSchedule('Europe/Vienna'), week: defaultSchedule('UTC').week.map(d => ({ ...d, on: true })), reserve: 'fixed' as const, reserve_percent: 10, nights: true }
    expect(pacingSummary(s)).toBe('7 days · keep 10% · nights 22–08')
    expect(pacingSummary(defaultSchedule('UTC'))).toBe('5 days · keep auto · no nights')
    expect(middleEllipsis('admin@augmentoring.com', 40)).toBe('admin@augmentoring.com')
    expect(middleEllipsis('admin@augmentoring.com', 15)).toBe('admin@a…ing.com')
  })
})


describe('AEON-623 account diagnostics', () => {
  it('keeps a blocked Claude account without a capacity projection, with its exact cause and repair', () => {
    const view = computer({
      harness_statuses: { claude: 'blocked' },
      harness_details: { claude: { state: 'blocked', reason: 'probe_failed', reason_detail: 'the default Claude profile is not private (requires mode 0700)' } },
    })
    view.enrollments = [{ ...view.enrollments[0], harness: 'claude', verification_state: 'queued' }]
    const rows = buildRows([{ ...inputs('online')[0], harness: 'claude', last_probe_ok: false }], [])
    const line = buildComputerCards({ computers: [view], rows, now: NOW })[0].accounts[0]
    expect(line.id).toBe(CODEX)
    expect(line.readiness.kind).toBe('attention')
    expect(line.readiness.hint).toBe('Claude: sign-in check failed: the default Claude profile is not private (requires mode 0700).')
    expect(line.readiness.command).toBe('chmod 700 "$HOME/.claude"')
    expect(line.capacity.kind).toBe('none')
  })

  it('does not call an unverified account ready even when the harness is ready', () => {
    const view = computer()
    view.enrollments[0].verification_state = 'queued'
    const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), []), now: NOW })
    expect(cards[0].accounts[0].readiness.text).toBe('Verification queued')
    expect(cards[0].accounts[0].readiness.kind).toBe('waiting')
    expect(cards[0].accounts[1].readiness.kind).toBe('ready')
  })
})

it('AEON-623: revoked bindings cannot reappear as live loose accounts; active and unrelated accounts stay', () => {
  const revoked = computer({ computer_state: 'revoked' })
  const unrelated = { ...inputs('online')[0], id: 'unpaired', label: 'Unpaired account' }
  const rows = buildRows([...inputs('online'), unrelated], capacity([weekly(42)]))
  const hidden = buildComputerCards({ computers: [revoked], rows, now: NOW }).flatMap(c => c.accounts)
  expect(hidden.map(a => a.id)).toEqual(['unpaired'])
  const active = computer({ computer_id: 'active-computer' })
  active.enrollments = [active.enrollments[0]]
  const kept = buildComputerCards({ computers: [revoked, active], rows, now: NOW }).flatMap(c => c.accounts)
  expect(kept.map(a => a.id).sort()).toEqual([CODEX, 'unpaired'].sort())
  expect(kept.find(a => a.id === CODEX)?.capacity.kind).toBe('bar')
})


it('AEON-623: account cards keep different blocked causes and never borrow an unknown account repair', () => {
  const permission = 'the default Claude profile is not private (requires mode 0700)'
  const signedOut = "the approved account is signed out in the daemon's view"
  const view = computer({ harness_statuses: { claude: 'blocked' }, harness_details: { claude: {
    state: 'blocked', reason: 'probe_failed', reason_detail: permission, attention_count: 2,
    attention_accounts: [{ account_id: CODEX, reason: 'probe_failed', reason_detail: permission }, { account_id: CURSOR, reason: 'login_required', reason_detail: signedOut }],
  } } })
  view.enrollments = view.enrollments.map(e => ({ ...e, harness: 'claude' }))
  const cards = () => buildComputerCards({ computers: [view], rows: [], now: NOW })[0].accounts
  expect(cards().find(a => a.id === CODEX)?.readiness.command).toBe('chmod 700 "$HOME/.claude"')
  const login = cards().find(a => a.id === CURSOR)!
  expect(login.readiness.kind).toBe('signin')
  expect(login.readiness.text).toBe('Signed out')
  expect(login.readiness.hint).toBe(`Claude: ${signedOut}.`)
  expect(login.readiness.command).toBe('claude auth login')
  delete view.harness_details!.claude.attention_accounts
  for (const line of cards()) {
    expect(line.readiness.command).toBeUndefined()
    expect(line.readiness.hint ?? '').not.toContain(permission)
  }
})

describe('expired account verification', () => {
  it('keeps a ready account ready without claiming that the expired check succeeded', () => {
    const view = computer()
    view.enrollments[0].verification_state = 'expired'
    view.enrollments[0].verification_expired_ready = true
    const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), capacity()), now: NOW })
    expect(cards[0].accounts[0].readiness).toMatchObject({ kind: 'ready', text: 'Ready', tip: 'Verification expired; the live account probe is ready.' })
    expect(readySummary(cards)?.text).toBe('2 of 2 ready')
    expect(view.enrollments[0].verification_state).toBe('expired')
  })
  it('uses the server’s own probe rather than a ready sibling', () => {
    const view = computer()
    view.enrollments[0].verification_state = 'expired'
    view.enrollments[0].verification_expired_ready = false
    const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), capacity()), now: NOW })
    expect(cards[0].accounts[0].readiness.kind).not.toBe('ready')
  })
  it('never borrows readiness from a sibling or stale report', () => {
    for (const over of [{ connectivity: 'offline' as const }, { harness_statuses: { codex: 'blocked' as const, cursor: 'ready' as const } }, { harness_details: { codex: { state: 'ready' as const, attention_accounts: [{ account_id: CODEX, reason: 'authentication_failed' }], attention_count: 1 } } }]) {
      const view = computer(over)
      view.enrollments[0].verification_state = 'expired'
      const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), capacity()), now: NOW })
      expect(cards[0].accounts[0].readiness.kind).not.toBe('ready')
    }
  })
})


describe('verification reporting failures', () => {
  it('shows a stalled check as a problem without changing its active ownership', () => {
    const view = computer()
    Object.assign(view.enrollments[0], { verification_state: 'starting', verification_stalled: true, verification_error: 'verification_timeout', active_run_ids: ['run'] })
    const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), capacity()), now: NOW })
    expect(cards[0].accounts[0].readiness).toMatchObject({ kind: 'attention', text: 'Verification stalled' })
    expect(cards[0].accounts[0].readiness.tip).toContain('helper must confirm that it stopped')
    expect(view.enrollments[0].verification_state).toBe('starting')
    expect(view.enrollments[0].active_run_ids).toEqual(['run'])
    expect(cards[0].accounts[1].readiness.kind).toBe('ready')
  })
  it('explains a rejected report without blaming the vendor protocol or exposing raw errors', () => {
    const view = computer()
    Object.assign(view.enrollments[0], { verification_state: 'failed', verification_error: 'reporter_unavailable' })
    const cards = buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), capacity()), now: NOW })
    expect(cards[0].accounts[0].readiness).toMatchObject({ kind: 'attention', text: 'Verification failed' })
    expect(cards[0].accounts[0].readiness.tip).toContain('Update the helper, then use Verify again')
    expect(cards[0].accounts[0].readiness.tip).toContain('Usage may be incomplete')
    view.enrollments[0].verification_error = 'raw-private-error'
    expect(buildComputerCards({ computers: [view], rows: buildRows(inputs('online'), capacity()), now: NOW })[0].accounts[0].readiness.tip).not.toContain('raw-private-error')
  })
})
