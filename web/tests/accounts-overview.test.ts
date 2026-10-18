// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { overviewAccounts, sharedByComputers, type SignInReference } from '../src/lib/accountsOverview.ts'
import { defaultHostPolicy, parseHostCapacity } from '../src/lib/hostCapacity.ts'
import type { AgentAccount } from '../src/lib/agents.ts'
const account = (id: string, pool = '', harness = 'codex') => ({ id, label: 'Same label', harness, quota_pool_fingerprint: pool } as AgentAccount)
test('accounts overview merges only a canonical shared quota identity within one harness', () => {
  const rows = overviewAccounts([account('one','pool'), account('two','pool'), account('three'), account('four'), account('five','pool','claude')], [], [])
  assert.equal(rows.length,4)
  assert.deepEqual(rows[0]!.records.map(r => r.id),['one','two'])
  assert.equal(rows[1]!.records.length,1)
  assert.equal(rows[3]!.harness,'claude')
})
test('host capacity parsing retains off defaults and rejects oversized history', () => {
  const value = { policy: defaultHostPolicy(), running: 2, queued: 3, reason: '', load_limit: 0, history: [], signals: null, reported_at: null }
  assert.equal(parseHostCapacity(value)?.policy.mode,'off')
  assert.equal(parseHostCapacity(value)?.policy.consider_activity,false)
  assert.equal(parseHostCapacity({ ...value, history: Array.from({length:61},() => ({ at:'2026-10-05T12:00:00Z',load:2 })) }),undefined)
  assert.equal(parseHostCapacity({ ...value, policy:{ ...value.policy,maximum_agents:65 } }),undefined)
})
// AEON-1062: QA saw "Shared by 1 computers". Two sign-ins on one computer still count as one computer.
test('shared quota names its computers in the singular and plural, in English and German', () => {
  const on = (...ids: string[]) => ({ signins: ids.map(id => ({ computer: { computer_id: id } }) as SignInReference) })
  assert.equal(sharedByComputers(on('mac', 'mac')), 'Shared by 1 computer')
  assert.equal(sharedByComputers(on('mac', 'mini', 'mac')), 'Shared by 2 computers')
  assert.equal(sharedByComputers(on()), 'Not signed in on any computer')
  assert.equal(sharedByComputers(on('mac'), 'de'), 'Von 1 Computer geteilt')
  assert.equal(sharedByComputers(on('mac', 'mini'), 'de'), 'Von 2 Computern geteilt')
  assert.equal(sharedByComputers(on(), 'de'), 'Auf keinem Computer angemeldet')
})
