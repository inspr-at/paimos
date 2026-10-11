// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1174 (design AEON-1149, package P4): Settings › Computers is one table.
// Risks: the New starts chip writes defaults or drops a policy field the person
// set in the panel (it must send the full policy with only the mode changed, at
// the reviewed revision); a cell says Ready for a sign-in that cannot start
// agents, or hides an expired verification behind "Offline"; the chip's choices
// promise waits the computer's options have switched off.
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PairingView } from '../src/lib/agentPairing'
import { computerLine, shortIdentity, signinCell } from '../src/lib/accountsGlance'
import { HostCapacityWriteError, mayChangeNewStarts, newStartsChoices, newStartsLabel, newStartsWrite, samePolicy, saveHostPolicy, type HostCapacityPolicy } from '../src/lib/hostCapacity'

const NOW = Date.parse('2026-10-11T12:00:00Z')
// Every field differs from the defaults, so a write that fell back to them shows.
const POLICY: HostCapacityPolicy = { mode: 'smart', maximum_agents: 12, maximum_load: 17.5, wait_when_busy: false, ease_on_battery: false, ease_when_hot: true, consider_activity: true }
const host = (policy: HostCapacityPolicy = POLICY, over: Record<string, unknown> = {}) => ({ policy, signals: { load: 3, cores: 14, memory_pressure: 'normal', power: 'plugged_in', thermal: 'normal' }, reported_at: new Date(NOW).toISOString(), running: 4, queued: 0, reason: '', load_limit: 14, history: [], ...over })
function computer(over: Record<string, unknown> = {}, enrollment: Record<string, unknown> = {}): PairingView {
  return {
    computer_id: 'c-1', computer_name: 'build-7', computer_state: 'connected', connectivity: 'online', setup_state: 'connected', revision: 4, local_cleanup: 'confirmed',
    last_seen_at: new Date(NOW - 3 * 3_600_000).toISOString(), harness_statuses: { claude: 'ready' }, harness_details: { claude: { state: 'ready' } },
    enrollments: [{ account_id: 'a-1', harness: 'claude', label: 'markus@example.com', state: 'connected', verification_state: 'completed', can_verify: true, ...enrollment }],
    host_capacity: host(),
    ...over,
  } as unknown as PairingView
}
const signin = (c: PairingView) => ({ computer: c, enrollment: c.enrollments[0]! })

describe('the New starts chip writes only the mode', () => {
  it('sends the full policy as last reported, with only the mode changed, at the reviewed revision', () => {
    for (const mode of ['fixed', 'off'] as const) {
      const write = newStartsWrite(computer(), mode)
      expect(write).toEqual({ expected_revision: 4, policy: { ...POLICY, mode } })
      // Nothing the person set in the panel is lost or reset to a default.
      const { mode: _changed, ...kept } = write!.policy
      const { mode: _was, ...before } = POLICY
      expect(kept).toEqual(before)
    }
  })
  it('writes nothing for the mode already set, without a reported policy, or without a revision', () => {
    expect(newStartsWrite(computer(), 'smart')).toBeNull()
    expect(newStartsWrite(computer({ host_capacity: undefined }), 'off')).toBeNull()
    expect(newStartsWrite(computer({ revision: undefined }), 'off')).toBeNull()
  })
  it('uses the same PUT as the panel and reports a conflict or an unconfirmed answer honestly', async () => {
    const calls: { url: string; init: RequestInit }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => { calls.push({ url, init }); return new Response(JSON.stringify(host({ ...POLICY, mode: 'off' })), { status: 200 }) }))
    await expect(saveHostPolicy('c-1', newStartsWrite(computer(), 'off')!)).resolves.toEqual({ ...POLICY, mode: 'off' })
    expect(calls[0]!.url).toBe('/api/agent-pairing/computers/c-1/capacity')
    expect(calls[0]!.init.method).toBe('PUT')
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({ expected_revision: 4, policy: { ...POLICY, mode: 'off' } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'conflict' }), { status: 409 })))
    await expect(saveHostPolicy('c-1', newStartsWrite(computer(), 'off')!)).rejects.toMatchObject({ status: 409 })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ policy: { mode: 'sideways' } }), { status: 200 })))
    const unconfirmed = await saveHostPolicy('c-1', newStartsWrite(computer(), 'off')!).catch(error => error)
    expect(unconfirmed).toBeInstanceOf(HostCapacityWriteError)
    expect(unconfirmed.status).toBe(0)
  })
  afterEach(() => vi.unstubAllGlobals())
  it('names only the waits this computer has switched on', () => {
    const [smart, fixed, off] = newStartsChoices(POLICY, 14)
    expect(smart!.detail).toBe('Starts new agents whenever the computer has room; the limit comes from its 14 cores. Waits while it is hot.')
    expect(newStartsChoices({ ...POLICY, wait_when_busy: true, ease_on_battery: true })[0]!.detail).toBe('Starts new agents whenever the computer has room. Waits while it is busy, hot or on battery.')
    expect(newStartsChoices({ ...POLICY, ease_when_hot: false })[0]!.detail).toBe('Starts new agents whenever the computer has room.')
    expect(fixed!.detail).toBe('New starts wait while the one-minute load is above 17.5.')
    expect(off!.detail).toBe('Starts without looking at load. Maximum agents still applies.')
    expect(newStartsChoices({ ...POLICY, maximum_agents: 0 })[2]!.detail).toBe('Starts without looking at load.')
    expect([newStartsLabel(POLICY), newStartsLabel(POLICY, 'fixed'), newStartsLabel(POLICY, 'off')]).toEqual(['Smart', 'Fixed · load 17.5', 'Off'])
  })
  it('Undo compares the stored policy field by field, whatever the key order', () => {
    const reordered = Object.fromEntries(Object.entries(POLICY).reverse()) as unknown as HostCapacityPolicy
    expect(samePolicy(POLICY, reordered)).toBe(true)
    expect(samePolicy(POLICY, { ...POLICY, maximum_agents: 9 })).toBe(false)
    expect(samePolicy(POLICY, undefined)).toBe(false)
  })
  it('is a control only for the owner with management on a connected computer', () => {
    expect(mayChangeNewStarts(computer(), true)).toBe(true)
    expect(mayChangeNewStarts(computer(), false)).toBe(false)
    expect(mayChangeNewStarts(computer({}, { can_verify: false }), true)).toBe(false)
    expect(mayChangeNewStarts(computer({ computer_state: 'draining' }), true)).toBe(false)
  })
})

describe('a cell says the sign-in in one word', () => {
  it('Ready only for a reported, connected, verified sign-in', () => {
    expect(signinCell(signin(computer()), undefined, NOW)).toMatchObject({ word: 'Ready', tone: 'ok' })
  })
  it('Expired stays Expired on an offline computer: it still needs verifying', () => {
    const expired = { verification_state: 'expired', verification_expired_ready: false }
    expect(signinCell(signin(computer({}, expired)), undefined, NOW)).toMatchObject({ word: 'Expired', tone: 'warn' })
    expect(signinCell(signin(computer({ connectivity: 'offline' }, expired)), undefined, NOW).word).toBe('Expired')
  })
  it('Offline, Draining and Blocked say why in the tip', () => {
    expect(signinCell(signin(computer({ connectivity: 'offline' })), undefined, NOW)).toEqual({ word: 'Offline', tone: 'off', tip: 'build-7 last reported 3 h ago. New agents wait until it reports again.' })
    expect(signinCell(signin(computer({ computer_state: 'draining' })), undefined, NOW).word).toBe('Draining')
    expect(signinCell(signin(computer({}, { state: 'draining' })), undefined, NOW).word).toBe('Draining')
    expect(signinCell(signin(computer({ computer_state: 'revoked' })), undefined, NOW)).toMatchObject({ word: 'Blocked', tone: 'blocked' })
    expect(signinCell(signin(computer({}, { state: 'revoked' })), undefined, NOW)).toMatchObject({ word: 'Blocked', tone: 'blocked' })
    // A sign-in that cannot start agents until someone acts.
    const signedOut = computer({ harness_statuses: { claude: 'login_required' }, harness_details: { claude: { state: 'login_required', reason: 'login_required' } } })
    expect(signinCell(signin(signedOut), undefined, NOW)).toMatchObject({ word: 'Blocked', tone: 'warn' })
    expect(signinCell(signin(signedOut), undefined, NOW).tip).toMatch(/New agents wait until someone acts\.$/)
  })
  it('a check under way or a missing report is never called Ready', () => {
    expect(signinCell(signin(computer({}, { verification_state: 'queued' })), undefined, NOW)).toMatchObject({ word: 'Checking', tone: 'wait' })
    expect(signinCell(signin(computer({ harness_statuses: {}, harness_details: {} })), undefined, NOW)).toMatchObject({ word: 'Not reported' })
  })
})

describe('the computer cell and the column heads', () => {
  it('says agents running, why new starts wait, or when it was last seen', () => {
    expect(computerLine(computer(), NOW)).toEqual({ text: '4 of 12 agents running', tone: 'ok' })
    expect(computerLine(computer({ host_capacity: host({ ...POLICY, maximum_agents: 0 }, { running: 1 }) }), NOW).text).toBe('1 agent running')
    expect(computerLine(computer({ host_capacity: host({ ...POLICY, maximum_agents: 0 }, { running: 0 }) }), NOW).text).toBe('No agents running')
    expect(computerLine(computer({ host_capacity: host(POLICY, { reason: 'host_load' }) }), NOW)).toEqual({ text: '4 of 12 agents running · waiting: computer busy', tone: 'wait' })
    expect(computerLine(computer({ connectivity: 'offline' }), NOW)).toEqual({ text: 'Offline · seen 3 h ago', tone: 'off' })
    expect(computerLine(computer({ computer_state: 'revoked', local_cleanup: 'pending' }), NOW)).toEqual({ text: 'Removed · cleanup pending', tone: 'blocked' })
    expect(computerLine(computer({ computer_state: 'draining' }), NOW).text).toBe('Draining · running agents finish')
  })
  it('shortens an address to its name; a plain name stays whole', () => {
    expect(shortIdentity('markus.barta@example.com')).toBe('markus.barta')
    expect(shortIdentity('Main')).toBe('Main')
    expect(shortIdentity('@handle')).toBe('@handle')
  })
})
