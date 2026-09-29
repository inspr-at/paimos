// SPDX-License-Identifier: AGPL-3.0-only
// AEON-291: one copy of the server's managed-control eligibility, per prerequisite.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { HarnessSession } from '../src/lib/agents.ts'
import { managedControlAllowed, managedControlUnavailable } from '../src/lib/managedControl.ts'

const now = Date.parse('2026-09-29T09:00:00Z')
const ago = (ms: number) => new Date(now - ms).toISOString()
const ownership = { daemon_id: 'd', generation: 'g'.repeat(32), process_id: 'p'.repeat(32), root_pid: 9, group_id: 9, started_at: ago(3_600_000) }
const session = (fields: Partial<HarnessSession> = {}) => ({
  id: 's', project_id: 'p', harness: 'claude', management_mode: 'managed', run_id: 'r', phase: 'working', stopped_at: null, archived_at: null,
  advertised_capabilities: ['interrupt', 'stop', 'steer', 'managed_control_v1', 'rename'], process_ownership: ownership, process_observed_at: ago(5_000),
  ...fields,
}) as HarnessSession
const access = { now, allowed: true, runStatus: 'running' }

test('a live owned Claude run takes every advertised managed control', () => {
  assert.equal(managedControlUnavailable(session(), access), '')
  for (const kind of ['interrupt', 'stop', 'steer', 'rename'] as const) assert.equal(managedControlAllowed(session(), kind, access), true)
  assert.equal(managedControlAllowed(session(), 'model', access), false)
})

test('each server prerequisite blocks on its own', () => {
  const blocked: [string, HarnessSession, typeof access][] = [
    ['permission', session(), { ...access, allowed: false }],
    ['stopped', session({ phase: 'stopped', stopped_at: ago(0) }), access],
    ['archived', session({ archived_at: ago(0) }), access],
    ['not claude', session({ harness: 'codex' }), access],
    ['no run', session({ run_id: null }), access],
    ['no stop capability', session({ advertised_capabilities: ['interrupt', 'managed_control_v1'] }), access],
    ['no managed_control_v1', session({ advertised_capabilities: ['interrupt', 'stop'] }), access],
    ['unmanaged', session({ management_mode: 'unmanaged' }), access],
    ['run failed', session(), { ...access, runStatus: 'failed' }],
    ['run status unknown', session(), { ...access, runStatus: undefined }],
    ['no ownership', session({ process_ownership: undefined }), access],
    ['ownership stale', session({ process_observed_at: ago(46_000) }), access],
    ['ownership from the future', session({ process_observed_at: ago(-5_000) }), access],
  ]
  for (const [name, s, a] of blocked) {
    assert.notEqual(managedControlUnavailable(s, a), '', name)
    assert.equal(managedControlAllowed(s, 'interrupt', a), false, name)
  }
  // Interrupt without the interrupt capability, even when everything else holds.
  assert.equal(managedControlAllowed(session({ advertised_capabilities: ['stop', 'managed_control_v1'] }), 'interrupt', access), false)
  assert.equal(managedControlAllowed(session({ advertised_capabilities: ['stop', 'managed_control_v1'] }), 'stop', access), true)
})

test('reasons keep the panel wording', () => {
  assert.equal(managedControlUnavailable(session(), { ...access, allowed: false }), 'You need permission to control this session.')
  assert.equal(managedControlUnavailable(session({ phase: 'stopped', stopped_at: ago(0) }), access), 'This session has stopped.')
  assert.equal(managedControlUnavailable(session({ process_observed_at: ago(60_000) }), access), 'Waiting for the owning daemon to confirm this process.')
  assert.equal(managedControlUnavailable(session(), { ...access, runStatus: 'failed' }), 'Its run is not running.')
})
