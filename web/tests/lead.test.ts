// SPDX-License-Identifier: AGPL-3.0-only
// AEON-741: the lead flow's words and derived state, from the server's authoritative lifecycle.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  atGate, canPause, canResume, checksSummary, decisionLine, LEAD_WORDS, leadBand, leadLine, leadWords, mergedToday, modelByRole, pluralWord, setLeadWords, startChecks, ticketSteps, waitCopy,
  type LeadDecision, type ProjectLead,
} from '../src/lib/lead.ts'
import { parentQueueSummary, type ParentQueueSnapshot } from '../src/lib/workQueue.ts'

const lead = (patch: Partial<ProjectLead> = {}): ProjectLead => ({ project_id: 'p1', revision: 3, generation: 2, session_id: 's1', state: 'working', reason: '', process_active: true, ...patch })
const decision = (id: number, stage: LeadDecision['request']['stage'], outcome: LeadDecision['outcome'], ticket?: string, at = '2026-10-06T10:00:00Z', gates: LeadDecision['gate_freshness'] = []): LeadDecision => ({
  event_id: id, project_id: 'p1', session_id: 's1', recorded_at: at, outcome, reason_codes: [], gate_freshness: gates, results: [],
  request: { stage, outcome, reason_codes: ['gates_ready'], attempt: 1, ...(ticket ? { ticket_node_id: ticket } : {}) },
})

test('the workspace word reads lower case mid-sentence, keeps acronyms and pluralises', () => {
  assert.deepEqual(leadWords(), { S: 'Lead', l: 'lead', P: 'Leads', pl: 'leads' })
  assert.deepEqual(leadWords('Supervisor'), { S: 'Supervisor', l: 'supervisor', P: 'Supervisors', pl: 'supervisors' })
  assert.equal(leadWords('AI').l, 'AI')
  assert.equal(pluralWord('Proxy'), 'Proxies')
  assert.equal(pluralWord('Boss'), 'Boss')
  assert.equal(leadWords('Conductor', 'Dirigenten').P, 'Dirigenten')
})

test('AEON-791: the saved agent names reach every screen that reads LEAD_WORDS, and clearing restores Lead', () => {
  setLeadWords({ singular: 'Dirigent', plural: 'Dirigenten' })
  try {
    assert.equal(leadBand(null, 'AEON', 0).title, 'No dirigent in AEON')
    assert.equal(leadBand(null, 'AEON', 0).actionLabel, 'Start dirigent')
    assert.equal(LEAD_WORDS.P, 'Dirigenten')
  } finally { setLeadWords(null) }
  assert.deepEqual({ ...LEAD_WORDS }, { S: 'Lead', l: 'lead', P: 'Leads', pl: 'leads' })
})

test('no lead: the band offers Start lead and counts queued work honestly', () => {
  const none = leadBand(lead({ state: 'none', session_id: null, revision: 0 }), 'AEON', 3)
  assert.equal(none.title, 'No lead in AEON')
  assert.equal(none.status, '3 queued work items wait for one')
  assert.equal(none.action, 'start')
  assert.equal(leadBand(null, 'AEON', 1).status, '1 queued work item waits for one')
  assert.equal(leadBand(null, 'AEON', 0).status, 'Nothing is queued yet')
  assert.equal(leadBand(null, 'AEON', null).status, 'The queue can’t be read right now')
})

test('every lifecycle state has one fitting action', () => {
  assert.equal(leadBand(lead({ state: 'working' }), 'AEON', 2).action, 'pause')
  assert.equal(leadBand(lead({ state: 'starting' }), 'AEON', 2).actionLabel, 'Cancel start')
  const requested = leadBand(lead({ state: 'waiting_for_room', reason: 'awaiting_generation', session_id: null, generation: 0 }), 'AEON', 2)
  assert.equal(requested.action, 'cancel')
  assert.match(requested.status, /waiting for its session/)
  assert.equal(leadBand(lead({ state: 'waiting_for_room', reason: 'dial_full' }), 'AEON', 2).action, 'dial')
  assert.equal(leadBand(lead({ state: 'waiting_for_room', reason: 'host_unavailable' }), 'AEON', 2).action, 'computers')
  assert.equal(leadBand(lead({ state: 'paused', reason: 'process_stopped', process_active: false }), 'AEON', 2).action, 'resume')
  assert.equal(leadBand(lead({ state: 'cannot_start', reason: 'project_archived' }), 'AEON', 2).action, 'none')
  assert.equal(leadBand(lead({ state: 'cannot_start', reason: 'owner_revoked' }), 'AEON', 2).action, 'start')
})

test('unreadable gates mean WAIT, never a start', () => {
  assert.match(waitCopy('account_unavailable').status, /account room can’t be read/)
  assert.match(waitCopy('harness_full').status, /a harness limit is full/)
  assert.match(waitCopy('start_checks_unavailable').now, /nothing new starts/)
  assert.match(waitCopy('something_new').status, /Waiting for room/)
})

test('an unclaimed adoption offers Cancel and neither Pause nor Resume', () => {
  const pending = lead({ state: 'waiting_for_room', reason: 'adoption_pending', generation: 0, session_id: 's1', process_active: true })
  const band = leadBand(pending, 'AEON', 2)
  assert.equal(band.action, 'cancel_adoption')
  assert.equal(band.actionLabel, 'Cancel')
  assert.equal(canPause(pending), false)
  assert.equal(canResume(pending), false)
  assert.equal(canResume(lead({ state: 'paused', reason: 'adoption_pending', process_active: false })), false)
  const cleared = lead({ state: 'waiting_for_room', reason: 'selection_cleared', generation: 0, session_id: null, process_active: false, revision: 2 })
  const next = leadBand(cleared, 'AEON', 2)
  assert.equal(next.action, 'start')
  assert.equal(next.actionLabel, 'Start lead')
  assert.equal(next.status, 'No session selected')
  assert.match(next.now, /previous choice was cleared/)
  assert.equal(canPause(lead({ state: 'waiting_for_room', session_id: null, reason: '' })), true)
})

test('Resume is only a restart after a confirmed stop; Pause needs an existing intent', () => {
  assert.equal(canResume(lead({ state: 'paused', process_active: true })), false)
  assert.equal(canResume(lead({ state: 'paused', process_active: false })), true)
  assert.equal(canPause(lead({ state: 'waiting_for_room', session_id: null })), true)
  assert.equal(canPause(lead({ state: 'none', revision: 0 })), false)
  assert.equal(canPause(lead({ state: 'paused' })), false)
  const pausing = leadBand(lead({ state: 'paused', reason: 'handover_pending', process_active: true }), 'AEON', 0)
  assert.match(pausing.status, /Pausing/)
  assert.match(pausing.foot, /once its session has stopped/)
})

test('the line shows counts, at most two keys and the rest as +n; unknown stays unknown', () => {
  const [queued, working, gate, merged] = leadLine({ state: 'waiting_for_room', queued: ['A-1', 'A-2', 'A-3'], waiting: true, working: ['A-9'], gate: null, merged: [] })
  assert.deepEqual([queued!.count, queued!.keys, queued!.more, queued!.tone], [3, ['A-1', 'A-2'], 1, 'hold'])
  assert.deepEqual([working!.count, working!.tone], [1, 'live'])
  assert.deepEqual([gate!.count, gate!.keys], [null, []])
  assert.deepEqual([merged!.count, merged!.tone], [0, ''])
  assert.equal(leadLine({ state: 'paused', queued: [], waiting: false, working: ['A-9'], gate: [], merged: [] })[1]!.tone, '')
})

test('the gate and today’s merges come from the latest reported decisions', () => {
  const now = new Date('2026-10-06T15:00:00Z')
  const list = [
    decision(1, 'review', 'requested', 't1'), decision(2, 'review', 'requested', 't2'), decision(3, 'review', 'passed', 't1'),
    decision(4, 'release_handoff', 'handoff', 't1', '2026-10-06T12:00:00Z'), decision(5, 'release_handoff', 'handoff', 't3', '2026-10-04T12:00:00Z'),
  ]
  assert.deepEqual(atGate(list), ['t2'])
  assert.deepEqual(mergedToday(list, now), ['t1'])
  assert.equal(decisionLine(list[2]!, id => id.toUpperCase()), 'T1 passed its cross-family review')
  assert.equal(decisionLine(decision(9, 'queue', 'selected', 't4'), () => 'AEON-720'), 'Picked up AEON-720 from the queue')
  assert.equal(decisionLine({ ...decision(9, 'queue', 'wait'), reason_codes: ['no_work'] }, () => 'x'), 'Waited for queued work: no work')
})

test('start checks: the current wait reason wins, reported freshness follows, nothing is invented', () => {
  const reported = [decision(1, 'admission', 'selected', 't1', '2026-10-06T10:00:00Z', [{ kind: 'dial', freshness: 'fresh' }, { kind: 'host_load', freshness: 'unreadable' }])]
  const checks = startChecks(lead({ state: 'waiting_for_room', reason: 'harness_full' }), reported, { running: 3, total: 5 })
  assert.deepEqual(checks.map(c => c.state), ['ok', 'full', 'unknown', 'unreadable'])
  assert.equal(checks[0]!.detail, '3 of 5 running')
  assert.equal(startChecks(lead(), [], null)[2]!.detail, 'Checked at the next start')
  assert.equal(startChecks(lead({ reason: 'dial_full' }), [], { running: 5, total: 5 })[0]!.detail, '5 of 5 · full')
})

test('start checks: a fresh reading of a full gate is full, and past readings never read as current', () => {
  const fresh = (['dial', 'harness', 'account_room', 'host_load'] as const).map(kind => ({ kind, freshness: 'fresh' as const }))
  const waited = { ...decision(2, 'admission', 'wait', 't1', '2026-10-06T10:00:00Z', fresh), reason_codes: ['gates_ready', 'account_room_full'] }
  const checks = startChecks(lead({ state: 'waiting_for_room', reason: 'awaiting_generation' }), [waited], null)
  assert.deepEqual(checks.map(c => c.state), ['ok', 'ok', 'full', 'ok'])
  assert.match(checks[2]!.detail, /^Full at \d\d:\d\d$/)
  assert.equal(checksSummary(checks), 'A limit is full')
  // The server may also carry the full state only in the reported gates.
  const gated = decision(3, 'admission', 'wait', 't1', '2026-10-06T10:00:00Z', fresh)
  gated.request.gates = [{ kind: 'host_load', state: 'full', observed_at: '2026-10-06T09:59:30Z' }]
  assert.equal(startChecks(lead(), [gated], null)[3]!.state, 'full')
  const passed = startChecks(lead(), [decision(4, 'admission', 'selected', 't1', '2026-10-06T10:00:00Z', fresh)], null)
  assert.equal(checksSummary(passed), 'All passed at the last start')
  assert.notEqual(checksSummary(passed), 'All checks pass')
})

test('a ticket has exactly one current step on its way to Merged', () => {
  const base = { queuedPosition: null, queuedAt: null, estimateHours: 2, status: 'open' as const, worker: null, review: 'none' as const }
  const now = (input: Parameters<typeof ticketSteps>[0]) => ticketSteps(input).filter(s => s.state === 'now').map(s => s.id)
  assert.deepEqual(now({ ...base, queuedPosition: 1 }), ['queued'])
  assert.equal(ticketSteps({ ...base, queuedPosition: 1 })[0]!.note, 'Next in line')
  assert.equal(ticketSteps({ ...base, queuedPosition: 4 })[1]!.note, '2 h estimated')
  assert.deepEqual(now({ ...base, status: 'progress', worker: 'Codex worker' }), ['working'])
  assert.deepEqual(now({ ...base, status: 'progress', review: 'running' }), ['gate'])
  assert.deepEqual(now({ ...base, status: 'progress', review: 'passed' }), [])
  assert.deepEqual(ticketSteps({ ...base, status: 'done' }).map(s => s.state), ['done', 'done', 'done', 'done', 'done'])
  assert.deepEqual(now(base), [])
})

test('model by role shows the configured line and effort, or nothing', () => {
  assert.equal(modelByRole(null), '')
  assert.equal(modelByRole({ revision: 1, details_redacted: false, automatic_launch_enabled: false, owner_person_id: null, model_selector: { cell: { mode: 'pick', line: 'Opus 5.5', effort: 'high' } } }), 'Opus 5.5, high thinking')
  assert.equal(modelByRole({ revision: 1, details_redacted: true, automatic_launch_enabled: false, owner_person_id: null, model_selector: { cell: { mode: 'auto' } } }), '')
})

test('a parent queue reports queued, existing and skipped leaves and what remains', () => {
  const snapshot: ParentQueueSnapshot = { id: 's', parent_id: 'p', state: 'applied', truncated: true, continuation_available: true, partial: true, tree_changed: false,
    items: [{ node_id: '1', outcome: 'queued' }, { node_id: '2', outcome: 'queued' }, { node_id: '3', outcome: 'already_queued' }, { node_id: '4', outcome: 'not_ready' }] }
  assert.equal(parentQueueSummary(snapshot), '2 queued · 1 already queued · 1 skipped (changed, not ready, active or unavailable) · more open work remains below; Queue again to continue')
  assert.equal(parentQueueSummary({ ...snapshot, truncated: false, continuation_available: false, items: [] }), '0 queued')
})
