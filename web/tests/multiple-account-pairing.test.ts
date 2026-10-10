// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { defaultSelectedAccountKeys, planApproval, pairingPermissions, type PairingView } from '../src/lib/agentPairing.ts'

test('pairing approves every distinct isolated home and rejects a duplicate or default home', () => {
  const accounts = ['claude', 'claude', 'codex', 'codex', 'codex'].map((harness, i) => ({
    harness, label: `${harness} ${i}`, account_key: `${harness}-${i}`, config_home_id: String(i + 1).repeat(64),
  }))
  const view = { state: 'pending', request_digest: 'ab'.repeat(32), request_id: '11111111-1111-4111-8111-111111111111', requested_accounts: accounts } as PairingView
  const permissions = pairingPermissions({ permissions: ['account.manage', 'account.read'], principalKind: 'person' })
  const keys = defaultSelectedAccountKeys(accounts)
  assert.equal(keys.length, 5)
  const plan = planApproval({ view, choice: 'connect_only', selectedAccountKeys: keys, permissions })
  assert.equal(plan.ok, true)
  if (plan.ok) assert.deepEqual(plan.body.selected_account_keys, keys)
  accounts[1]!.config_home_id = accounts[0]!.config_home_id
  const duplicate = planApproval({ view, choice: 'connect_only', selectedAccountKeys: keys, permissions })
  assert.equal(duplicate.ok, false)
  if (!duplicate.ok) assert.match(duplicate.message, /share a config home/)
  accounts[1]!.config_home_id = ''
  const defaultHome = planApproval({ view, choice: 'connect_only', selectedAccountKeys: keys, permissions })
  assert.equal(defaultHome.ok, false)
  if (!defaultHome.ok) assert.match(defaultHome.next, /separate isolated config home/)
})
