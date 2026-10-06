// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { estimates, hostTick, normalizeScope, prediction, predictWindDown, selectedAgent, toggleScope } from '../src/lib/agentPause.ts'
import { assessAgentState } from '../src/lib/agentSignals.ts'
import type { HarnessSessionRow } from '../src/lib/agentRows.ts'
const now = Date.parse('2026-10-02T09:30:00Z'), minute = 60_000
const row = (fields: Partial<HarnessSessionRow> = {}) => ({ id: 'a', host: 'studio', phase: 'working', stopped_at: null, management_mode: 'unmanaged', advertised_capabilities: ['pause'], finished: false, activity: 'busy', heartbeat_at: new Date(now).toISOString(), ...fields }) as HarnessSessionRow
const progress = (fields = {}) => ({ reported_at: new Date(now).toISOString(), ...fields })
test('selection separates host admission intent from individual agents and normalizes tri-state', () => {
  const sessions = [row(), row({ id: 'b' }), row({ id: 'c', host: 'build' }), row({ id: 'paused', host: 'idle', phase: 'stopped' })], hosts = ['studio', 'build', 'idle', 'offline']
  let scope = toggleScope({ hosts: [], agents: [] }, 'agent', 'a', sessions, hosts)
  assert.equal(hostTick('studio', scope, sessions), 'mixed'); assert.deepEqual(scope.hosts, [])
  scope = toggleScope(scope, 'host', 'studio', sessions, hosts)
  assert.equal(hostTick('studio', scope, sessions), 'true'); assert.deepEqual(scope.agents, ['a', 'b']); assert.deepEqual(scope.hosts, ['studio'])
  scope = toggleScope(scope, 'agent', 'a', sessions, hosts)
  assert.equal(hostTick('studio', scope, sessions), 'mixed'); assert.deepEqual(scope.hosts, [])
  scope = toggleScope(scope, 'host', 'idle', sessions, hosts)
  assert.equal(hostTick('idle', scope, sessions), 'true'); assert.ok(!scope.agents?.includes('paused'))
  assert.deepEqual(normalizeScope({ hosts, agents: ['a', 'b', 'c'] }, sessions, hosts), { hosts: 'all' })
  const partial = toggleScope({ hosts: 'all' }, 'agent', 'b', sessions, hosts)
  assert.deepEqual(partial.hosts, ['build', 'idle', 'offline']); assert.deepEqual(partial.agents, ['a', 'c'])
})
test('wind-down chooses fitting wrap-up before quick pause and respects margins and uninterruptible commands', () => {
  assert.deepEqual(predictWindDown(row({ pause_progress: progress({ finish_in_min: 1, next_point_in_min: 5 }) }), now, now + 2 * minute, 10), { level: 'wrap_up', starts: now })
  assert.equal(predictWindDown(row({ pause_progress: progress({ finish_in_min: 10, next_point_in_min: 5 }) }), now, now + 2 * minute, 10).level, 'pause_quickly')
  assert.deepEqual(predictWindDown(row(), now, now + 15 * minute, 10), { level: 'pause', starts: now + 5 * minute })
  assert.equal(predictWindDown(row({ pause_progress: progress({ interrupt: false, command_left_min: 14, next_point_in_min: 1 }) }), now, now + 2 * minute, 10).level, 'pause')
  assert.deepEqual(predictWindDown(row({ advertised_capabilities: [] }), now, now + 15 * minute, 10), { level: 'stop_now', starts: now + 15 * minute })
})
test('effective agent lists override all hosts, including an explicit empty list', () => {
  const sessions = [row(), row({ id: 'b' }), row({ id: 'c', host: 'build' })]
  const scope = { hosts: 'all' as const, agents: ['a'] }
  assert.equal(selectedAgent(sessions[0]!, scope), true)
  assert.equal(selectedAgent(sessions[1]!, scope), false)
  assert.equal(selectedAgent(sessions[2]!, scope), false)
  assert.equal(hostTick('studio', scope, sessions), 'mixed')
  assert.equal(hostTick('build', scope, sessions), 'false')
  assert.equal(hostTick('idle', scope, sessions), 'true')
  assert.deepEqual(normalizeScope(scope, sessions, ['studio', 'build']), scope)
  assert.equal(selectedAgent(row(), { hosts: 'all', agents: [] }), false)
  assert.equal(hostTick('studio', { hosts: 'all', agents: [] }, sessions), 'false')
  assert.equal(selectedAgent(row(), { hosts: 'all' }), true)
})
test('planning counts down from its report and stale or cleared snapshots never revive legacy ETA', () => {
  const s = row({ eta_ready_at: new Date(now + 2 * minute).toISOString(), eta_reported_at: new Date(now).toISOString(), pause_progress: { reported_at: new Date(now - minute).toISOString(), finish_in_min: 4 } })
  assert.equal(estimates(s, now, 10).finish, 3 * minute)
  s.pause_progress = { reported_at: new Date(now - 21 * minute).toISOString() }
  assert.equal(estimates(s, now, 10).finish, null)
  assert.match(prediction(s, 'wrap_up', now, 10).detail, /No estimate/)
  assert.match(prediction(s, 'pause_quickly', now, 10).detail, /No estimate/)
  assert.match(prediction(row({ pause_progress: progress({ interrupt: false }) }), 'pause_quickly', now, 10).detail, /No estimate/)
  assert.equal(estimates(s, now, null).fresh, false)
})
test('paused is resumable evidence, scheduled pauses stay working and stop requests do not claim exit', () => {
  assert.equal(assessAgentState({ ...row(), pause: { state: 'requested', deliver: false } }, now).state, 'working')
  assert.equal(assessAgentState({ ...row(), pause: { state: 'planned', deliver: true } }, now).state, 'pausing')
  assert.equal(assessAgentState({ ...row({ heartbeat_at: new Date(now - 21 * minute).toISOString() }), pause: { state: 'requested', deliver: true } }, now).state, 'unresponsive')
  assert.equal(assessAgentState({ ...row(), pause: { state: 'cancelled', stop_requested: true } }, now).state, 'pausing')
  assert.equal(assessAgentState({ ...row({ phase: 'stopped' }), pause: { state: 'paused' } }, now).state, 'paused')
})

test('stop requests remain visible through stale heartbeats until a worker stop report', () => {
  for (const activity of ['busy', 'idle', 'throttled']) for (const heartbeat_at of [new Date(now).toISOString(), new Date(now - 21 * minute).toISOString()]) {
    const requested = { ...row({ activity, heartbeat_at }), pause: { state: 'cancelled', stop_requested: true } }
    const pending = assessAgentState(requested, now)
    assert.equal(pending.state, 'pausing')
    assert.equal(pending.label, 'Stop requested')
    assert.match(pending.reasons[0]!.detail, /not confirmed/)
    const confirmed = assessAgentState({ ...requested, phase: 'stopped', stopped_at: new Date(now).toISOString(), stop_reason: 'stopped' }, now)
    assert.equal(confirmed.state, 'stopped')
    assert.equal(confirmed.label, 'Stopped')
  }
  assert.equal(assessAgentState({ ...row(), pause: { state: 'cancelled', stop_requested: false } }, now).label, 'Working')
})
