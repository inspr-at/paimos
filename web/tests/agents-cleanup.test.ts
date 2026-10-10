// SPDX-License-Identifier: AGPL-3.0-only
// AEON-402: /agents cleanup — the folded summary, Remove for computers, the
// paused copy that names why and how to resume, and Deny's visible reason.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildPools, buildRows, plainText, poolSentence, setupSummary, type AccountInput } from '../src/lib/capacity.ts'
import { capacityWaitText } from '../src/lib/capacityWait.ts'
import { computerRemoval, denyDisabledReason, type PairingView } from '../src/lib/agentPairing.ts'

test('the folded line counts computers and ready accounts, never filler', () => {
  assert.equal(setupSummary({ computers: 2, ready: 3, total: 3 }), '2 computers · 3 accounts ready')
  assert.equal(setupSummary({ computers: 1, ready: 0, total: 5 }), '1 computer · 0 of 5 accounts ready')
  assert.equal(setupSummary({ computers: 0, ready: 1, total: 1 }), '1 account ready')
  assert.equal(setupSummary({ computers: 0, ready: 0, total: 0 }), '')
})

const computer = (fields: Partial<PairingView>) => ({ computer_id: '11111111-1111-4111-8111-111111111111', computer_state: 'revoked', state: 'revoked', last_seen_at: '2026-09-30T08:00:00Z', enrollments: [], ...fields }) as Pick<PairingView, 'computer_id' | 'computer_state' | 'state' | 'last_seen_at' | 'enrollments'>
const enrollment = (runs: string[]) => ({ account_id: 'a', account_key: 'k', harness: 'claude', label: 'Claude', state: 'revoked', active_run_ids: runs }) as unknown as PairingView['enrollments'][number]
test('a revoked or never-confirmed computer can be removed by a person who manages accounts', () => {
  const manage = { canDisconnect: true }
  assert.deepEqual(computerRemoval(computer({}), manage), { allowed: true, reason: '' })
  assert.deepEqual(computerRemoval(computer({}), { canDisconnect: false }), { allowed: false, reason: '' })
  assert.deepEqual(computerRemoval(computer({ computer_state: 'connected', state: 'approved', last_seen_at: null }), manage), { allowed: true, reason: '' })
  assert.equal(computerRemoval(computer({ computer_state: 'connected', state: 'redeemed' }), manage).allowed, false)
  assert.equal(computerRemoval(computer({ computer_state: 'connected', state: 'approved' }), manage).allowed, false, 'a computer that reported is disconnected first')
  assert.deepEqual(computerRemoval(computer({ enrollments: [enrollment(['r1'])] }), manage), { allowed: false, reason: 'A run on this computer is not settled yet.' })
})

test('a paused account says why and how to resume', () => {
  const text = capacityWaitText({ code: 'state', run_now_allowed: false })
  assert.match(text, /Settings \/ Accounts/)
  assert.match(text, /resume/)
  assert.doesNotMatch(text, /Agents are paused for this account/)
  const accounts: AccountInput[] = [{ id: 'c1', label: 'admin@example.test', harness: 'claude', host: 'build-7', state: 'draining', last_probe_ok: true }]
  const paused = buildPools(buildRows(accounts, []), Date.now())
  assert.equal(plainText(poolSentence(paused[0], Date.now())), 'Paused in Settings / Accounts. Turn “Agents may use it” back on there to resume.')
  const draining = buildPools(buildRows([{ ...accounts[0], disconnecting: true }], [{ account_id: 'c1', schedule: null as never, windows: [], routing: { rank: 0, available_slots: 0, wait: { code: 'state', run_now_allowed: false } } }]), Date.now())
  assert.equal(plainText(poolSentence(draining[0], Date.now())), 'Claude: Disconnecting from build-7; agents start nothing new on it.')
})

test('Deny is never disabled without a reason', () => {
  assert.equal(denyDisabledReason({ busy: '', canDeny: true }), null)
  assert.equal(denyDisabledReason({ busy: '', canDeny: false }), 'Only a signed-in person who can manage accounts can connect or deny this computer.')
  assert.equal(denyDisabledReason({ busy: 'deny', canDeny: true }), 'Denying this request…')
  assert.equal(denyDisabledReason({ busy: 'approve', canDeny: true }), 'Connecting this computer…')
  assert.equal(denyDisabledReason({ busy: 'lookup', canDeny: true }), 'Wait for the current action to finish.')
})
