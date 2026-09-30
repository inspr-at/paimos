// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  agentName, controlBlocked, cost, decidedApprovals, duration, expiresIn, groupSessions, heldRequests, needsYou,
  pendingApprovals, riskFor, riskOf, runDuration, scopeLabel, sessionStatus, sessionForest, tokens,
} from '../src/lib/agentState.ts'
import type { AgentRun, Approval, HarnessSession, ProjectMessage } from '../src/lib/agents.ts'

const now = Date.parse('2026-09-24T12:00:00Z')
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
function session(fields: Partial<HarnessSession> = {}): HarnessSession {
  return {
    id: 's1', project_id: 'p1', agent_principal_id: 'a1', run_id: 'r1', ticket_node_id: 'n1', work_order_id: null, parent_harness_session_id: null,
    harness: 'claude', host: 'imac0', management_mode: 'managed', role: 'worker', work_shape: 'ship', advertised_capabilities: ['interrupt', 'stop'],
    phase: 'working', activity: 'busy', activity_sequence: 1, revision: 1, heartbeat_at: ago(0.5), stopped_at: null, stop_reason: null, created_at: ago(90), ...fields,
  }
}
function approval(fields: Partial<Approval> = {}): Approval {
  return { id: 'ap1', agent_principal_id: 'a1', scope: 'run.claim', resource_kind: 'run', resource_id: 'r1', run_id: 'r1', rationale: '', expires_at: ago(-30), proposed_at: ago(5), decision: null, ...fields }
}
function message(fields: Partial<ProjectMessage> = {}): ProjectMessage {
  return { id: 'm1', sender_principal_id: 'a2', recipient_principal_id: 'a1', to: 'claude:camy', body: 'x', sent_event_id: 1, is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none', ...fields }
}

test('per-session labels distinguish workers sharing a principal and address', () => {
  const shared = { a1: 'claude:coordinator' }
  assert.equal(agentName(session({ display_label: 'AC4 hierarchy' }), shared), 'AC4 hierarchy')
  assert.equal(agentName(session({ display_label: 'AC5 launch' }), shared), 'AC5 launch')
  assert.equal(agentName(session({ display_label: '  ' }), shared), 'coordinator')
})

test('session families cross status groups without losing any worker or orphan', () => {
  const sessions = [
    session({ id: 'child', parent_harness_session_id: 'lead' }),
    session({ id: 'lead', role: 'coordinator', phase: 'stopped', stopped_at: ago(5) }),
    session({ id: 'grandchild', parent_harness_session_id: 'child', activity: 'idle' }),
    session({ id: 'orphan', parent_harness_session_id: 'unavailable' }),
    session({ id: 'foreign-project', project_id: 'p2', parent_harness_session_id: 'lead' }),
  ]
  const views = sessions.map(s => ({ session: s, status: sessionStatus(s, now) }))
  const tree = sessionForest(views, now)
  assert.deepEqual(tree.map(n => n.view.session.id), ['lead', 'orphan', 'foreign-project'])
  assert.equal(tree[0]!.group, 'working')
  assert.equal(tree[0]!.count, 3)
  assert.equal(tree[0]!.liveCount, 2)
  assert.equal(tree[0]!.children[0]!.children[0]!.view.session.id, 'grandchild')
  assert.equal(views[1]!.status.label, 'Stopped')
})

test('worker families sort working and starting by heartbeat, then idle, then stopped', () => {
  const sessions = [
    session({ id: 'lead', role: 'coordinator' }),
    session({ id: 'stopped', parent_harness_session_id: 'lead', phase: 'stopped', stopped_at: ago(0), heartbeat_at: ago(0) }),
    session({ id: 'idle', parent_harness_session_id: 'lead', activity: 'idle', heartbeat_at: ago(0) }),
    session({ id: 'busy-older', parent_harness_session_id: 'lead', heartbeat_at: ago(1) }),
    session({ id: 'starting', parent_harness_session_id: 'lead', phase: 'starting', heartbeat_at: ago(0.1) }),
    session({ id: 'busy-newer', parent_harness_session_id: 'lead', heartbeat_at: ago(0.2) }),
    session({ id: 'idle-needs', parent_harness_session_id: 'lead', activity: 'idle', heartbeat_at: ago(0.1) }),
    session({ id: 'stale', parent_harness_session_id: 'lead', heartbeat_at: ago(30) }),
  ]
  const tree = sessionForest(sessions.map(s => ({ session: s, status: sessionStatus(s, now, s.id === 'idle-needs') })), now)
  assert.deepEqual(tree[0]!.children.map(n => n.view.session.id), ['starting', 'busy-newer', 'busy-older', 'idle', 'idle-needs', 'stale', 'stopped'])
  assert.equal(tree[0]!.workingCount, 4) // includes the working lead
  assert.equal(tree[0]!.liveCount, 7) // includes idle, never a fabricated stop
  assert.equal(tree[0]!.count, 8)
})

test('live descendants keep stopped parents ahead of history; heartbeat ties use session ID', () => {
  const sessions = [
    session({ id: 'lead' }),
    session({ id: 'stopped', parent_harness_session_id: 'lead', phase: 'stopped', stopped_at: ago(0) }),
    session({ id: 'parent', parent_harness_session_id: 'lead', phase: 'stopped', stopped_at: ago(5) }),
    session({ id: 'b', parent_harness_session_id: 'parent' }),
    session({ id: 'a', parent_harness_session_id: 'parent' }),
  ]
  const tree = sessionForest(sessions.map(s => ({ session: s, status: sessionStatus(s, now) })), now)
  assert.deepEqual(tree[0]!.children.map(n => n.view.session.id), ['parent', 'stopped'])
  assert.deepEqual(tree[0]!.children[0]!.children.map(n => n.view.session.id), ['a', 'b'])
  assert.equal(tree[0]!.children[0]!.workingCount, 2)
})

test('invalid cyclic session bindings remain visible instead of recursing or disappearing', () => {
  const sessions = [session({ id: 'a', parent_harness_session_id: 'b' }), session({ id: 'b', parent_harness_session_id: 'a' }), session({ id: 'self', parent_harness_session_id: 'self' })]
  const tree = sessionForest(sessions.map(s => ({ session: s, status: sessionStatus(s, now) })), now)
  assert.deepEqual(tree.map(n => n.view.session.id), ['a', 'b', 'self'])
})

test('session states share words and shapes with project indicators', () => {
  assert.deepEqual(sessionStatus(session(), now), { state: 'working', group: 'working', tone: 'busy', label: 'Working' })
  assert.equal(sessionStatus(session({ phase: 'starting', heartbeat_at: null, created_at: ago(0) }), now).state, 'awaiting')
  assert.equal(sessionStatus(session({ phase: 'stopping' }), now).state, 'working')
  assert.equal(sessionStatus(session({ phase: 'yielded', activity: 'idle' }), now).state, 'waiting')
  assert.equal(sessionStatus(session({ activity: 'idle' }), now).state, 'idle')
  assert.equal(sessionStatus(session({ heartbeat_at: ago(3) }), now).state, 'awaiting')
  assert.equal(sessionStatus(session({ heartbeat_at: ago(10) }), now).state, 'unresponsive')
  assert.equal(sessionStatus(session({ phase: 'stopped', stopped_at: ago(1) }), now, true).state, 'stopped')
  assert.equal(sessionStatus(session(), now, true).state, 'waiting')
  assert.match(sessionStatus(session(), now, true).reasons![0]!.detail, /outstanding/)
})

test('heartbeat waiting has its own group while real requests still need a person', () => {
  const fresh = session({ id: 'new', phase: 'starting', heartbeat_at: null, created_at: ago(0), needs_attention: false })
  const overdue = session({ id: 'overdue', heartbeat_at: ago(4), needs_attention: false })
  const request = session({ ...fresh, id: 'request', needs_attention: true })
  const groups = groupSessions([fresh, overdue, request], now, () => false)
  assert.deepEqual(groups.awaiting.map(view => view.session.id), ['new', 'overdue'])
  assert.deepEqual(groups.needs.map(view => view.session.id), ['request'])
  assert.equal(sessionStatus(session({ ...fresh, heartbeat_at: ago(0) }), now).group, 'working')
  assert.equal(sessionStatus(session({ ...fresh, created_at: ago(10) }), now).group, 'unresponsive')
})

test('only an exact run approval attributes person action to a session', () => {
  const s = session()
  assert.equal(needsYou(s, [approval()], []), true)
  assert.equal(needsYou(s, [approval({ run_id: 'other' })], []), false)
  assert.equal(needsYou(s, [approval({ run_id: null })], []), true)
  assert.equal(needsYou(session({ run_id: null }), [approval({ run_id: 'other' })], []), false)
  assert.equal(needsYou(s, [], [{ ...message({ sender_principal_id: 'a1', is_action_request: true }), projectId: 'other-project' }]), false)
  assert.equal(needsYou(s, [], [message({ sender_principal_id: 'a1', is_action_request: true })]), false)
  assert.equal(needsYou(s, [approval({ run_id: null, resource_kind: 'node', resource_id: 'n1' })], []), false)
  assert.equal(needsYou(session({ phase: 'stopped', stopped_at: ago(1) }), [approval()], []), false)
})

test('groups sort live sessions by heartbeat and stopped ones by when they stopped', () => {
  const groups = groupSessions([
    session({ id: 'old', heartbeat_at: ago(1.5) }), session({ id: 'new', heartbeat_at: ago(0.1) }),
    session({ id: 'stopA', phase: 'stopped', stopped_at: ago(60) }), session({ id: 'stopB', phase: 'stopped', stopped_at: ago(5) }),
    session({ id: 'asks', agent_principal_id: 'a9' }),
  ], now, s => s.agent_principal_id === 'a9')
  assert.deepEqual(groups.working.map(e => e.session.id), ['new', 'old'])
  assert.deepEqual(groups.stopped.map(e => e.session.id), ['stopB', 'stopA'])
  assert.deepEqual(groups.needs.map(e => e.session.id), ['asks'])
})

test('agent names come from the message address, else the host', () => {
  assert.equal(agentName(session(), { a1: 'claude:camy' }), 'camy')
  assert.equal(agentName(session(), {}), 'imac0')
  assert.equal(agentName({ ...session(), agent: { id: 'a1', name: 'aeon-coordinator' } }, {}), 'aeon-coordinator')
  assert.equal(agentName({ ...session(), agent: { id: 'a1', name: 'aeon-coordinator' } }, { a1: 'claude:camy' }), 'camy')
})

test('controls are blocked with a reason people can act on', () => {
  assert.equal(controlBlocked(session(), 'stop', 'camy', true), '')
  assert.match(controlBlocked(session(), 'stop', 'camy', false), /may write/)
  assert.match(controlBlocked(session({ management_mode: 'unmanaged' }), 'interrupt', 'amy', true), /outside AEON/)
  assert.match(controlBlocked(session({ advertised_capabilities: ['stop'] }), 'interrupt', 'camy', true), /does not accept interrupts/)
  assert.match(controlBlocked(session(), 'interrupt', 'camy', true, { kind: 'stop', state: 'pending' }), /stop is on its way/)
  assert.equal(controlBlocked(session(), 'interrupt', 'camy', true, { kind: 'stop', state: 'completed' }), '')
  assert.match(controlBlocked(session({ phase: 'stopped', stopped_at: ago(1) }), 'stop', 'camy', true), /stopped/)
})

test('approvals: pending by urgency, history by recency, risk from what is allowed', () => {
  const list = [
    approval({ id: 'later', expires_at: ago(-90) }), approval({ id: 'soon', expires_at: ago(-5) }),
    approval({ id: 'expired', expires_at: ago(1) }), approval({ id: 'done', decision: 'approved', proposed_at: ago(1) }),
  ]
  assert.deepEqual(pendingApprovals(list, now).map(a => a.id), ['soon', 'later'])
  assert.deepEqual(decidedApprovals(list, now).map(a => a.id), ['done', 'expired'])
  assert.equal(riskOf({ scope: 'nodes.read', resource_kind: 'node' }), 'low')
  assert.equal(riskOf({ scope: 'run.claim', resource_kind: 'run' }), 'medium')
  assert.equal(riskOf({ scope: 'harness.control', resource_kind: 'node' }), 'high')
  assert.equal(riskOf({ scope: 'stage.deploy', resource_kind: 'node' }), 'high')
  assert.equal(riskOf({ scope: 'inbox.send', resource_kind: 'tenant' }), 'high')
  assert.equal(scopeLabel('run.claim'), 'Claim a run and start work')
  assert.equal(scopeLabel('journey.build'), 'Build journey')
  assert.equal(expiresIn(approval({ expires_at: ago(-42) }), now), 'Expires in 42m')
  assert.equal(expiresIn(approval({ expires_at: ago(1) }), now), 'Expired')
})

test('held action requests stay until a person resolves or dismisses them', () => {
  const list = [
    message({ id: 'a', is_action_request: true, reply_obligation: 'open' }), message({ id: 'b', is_action_request: true, human_resolution_outcome: 'resolved' }),
    message({ id: 'c', is_action_request: true, human_resolution_outcome: 'dismissed' }), message({ id: 'd' }),
  ]
  assert.deepEqual(heldRequests(list).map(m => m.id), ['a'])
})

test('the server’s risk wins over the local rule; runs use the server duration', () => {
  assert.equal(riskFor({ scope: 'nodes.read', resource_kind: 'node', risk: 'medium' }), 'medium')
  assert.equal(riskFor({ scope: 'nodes.read', resource_kind: 'node' }), 'low')
  const run = { id: 'r', work_order_id: 'w', agent_principal_id: 'a', status: 'completed' as const, model_evidence: 'unverified' as const, input_tokens: 0, output_tokens: 0, cost_micros: 0, created_at: ago(10), started_at: ago(10), ended_at: ago(1) }
  assert.equal(runDuration({ ...run, duration_ms: 125_000 }, now), '2m')
  assert.equal(runDuration(run, now), '9m')
  assert.equal(runDuration({ ...run, started_at: null }, now), '')
})

test('numbers read short: durations, tokens and cost', () => {
  assert.deepEqual([duration(45_000), duration(12 * 60_000), duration(134 * 60_000), duration(60 * 60_000), duration(28 * 3_600_000)], ['45s', '12m', '2h 14m', '1h', '1d 4h'])
  assert.deepEqual([tokens(950), tokens(4200), tokens(184_300), tokens(2_500_000)], ['950', '4.2k', '184k', '2.5M'])
  assert.deepEqual([cost(3_840_000), cost(0), cost(123_000_000)], ['$3.84', '$0.00', '$123'])
})

test('fresh session projection cannot be changed by a stale optional run or request cache', () => {
  const current = session({ run_status: null, has_problem: false, needs_attention: false })
  for (const status of ['failed', 'ownership_lost', 'waiting'] as const) {
    assert.equal(sessionStatus(current, now, true, undefined, { status } as AgentRun).state, 'working')
  }
})
