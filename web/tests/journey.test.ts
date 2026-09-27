// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  captureJourneyConfirmation, matchesJourneyConfirmation, gateApprovals, gateApprovalState, nextPick, nextPickLabel, offeredApproval, orderedTickets, planWrite, releaseRefs, selectionOf, ticketGroups, walkOrder,
  type Journey, type JourneyStage, type Walker, type WalkerTicket,
} from '../src/lib/journey.ts'
import type { Approval } from '../src/lib/agents.ts'
import type { WorkNode } from '../src/lib/api.ts'

const ticket = (id: string, feature: string | null, included: boolean, position: number): WalkerTicket =>
  ({ ticket_node_id: id, key: `K-${id}`, title: id, feature_node_id: feature, included, position, estimated_hours: null, screen_node_ids: [] })
const walker = (tickets: WalkerTicket[]): Walker => ({
  release_node_id: 'r', project_node_id: 'p', state: 'planning', revision: 5, tickets,
  features: [
    { feature_node_id: 'f1', epic_key: 'E-1', title: 'One', selection: 'some', included_count: 1, open_count: 2 },
    { feature_node_id: 'f2', epic_key: 'E-2', title: 'Two', selection: 'empty', included_count: 0, open_count: 0 },
  ],
})

test('features cycle all → none → the last partial pick → all', () => {
  const tickets = [ticket('a', 'f1', true, 0), ticket('b', 'f1', false, 1), ticket('c', 'f1', true, 2)]
  let included = new Set(['a', 'c'])
  assert.equal(selectionOf(tickets, included), 'some')
  const remembered = ['a', 'c']
  included = nextPick(tickets, included, remembered)
  assert.equal(selectionOf(tickets, included), 'all')
  assert.equal(nextPickLabel('all'), 'defer all to the backlog')
  included = nextPick(tickets, included, remembered)
  assert.equal(selectionOf(tickets, included), 'none')
  assert.equal(nextPickLabel('none', remembered), 'restore the earlier 2')
  included = nextPick(tickets, included, remembered)
  assert.deepEqual([...included].sort(), ['a', 'c'])
  // Without a memory, none goes to all.
  assert.deepEqual([...nextPick(tickets, new Set())].sort(), ['a', 'b', 'c'])
  assert.equal(selectionOf([], new Set()), 'empty')
  // Other features' tickets are untouched.
  assert.deepEqual([...nextPick(tickets, new Set(['x']))].sort(), ['a', 'b', 'c', 'x'])
})

test('groups follow the features, add epics for loose tickets, and keep the rest last', () => {
  const w = walker([ticket('t3', null, false, 3), ticket('t1', 'f1', true, 1), ticket('t2', null, true, 2), ticket('t0', 'f1', false, 0)])
  const epics: Record<string, { id: string; key: string; title: string }> = { t2: { id: 'e9', key: 'E-9', title: 'Imported epic' } }
  const groups = ticketGroups(w, id => epics[id] ?? null)
  assert.deepEqual(groups.map(g => g.id), ['f1', 'e9', 'f2', ''])
  assert.deepEqual(groups[0].tickets.map(t => t.ticket_node_id), ['t0', 't1'])
  assert.equal(groups[1].feature?.derived, true)
  assert.equal(groups[2].tickets.length, 0)
  assert.deepEqual(walkOrder(groups).map(t => t.ticket_node_id), ['t0', 't1', 't2', 't3'])
  assert.deepEqual(orderedTickets(w).map(t => t.position), [0, 1, 2, 3])
})

test('the plan write lists every ticket once and the included subset in order', () => {
  const w = walker([ticket('a', 'f1', true, 0), ticket('b', null, false, 1)])
  assert.deepEqual(planWrite(w, orderedTickets(w), new Set(['b'])), { expected_revision: 5, ordered_ticket_ids: ['a', 'b'], included_ticket_ids: ['b'] })
})

test('releases are numbered in creation order; journey releases keep their own number', () => {
  const node = (id: string, title: string, created: string) => ({ id, key: `R-${id}`, title, state: 'done', created_at: created }) as WorkNode
  const refs = releaseRefs([node('b', '260912102405.0.0', '2026-09-12T00:00:00Z'), node('a', '0.1.33', '2026-08-01T00:00:00Z'), node('c', 'Release 7', '2026-09-20T00:00:00Z')])
  assert.deepEqual(refs.map(r => [r.id, r.number, r.version]), [['a', 1, '0.1.33'], ['b', 2, '260912102405.0.0'], ['c', 7, null]])
})

test('gate approvals match the gate and its resource; requirements need the revision scope', () => {
  const now = Date.parse('2026-09-24T10:00:00Z')
  const approval = (id: string, scope: string, resource: string, extra: Partial<Approval> = {}): Approval => ({
    id, agent_principal_id: 'ag', scope, resource_kind: 'node', resource_id: resource, run_id: null, rationale: '', expires_at: '2026-09-24T12:00:00Z',
    proposed_at: `2026-09-24T09:0${id.length}:00Z`, decision: null, ...extra,
  })
  const approvals = [
    approval('b1', 'journey.build', 'rel'), approval('b22', 'journey.build', 'other'), approval('q1', 'journey.requirements', 'proj'),
    approval('q22', 'journey.requirements.r12.dabc', 'proj'), approval('q333', 'journey.requirements.r11.dold', 'proj'), approval('x1', 'nodes.read', 'rel'),
    approval('b4444', 'journey.build', 'rel', { decision: 'denied' }), approval('b55555', 'journey.build', 'rel', { expires_at: '2026-09-24T09:00:00Z' }),
  ]
  assert.deepEqual(gateApprovals(approvals, 'build', 'rel').map(a => a.id).sort(), ['b1', 'b4444', 'b55555'])
  const journey = { project_node_id: 'proj', current_release_id: 'rel', revision: 12, stages: [], next_action: { key: 'start_build', approval_request_id: 'b1' } } as unknown as Journey
  assert.equal(offeredApproval(approvals, journey, 'build', now)?.id, 'b1')
  const requirements = { ...journey, requirements_approval_scope: 'journey.requirements.r12.dabc', next_action: { ...journey.next_action, key: 'approve_requirements' as const, approval_request_id: 'q22' } }
  assert.equal(offeredApproval(approvals, requirements, 'requirements', now)?.id, 'q22')
  assert.equal(offeredApproval(approvals, { ...requirements, revision: 13, requirements_approval_scope: 'journey.requirements.r13.dabc' }, 'requirements', now), null)
  assert.equal(offeredApproval([], journey, 'build', now), null)
})

test('distinct standing and retry IDs retain applied evidence and exact fresh authority', () => {
  const now = Date.parse('2026-09-27T12:00:00Z')
  const standing: Approval = { id: 'standing', agent_principal_id: 'agent', scope: 'journey.deploy', resource_kind: 'node', resource_id: 'release', rationale: '', expires_at: '2026-09-27T14:00:00Z', proposed_at: '2026-09-27T10:00:00Z', decision: 'approved' }
  const retry = { ...standing, id: 'retry', decision: null }
  const stage = { key: 'deploy', gate_approval_id: standing.id, gate_live: true, gate_offer_id: retry.id, gate_offer_state: 'pending', gate_offer_expires_at: '2026-09-27T12:30:00Z' } as JourneyStage
  const journey = { project_node_id: 'project', current_release_id: 'release', stages: [stage], next_action: { key: 'retry_deploy', approval_request_id: retry.id } } as Journey
  assert.equal(gateApprovalState(standing, stage, now), 'applied')
  assert.equal(offeredApproval([standing, retry], journey, 'deploy', now)?.id, retry.id)
  assert.equal(offeredApproval([standing], journey, 'deploy', now), null, 'missing exact details cannot borrow the consumed grant')
  assert.equal(offeredApproval([standing, retry], { ...journey, next_action: { ...journey.next_action, approval_request_id: null } }, 'deploy', now), null)
  assert.equal(offeredApproval([standing, retry], { ...journey, next_action: { ...journey.next_action, approval_request_id: standing.id } }, 'deploy', now), null)
  const approved = { ...retry, decision: 'approved' as const }
  stage.gate_offer_state = 'approved_live'
  assert.equal(gateApprovalState(approved, stage, now), 'approved_live')
  assert.equal(offeredApproval([standing, approved], journey, 'deploy', now)?.id, retry.id)
  const expired = Date.parse(stage.gate_offer_expires_at!)
  assert.equal(gateApprovalState(approved, stage, expired), 'expired')
  assert.equal(offeredApproval([standing, approved], journey, 'deploy', expired), null, 'grant deadline precedes request deadline')
  for (const state of ['revoked', 'grant_missing', 'expired', 'rejected'] as const) {
    stage.gate_offer_state = state
    assert.equal(offeredApproval([standing, approved], journey, 'deploy', now), null, state)
  }
  stage.gate_live = false
  assert.equal(gateApprovalState(standing, stage, now), 'applied', 'revocation does not erase the applied historical record')
})

test('confirmation binds journey, requirements, release, action and request identities by value', () => {
  const approval: Approval = { id: 'request-a', scope: 'journey.requirements.r12.da', resource_kind: 'node', resource_id: 'project', agent_principal_id: 'agent', run_id: null, decision: null, expires_at: '2026-09-27T14:00:00Z', proposed_at: '2026-09-27T12:00:00Z', rationale: '' }
  const journey = {
    project_node_id: 'project', revision: 12, profile: 'professional', stage: 'requirements', current_release_id: 'release',
    requirements_revision: 3, requirements_digest_sha256: 'a', requirements_approval_scope: approval.scope,
    next_action: { key: 'approve_requirements', stage: 'requirements', approval_request_id: approval.id, available: false }, stages: [],
  } as unknown as Journey
  const captured = captureJourneyConfirmation(journey, 'approve_requirements', approval)
  assert.equal(matchesJourneyConfirmation(captured, structuredClone(journey), [{ ...approval }]), true, 'unchanged refreshed objects still match')
  for (const changed of [
    { ...journey, project_node_id: 'other' }, { ...journey, revision: 13 }, { ...journey, profile: 'enterprise' as const },
    { ...journey, stage: 'plan' as const }, { ...journey, current_release_id: 'other-release' },
    { ...journey, requirements_revision: 4 }, { ...journey, requirements_digest_sha256: 'b' }, { ...journey, requirements_approval_scope: 'other-scope' },
    { ...journey, next_action: { ...journey.next_action, key: 'start_build' as const } },
    { ...journey, next_action: { ...journey.next_action, stage: 'plan' as const } },
    { ...journey, next_action: { ...journey.next_action, approval_request_id: 'request-b' } },
  ]) assert.equal(matchesJourneyConfirmation(captured, changed, [approval]), false)
  for (const changed of [
    { ...approval, id: 'request-b' }, { ...approval, scope: 'other-scope' }, { ...approval, resource_id: 'other-project' },
    { ...approval, agent_principal_id: 'other-agent' }, { ...approval, run_id: 'other-run' },
  ]) assert.equal(matchesJourneyConfirmation(captured, journey, [changed]), false)
  assert.equal(matchesJourneyConfirmation(captured, journey, []), false)
  // Own approval may update the decision and availability before the action
  // write; neither change substitutes another request or journey revision.
  assert.equal(matchesJourneyConfirmation(captured, { ...journey, next_action: { ...journey.next_action, available: true } }, [{ ...approval, decision: 'approved' }]), true)
  journey.revision = 13
  approval.scope = 'mutated'
  assert.equal(captured.revision, 12)
  assert.equal(captured.approval!.scope, 'journey.requirements.r12.da')
})
