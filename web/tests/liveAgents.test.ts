// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { activeAgentLabel, activeByProject, activeSessions, advanceActivity, liveState, agentKey, byLead, chipText, phaseLabel, elapsedFor, groupLive, isActiveSession, isListedTicketWorker, liveChanges, liveSummary, phrase, sameLive, skewOf, ticketWorkers, who, type LiveAgent } from '../src/lib/liveAgents.ts'
import { DEFAULT_AGENT_STATE } from '../src/lib/agentSignals.ts'

const now = Date.parse('2026-09-26T12:00:00Z')
const ago = (seconds: number) => new Date(now - seconds * 1000).toISOString()
function agent(fields: Partial<LiveAgent> = {}): LiveAgent {
  return {
    project_id: 'p1', session_id: 's1', principal_id: 'a1', name: 'hausv', harness: 'claude', management_mode: 'unmanaged', role: 'worker',
    phase: 'working', activity: 'busy', ticket: { id: 't1', key: 'HAUSV-887', title: 'Statements', project_id: 'p1' }, since: ago(16 * 60), heartbeat_at: ago(30), ...fields,
  }
}

test('groupLive derives heartbeat warnings on the server clock and puts the lead first', () => {
  const coordinator = agent({ session_id: 's2', name: 'aeon-coordinator', role: 'coordinator', ticket: null, since: ago(3600) })
  const idleTicketless = agent({ session_id: 's3', name: 'scout', ticket: null, since: ago(60) })
  const early = agent({ session_id: 's4', name: 'early', since: ago(7200) })
  const stale = agent({ session_id: 's5', project_id: 'p2', heartbeat_at: ago(181) })
  const grouped = groupLive([coordinator, idleTicketless, agent(), early, stale], now)
  assert.deepEqual([...grouped.keys()], ['p1', 'p2'])
  assert.deepEqual(grouped.get('p1')!.map(a => a.session_id), ['s4', 's1', 's3', 's2'])
  assert.equal(groupLive([stale], now).get('p2')![0]!.state, 'awaiting')
  assert.equal(groupLive([stale], now - 2000).get('p2')![0]!.state, 'working', 'a server clock two seconds earlier still sees it fresh')
  assert.equal(groupLive([agent({ heartbeat_at: 'not a time' })], now).get('p1')![0]!.state, 'unresponsive')
  assert.ok(byLead(agent(), coordinator) < 0)
})

test('the server clock: skew from the page, elapsed from since', () => {
  assert.equal(skewOf({ at: new Date(now).toISOString() }, now + 1500), 1500)
  assert.equal(skewOf({ at: 'nonsense' }, now), 0)
  assert.equal(elapsedFor(agent(), now), '16m')
  assert.equal(elapsedFor(agent({ since: ago(30) }), now), '30s')
})

test('words for chips, labels and screen readers', () => {
  const nameless = agent({ name: undefined, principal_id: undefined, session_id: undefined, harness: 'codex' })
  assert.equal(who(nameless), 'Codex agent')
  assert.equal(phrase(agent()), 'hausv on HAUSV-887')
  assert.equal(phrase(agent({ ticket: null })), 'hausv')
  assert.equal(phrase(agent({ phase: 'starting', ticket: null })), 'hausv, working')
  assert.equal(phrase(agent({ phase: 'stopping' })), 'hausv, working on HAUSV-887')
  assert.equal(liveSummary([]), '')
  assert.equal(liveSummary([agent()]), '1 agent working: hausv on HAUSV-887')
  assert.equal(liveSummary([agent(), nameless]), '2 agents working: hausv on HAUSV-887, Codex agent on HAUSV-887')
  assert.deepEqual(chipText([agent(), nameless]), { name: 'hausv', key: 'HAUSV-887', more: 1 })
  assert.deepEqual(chipText([]), { name: '', key: '', more: 0 })
})

test('the live region hears starts and ends, never the first reading', () => {
  const titles: Record<string, string> = { p1: 'Hausverwaltung', p2: 'Janus' }
  const title = (id: string) => titles[id]
  const one = new Map([['p1', [agent()]]])
  assert.equal(liveChanges(null, one, title), '')
  assert.equal(liveChanges(new Map(), one, title), 'hausv started working on Hausverwaltung.')
  assert.equal(liveChanges(one, one, title), '')
  assert.equal(liveChanges(one, new Map([['p2', [agent({ project_id: 'p2' }), agent({ project_id: 'p2', session_id: 's9' })]]]), title),
    '2 agents started working on Janus. No agent is working on Hausverwaltung any more.')
  // A project the page does not know is left out; many changes become a count.
  assert.equal(liveChanges(new Map(), new Map([['p9', [agent()]]]), title), '')
  const many = new Map(['a', 'b', 'c', 'd'].map(id => [id, [agent({ project_id: id })]]))
  assert.equal(liveChanges(new Map(), many, id => id.toUpperCase()), 'Agents changed in 4 projects.')
})

test('agents joining and leaving a project that stays busy are news too', () => {
  const title = () => 'Janus'
  const camy = agent({ session_id: 's-camy', name: 'camy' }), nova = agent({ session_id: 's-nova', name: 'nova' }), rex = agent({ session_id: 's-rex', name: 'rex' })
  const at = (...agents: LiveAgent[]) => new Map([['p1', agents]])
  assert.equal(liveChanges(at(camy), at(camy, nova), title), 'nova started working on Janus.')
  assert.equal(liveChanges(at(camy, nova), at(nova), title), 'camy stopped working on Janus.')
  assert.equal(liveChanges(at(camy), at(nova, rex), title), '2 agents started working on Janus. camy stopped working on Janus.')
  // The same agents in another order, or with a new heartbeat, are no news; nor is a start undone before it was said.
  assert.equal(liveChanges(at(camy, nova), at(agent({ ...nova, heartbeat_at: ago(1) }), camy), title), '')
  assert.equal(liveChanges(at(camy), at(camy), title), '')
  // An agent the caller may not name keeps one identity across readings.
  const nameless = agent({ session_id: undefined, principal_id: undefined, name: undefined })
  assert.equal(agentKey(nameless), agentKey(agent({ ...nameless, heartbeat_at: ago(2) })))
  assert.equal(liveChanges(at(nameless), at(agent({ ...nameless, heartbeat_at: ago(2) })), title), '')
})

test('sameLive preserves evidence changes for glints and state changes for labels', () => {
  const a = new Map([['p1', [agent()]]])
  assert.ok(sameLive(a, new Map([['p1', [agent()]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent({ heartbeat_at: ago(5) })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent({ activity_note_id: 2 })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent({ state: 'stale' })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent({ display_label: 'grok-ta1' })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent({ ticket: { id: 't2', key: 'HAUSV-888', title: 'Other', project_id: 'p1' } })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent(), agent({ session_id: 's2' })]]])))
  assert.ok(!sameLive(a, new Map([['p2', [agent()]]])))
  assert.ok(!sameLive(a, new Map()))
})


test('waiting evidence and age thresholds use the same state contract', () => {
  assert.equal(liveState(agent({ activity: 'unknown', phase: 'starting' }), now), 'working')
  assert.equal(liveState(agent({ state: 'waiting' }), now), 'waiting')
  assert.equal(liveState(agent({ state: 'waiting', heartbeat_at: ago(600) }), now), 'unresponsive')
  assert.equal(liveState(agent({ heartbeat_at: ago(120) }), now, { ...DEFAULT_AGENT_STATE, yellowMinutes: 1, redMinutes: 2 }), 'unresponsive')
  assert.equal(liveSummary([agent({ state: 'waiting' })]), '1 agent: hausv, needs something on HAUSV-887')
  assert.equal(liveSummary([agent({ state: 'stale' })]), '1 agent: hausv, idle · no heartbeat on HAUSV-887')
})

test('activity pulses only for new notes, with monotonic watermarks', () => {
  const first = advanceActivity(undefined, agent({ activity_note_id: 4 }))
  assert.equal(first.pulse, 0, 'initial data is a baseline')
  assert.deepEqual(advanceActivity(first, agent({ activity_note_id: 4 })), first)
  assert.deepEqual(advanceActivity(first, agent({ activity_note_id: 4, heartbeat_at: ago(20), activity_sequence: 5 })), first)
  const next = advanceActivity(first, agent({ activity_note_id: 5 }))
  assert.equal(next.pulse, 1)
  assert.deepEqual(advanceActivity(next, agent({ activity_note_id: 4 })), next, 'old response cannot roll back evidence')
  assert.deepEqual(advanceActivity(next, agent({ activity_note_id: 5 })), next, 'replayed evidence stays quiet')
  assert.deepEqual(advanceActivity(next, agent({ activity_note_id: NaN })), next)
  const unknown = advanceActivity(undefined, agent())
  assert.equal(advanceActivity(unknown, agent({ activity_note_id: 6 })).pulse, 1, 'first new note after an empty baseline is an event')
})

test('stale evidence neither leads current work nor claims that a session stopped', () => {
  const stale = agent({ state: 'stale' })
  const waiting = agent({ state: 'waiting', session_id: 's2', ticket: null })
  const working = agent({ session_id: 's3', ticket: null })
  assert.deepEqual([stale, waiting, working].sort(byLead), [waiting, working, stale])
  const at = (a: LiveAgent) => new Map([['p1', [a]]])
  assert.equal(liveChanges(at(agent()), at(stale), () => 'Project'), 'No recent activity from hausv on Project.')
  assert.equal(liveChanges(at(stale), at(agent()), () => 'Project'), 'Activity resumed for hausv on Project.')
  assert.equal(liveChanges(new Map(), at(stale), () => 'Project'), '')
})

test('ticket workers match this project and ticket, and omit stopped or archived sessions', () => {
  const ticket = { id: 't1', key: 'PHAROS-14', title: 'Pill', project_id: 'p1' }
  const working = agent({ session_id: 'work', ticket })
  const waiting = agent({ session_id: 'wait', name: 'wren', needs_attention: true, since: ago(60), ticket })
  const problem = agent({ session_id: 'bad', name: 'fault', has_problem: true, ticket })
  const stale = agent({ session_id: 'quiet', name: 'quiet', activity: 'idle', heartbeat_at: ago(10 * 60), ticket })
  const heartbeatOnly = agent({ session_id: 'beat', name: 'beat', activity: 'idle', heartbeat_at: ago(20), ticket })
  const stopped = agent({ session_id: 'stop', name: 'retired', phase: 'stopped', activity: 'idle', stopped_at: ago(30), stop_reason: 'completed', ticket })
  const failedStop = agent({ session_id: 'fail', name: 'crashed', phase: 'stopped', activity: 'idle', stopped_at: ago(30), has_problem: true, stop_reason: 'error: failed', ticket })
  const archived = agent({ session_id: 'arch', name: 'old', phase: 'stopped', activity: 'idle', stopped_at: ago(30), stop_reason: 'archived_process_unknown', ticket })
  const moved = agent({ session_id: 'moved', project_id: 'p1', ticket: { ...ticket, project_id: 'p2' } })
  const otherProject = agent({ session_id: 'other', project_id: 'p2', ticket: { ...ticket, project_id: 'p2' } })
  const duplicate = agent({ session_id: 'work', ticket })
  const nameless = agent({ session_id: undefined, principal_id: undefined, name: undefined, harness: 'codex', ticket })
  const grouped = groupLive([working, waiting, problem, stale, heartbeatOnly, stopped, failedStop, archived, moved, otherProject, duplicate, nameless], now)
  assert.equal(liveState(heartbeatOnly, now), 'idle')
  assert.equal(liveState(stale, now), 'stale')
  assert.equal(liveState(problem, now), 'problem')
  assert.equal(liveState(stopped, now), 'stopped')
  assert.equal(isListedTicketWorker(grouped.get('p1')!.find(a => a.session_id === 'stop')!), false)
  assert.equal(isListedTicketWorker(grouped.get('p1')!.find(a => a.session_id === 'fail')!), false)
  assert.equal(isListedTicketWorker(grouped.get('p1')!.find(a => a.session_id === 'arch')!), false)
  const listed = ticketWorkers(grouped.get('p1')!, 'p1')
  assert.deepEqual(listed.get('t1')!.map(a => a.session_id ?? a.harness), ['bad', 'wait', 'codex', 'work', 'beat', 'quiet'])
  assert.equal(listed.get('t1')!.find(a => a.session_id === 'quiet')!.state, 'stale')
  assert.equal(listed.get('t1')!.find(a => a.session_id === 'beat')!.state, 'idle')
  assert.equal(who(listed.get('t1')!.find(a => a.harness === 'codex' && !a.session_id)!), 'Codex agent')
  assert.equal(ticketWorkers(grouped.get('p1')!, 'p2').size, 0)
  assert.equal(ticketWorkers(grouped.get('p2') ?? [], 'p1').size, 0)
  const rebound = ticketWorkers(groupLive([agent({ session_id: 'work', ticket: { id: 't2', key: 'PHAROS-11', title: 'Other', project_id: 'p1' } })], now).get('p1')!, 'p1')
  assert.equal(rebound.get('t1'), undefined)
  assert.equal(rebound.get('t2')![0]!.session_id, 'work')
})

test('shared principal keeps distinct session labels and does not invent a withheld one', () => {
  const shared = { principal_id: 'coord', name: 'aeon-coordinator' }
  const ticket = { id: 't1', key: 'AEON-233', title: 'Workers', project_id: 'p1' }
  const first = agent({ ...shared, session_id: 's-a', display_label: 'grok-ta1', ticket })
  const second = agent({ ...shared, session_id: 's-b', display_label: 'grok-ta2', since: ago(60), ticket })
  const unlabeled = agent({ ...shared, session_id: 's-c', ticket })
  const withheld = agent({ session_id: undefined, principal_id: undefined, name: undefined, display_label: undefined, harness: 'codex', ticket })
  assert.equal(who(first), 'grok-ta1')
  assert.equal(who(second), 'grok-ta2')
  assert.equal(unlabeled.display_label, undefined)
  assert.equal(who(unlabeled), 'aeon-coordinator')
  assert.equal(who(agent({ ...shared, display_label: '   ' })), 'aeon-coordinator')
  assert.equal(who(withheld), 'Codex agent')
  assert.equal(withheld.display_label, undefined)
  const listed = ticketWorkers(groupLive([first, second, unlabeled, withheld], now).get('p1')!, 'p1').get('t1')!
  assert.deepEqual(listed.map(who).sort(), ['Codex agent', 'aeon-coordinator', 'grok-ta1', 'grok-ta2'])
  assert.equal(listed.find(worker => worker.session_id === 's-c')!.display_label, undefined)
  assert.deepEqual(listed.filter(worker => worker.principal_id === 'coord').map(worker => worker.session_id).sort(), ['s-a', 's-b', 's-c'])
})


test('project pills count sessions still in progress, once, and leave stopped history in the feed', () => {
  const ticket = { id: 't1', key: 'AEON-243', title: 'Pill', project_id: 'p1' }
  const working = agent({ session_id: 'a1', name: 'one', ticket })
  const starting = agent({ session_id: 'a2', name: 'two', phase: 'starting', activity: 'unknown', since: ago(50), ticket })
  const yielded = agent({ session_id: 'a3', name: 'three', phase: 'yielded', activity: 'idle', ticket })
  const throttled = agent({ session_id: 'a4', name: 'four', activity: 'throttled', ticket })
  const overdue = agent({ session_id: 'a5', name: 'five', heartbeat_at: ago(11 * 60), ticket })
  const stopped = Array.from({ length: 20 }, (_, i) => agent({
    session_id: `stop-${i}`, name: `retired-${i}`, phase: 'stopped', activity: 'idle', stopped_at: ago(30), stop_reason: 'completed', ticket,
  }))
  const failed = agent({ session_id: 'fail', name: 'crashed', phase: 'stopped', activity: 'idle', stopped_at: ago(30), has_problem: true, stop_reason: 'error: failed', heartbeat_at: ago(5), ticket })
  const archived = agent({ session_id: 'arch', name: 'old', phase: 'stopped', activity: 'idle', stopped_at: ago(30), stop_reason: 'archived_process_unknown', ticket })
  const duplicate = agent({ session_id: 'a1', name: 'one-again', ticket })
  const idle = agent({ session_id: 'quiet', name: 'quiet', activity: 'idle', heartbeat_at: ago(20), ticket: null })
  const grouped = groupLive([working, starting, yielded, throttled, overdue, ...stopped, failed, archived, duplicate, idle], now)
  assert.equal(grouped.get('p1')!.some(a => a.session_id === 'fail'), true, 'stopped history stays available to other surfaces')
  assert.equal(isActiveSession(failed), false)
  assert.equal(isListedTicketWorker(failed), false)
  const listed = activeSessions(grouped.get('p1')!)
  assert.deepEqual(listed.map(a => a.session_id), ['a5', 'a3', 'a4', 'a1', 'a2', 'quiet'])
  assert.equal(listed.find(a => a.session_id === 'a3')!.state, 'waiting')
  assert.equal(listed.find(a => a.session_id === 'a4')!.state, 'throttled')
  assert.equal(listed.find(a => a.session_id === 'a5')!.state, 'unresponsive')
  assert.equal(listed.find(a => a.session_id === 'a2')!.state, 'working')
  assert.equal(listed.find(a => a.session_id === 'quiet')!.state, 'idle')
  assert.equal(listed.filter(a => a.session_id === 'a1').length, 1)
  const summary = liveSummary(listed.filter(a => a.ticket))
  assert.match(summary, /^5 agents:/)
  assert.doesNotMatch(summary, /5 agents needs something/)
  assert.equal(activeByProject(groupLive(stopped, now)).size, 0)
  assert.equal(activeSessions(groupLive([failed, archived], now).get('p1')!).length, 0)
  assert.equal(activeAgentLabel(1), '1 active agent')
  assert.equal(activeAgentLabel(5), '5 active agents')
  const before = activeByProject(groupLive([working, failed], now))
  const after = activeByProject(groupLive([failed], now))
  assert.equal(liveChanges(before, after, () => 'Pill'), 'No agent is working on Pill any more.')
})

test('project labels and equality retain safe attention reason changes', () => {
  const approval = agent({ state: 'waiting', needs_attention: true, attention_reasons: [{ kind: 'approval', scope: 'run', actor: 'person', count: 1, blocking: true, location: 'approvals' }] })
  assert.equal(phaseLabel(approval), 'Awaiting approval')
  const cleared = { ...approval, state: 'working' as const, needs_attention: false, attention_reasons: [] }
  assert.equal(phaseLabel(cleared), 'Working')
  assert.ok(!sameLive(new Map([['p1', [approval]]]), new Map([['p1', [cleared]]])))
  const changed = { ...approval, attention_reasons: approval.attention_reasons!.map(r => ({ ...r, count: 2 })) }
  assert.ok(!sameLive(new Map([['p1', [approval]]]), new Map([['p1', [changed]]])))
})
