// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DEFAULT_AGENT_STATE, STATE_LABEL, assessAgentState, deriveAgentState, normalizeAgentState, problemReason, type StateEvidence } from '../src/lib/agentSignals.ts'
import { liveState, type LiveAgent } from '../src/lib/liveAgents.ts'
import { sessionStatus } from '../src/lib/agentState.ts'
import type { HarnessSession } from '../src/lib/agents.ts'

const now = Date.parse('2026-09-27T09:00:00Z')
const ago = (ms: number) => new Date(now - ms).toISOString()
const evidence = (fields: Partial<StateEvidence> = {}): StateEvidence => ({ phase: 'working', activity: 'busy', heartbeat_at: ago(0), created_at: ago(900_000), ...fields })

test('warning boundaries are inclusive at three and ten minutes, not at two', () => {
  for (const [age, expected] of [[120_000, 'working'], [179_999, 'working'], [180_000, 'awaiting'], [599_999, 'awaiting'], [600_000, 'unresponsive']] as const) {
    assert.equal(deriveAgentState(evidence({ heartbeat_at: ago(age) }), now), expected)
  }
  const custom = { ...DEFAULT_AGENT_STATE, yellowMinutes: 5, redMinutes: 12 }
  assert.equal(deriveAgentState(evidence({ heartbeat_at: ago(299_999) }), now, custom), 'working')
  assert.equal(deriveAgentState(evidence({ heartbeat_at: ago(300_000) }), now, custom), 'awaiting')
  assert.equal(deriveAgentState(evidence({ heartbeat_at: ago(720_000) }), now, custom), 'unresponsive')
})

test('a lost working heartbeat warns even while starting or stopping; clock skew cannot create an error', () => {
  for (const phase of ['starting', 'working', 'stopping']) {
    assert.equal(deriveAgentState(evidence({ phase, heartbeat_at: null, created_at: ago(0) }), now), 'awaiting')
    assert.equal(deriveAgentState(evidence({ phase, heartbeat_at: null }), now), 'unresponsive')
  }
  assert.equal(deriveAgentState(evidence({ heartbeat_at: 'invalid' }), now), 'unresponsive')
  assert.equal(deriveAgentState(evidence({ heartbeat_at: ago(-60_000) }), now), 'working')
})

test('normal stops and inactive sessions never turn red from heartbeat age', () => {
  for (const stop_reason of [null, '', 'stopped', 'completed', 'operator_stop', 'user requested', 'cancelled', 'interrupted']) {
    assert.equal(problemReason(stop_reason), false)
    assert.equal(deriveAgentState(evidence({ phase: 'stopped', stop_reason, stopped_at: ago(300_000), heartbeat_at: ago(3600_000) }), now), 'stopped')
  }
  assert.equal(deriveAgentState(evidence({ activity: 'idle' }), now), 'idle')
  assert.equal(deriveAgentState(evidence({ activity: 'idle', heartbeat_at: ago(3600_000) }), now), 'stale')
})

test('failures and blocked/error stops lead requests, throttling and terminal phase', () => {
  for (const stop_reason of ['failed', 'stopped_with_error', 'blocked', 'ownership_lost', 'run crashed', 'timed-out']) {
    assert.equal(deriveAgentState(evidence({ phase: 'stopped', stop_reason }), now), 'problem')
  }
  for (const run_status of ['failed', 'ownership_lost']) {
    assert.equal(deriveAgentState(evidence({ run_status, activity: 'throttled', needs_attention: true }), now), 'problem')
  }
  assert.equal(deriveAgentState(evidence({ has_problem: true, phase: 'stopped' }), now), 'problem', 'redacted error evidence still carries a state')
})

test('throttling has a distinct state and requests use a clock even when work continues', () => {
  assert.equal(deriveAgentState(evidence({ activity: 'throttled' }), now), 'throttled')
  assert.equal(deriveAgentState(evidence({ activity: 'throttled', heartbeat_at: ago(3600_000) }), now), 'throttled')
  assert.equal(deriveAgentState(evidence({ needs_attention: true }), now), 'waiting')
  assert.equal(deriveAgentState(evidence({ phase: 'yielded' }), now), 'waiting')
  assert.equal(deriveAgentState(evidence({ run_status: 'waiting' }), now), 'waiting')
  assert.equal(deriveAgentState(evidence(), now, DEFAULT_AGENT_STATE, true), 'waiting')
})

test('project and workspace adapters agree for every evidence combination', () => {
  for (const phase of ['starting', 'working', 'yielded', 'stopping', 'stopped']) {
    for (const activity of ['busy', 'unknown', 'idle', 'throttled']) {
      for (const age of [0, 180_000, 600_000]) {
        const facts = evidence({ phase, activity, heartbeat_at: ago(age) })
        const state = sessionStatus(facts as HarnessSession, now).state
        assert.equal(liveState({ ...facts, since: facts.created_at } as LiveAgent, now), state)
        assert.ok(STATE_LABEL[state])
      }
    }
  }
})

test('viewer preferences validate palette, dimming and ordered integer thresholds', () => {
  assert.deepEqual(normalizeAgentState(null), DEFAULT_AGENT_STATE)
  assert.deepEqual(normalizeAgentState({ palette: 'unknown', inactiveOpacity: NaN, yellowMinutes: Infinity }), DEFAULT_AGENT_STATE)
  const pref = normalizeAgentState({ palette: 'colour-blind', dimInactive: false, inactiveOpacity: 99, yellowMinutes: 20, redMinutes: 5 })
  assert.equal(pref.inactiveOpacity, 80)
  assert.equal(pref.redMinutes, 21)
  assert.equal(pref.dimInactive, false)
  assert.equal(normalizeAgentState({ inactiveOpacity: 0 }).inactiveOpacity, 40)
  assert.equal(normalizeAgentState({ yellowMinutes: 5000 }).redMinutes, 1440)
})

// Regression: the two live reports behind SC2 had no failure evidence.
test('missing heartbeat is explained without inventing a worker failure', () => {
  for (const heartbeat_at of [null, ago(7_200_000)]) {
    const status = assessAgentState(evidence({ phase: 'starting', heartbeat_at, has_problem: false, needs_attention: false }), now)
    assert.equal(status.state, 'unresponsive')
    assert.equal(status.label, 'No heartbeat')
    assert.equal(status.reasons[0]?.code, 'heartbeat')
    assert.match(status.reasons[0]!.detail, heartbeat_at ? /overdue/ : /No heartbeat has been received/)
    assert.match(status.reasons[0]!.next, /does not establish that the worker failed/)
  }
})

test('explicit fresh false beats fallback evidence; only a new heartbeat clears age warnings', () => {
  assert.equal(deriveAgentState(evidence({ has_problem: false, needs_attention: false, run_status: 'failed' }), now, DEFAULT_AGENT_STATE, true), 'working')
  const facts = evidence({ heartbeat_at: ago(599_999), has_problem: false, needs_attention: false })
  assert.equal(deriveAgentState(facts, now), 'awaiting')
  assert.equal(deriveAgentState(facts, now + 1), 'unresponsive')
  assert.equal(deriveAgentState(facts, now + 20_000), 'unresponsive')
  assert.equal(deriveAgentState({ ...facts, heartbeat_at: ago(-20_000) }, now + 20_000), 'working')
})

test('failure detail preserves the exact stop reason and bound run outcome', () => {
  const status = assessAgentState(evidence({ has_problem: true, stop_reason: 'worker crashed: exit 2', run_status: 'ownership_lost' }), now)
  assert.equal(status.state, 'problem')
  assert.deepEqual(status.reasons.map(reason => reason.detail), ['Reported stop reason: worker crashed: exit 2', 'The bound run reported ownership lost.'])
  assert.ok(status.reasons.every(reason => reason.next.length > 0))
})
