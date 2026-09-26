// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { advanceActivity, liveState, agentKey, byLead, chipText, elapsedFor, groupLive, liveChanges, liveSummary, phrase, sameLive, skewOf, who, type LiveAgent } from '../src/lib/liveAgents.ts'

const now = Date.parse('2026-09-26T12:00:00Z')
const ago = (seconds: number) => new Date(now - seconds * 1000).toISOString()
function agent(fields: Partial<LiveAgent> = {}): LiveAgent {
  return {
    project_id: 'p1', session_id: 's1', principal_id: 'a1', name: 'hausv', harness: 'claude', management_mode: 'unmanaged', role: 'worker',
    phase: 'working', activity: 'busy', ticket: { id: 't1', key: 'HAUSV-887', title: 'Statements', project_id: 'p1' }, since: ago(16 * 60), heartbeat_at: ago(30), ...fields,
  }
}

test('groupLive marks stale heartbeats on the server clock and puts the lead first', () => {
  const coordinator = agent({ session_id: 's2', name: 'aeon-coordinator', role: 'coordinator', ticket: null, since: ago(3600) })
  const idleTicketless = agent({ session_id: 's3', name: 'scout', ticket: null, since: ago(60) })
  const early = agent({ session_id: 's4', name: 'early', since: ago(7200) })
  const stale = agent({ session_id: 's5', project_id: 'p2', heartbeat_at: ago(121) })
  const grouped = groupLive([coordinator, idleTicketless, agent(), early, stale], now)
  assert.deepEqual([...grouped.keys()], ['p1', 'p2'])
  assert.deepEqual(grouped.get('p1')!.map(a => a.session_id), ['s4', 's1', 's3', 's2'])
  assert.equal(groupLive([stale], now).get('p2')![0]!.state, 'stale')
  assert.equal(groupLive([stale], now - 2000).get('p2')![0]!.state, 'working', 'a server clock two seconds earlier still sees it fresh')
  assert.equal(groupLive([agent({ heartbeat_at: 'not a time' })], now).get('p1')![0]!.state, 'stale')
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
  assert.equal(phrase(agent({ phase: 'starting', ticket: null })), 'hausv, starting')
  assert.equal(phrase(agent({ phase: 'stopping' })), 'hausv, stopping on HAUSV-887')
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
  assert.ok(!sameLive(a, new Map([['p1', [agent({ activity_sequence: 2 })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent({ state: 'stale' })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent({ ticket: { id: 't2', key: 'HAUSV-888', title: 'Other', project_id: 'p1' } })]]])))
  assert.ok(!sameLive(a, new Map([['p1', [agent(), agent({ session_id: 's2' })]]])))
  assert.ok(!sameLive(a, new Map([['p2', [agent()]]])))
  assert.ok(!sameLive(a, new Map()))
})


test('waiting is explicit evidence and stale overrides every claimed state', () => {
  assert.equal(liveState(agent({ activity: 'unknown', phase: 'starting' }), now), 'working')
  assert.equal(liveState(agent({ state: 'waiting' }), now), 'waiting')
  assert.equal(liveState(agent({ state: 'waiting', heartbeat_at: ago(121) }), now), 'stale')
  assert.equal(liveState(agent({ heartbeat_at: ago(20) }), now, 10_000), 'stale')
  assert.equal(liveSummary([agent({ state: 'waiting' })]), '1 agent: hausv, waiting for approval on HAUSV-887')
  assert.equal(liveSummary([agent({ state: 'stale' })]), '1 agent: hausv, no recent activity on HAUSV-887')
})

test('activity pulses only on an observed advance, with monotonic watermarks', () => {
  const first = advanceActivity(undefined, agent({ activity_sequence: 4 }))
  assert.equal(first.pulse, 0, 'initial data is a baseline')
  assert.deepEqual(advanceActivity(first, agent({ activity_sequence: 4 })), first)
  const heartbeat = advanceActivity(first, agent({ activity_sequence: 4, heartbeat_at: ago(20) }))
  assert.equal(heartbeat.pulse, 1, 'heartbeat is real evidence even without a sequence change')
  const sequence = advanceActivity(heartbeat, agent({ activity_sequence: 5, heartbeat_at: ago(20) }))
  assert.equal(sequence.pulse, 2)
  const both = advanceActivity(sequence, agent({ activity_sequence: 6, heartbeat_at: ago(10) }))
  assert.equal(both.pulse, 3, 'coincident updates produce one pulse')
  assert.deepEqual(advanceActivity(both, agent({ activity_sequence: 4 })), both, 'old response cannot roll back evidence')
  assert.deepEqual(advanceActivity(both, agent({ activity_sequence: 6, heartbeat_at: ago(10) })), both, 'replayed evidence stays quiet')
  assert.deepEqual(advanceActivity(both, agent({ activity_sequence: NaN, heartbeat_at: 'invalid' })), both)
  const unknown = advanceActivity(undefined, agent({ heartbeat_at: 'invalid' }))
  assert.equal(advanceActivity(unknown, agent({ activity_sequence: 6 })).pulse, 0, 'first valid evidence establishes a baseline')
})

test('stale evidence neither leads current work nor claims that a session stopped', () => {
  const stale = agent({ state: 'stale' })
  const waiting = agent({ state: 'waiting', session_id: 's2', ticket: null })
  const working = agent({ session_id: 's3', ticket: null })
  assert.deepEqual([stale, waiting, working].sort(byLead), [working, waiting, stale])
  const at = (a: LiveAgent) => new Map([['p1', [a]]])
  assert.equal(liveChanges(at(agent()), at(stale), () => 'Project'), 'No recent activity from hausv on Project.')
  assert.equal(liveChanges(at(stale), at(agent()), () => 'Project'), 'Activity resumed for hausv on Project.')
  assert.equal(liveChanges(new Map(), at(stale), () => 'Project'), '')
})
