// SPDX-License-Identifier: AGPL-3.0-only
// AEON-291: Lost contact is an ended session; menus offer only what works.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { SessionView } from '../src/stores/agents.ts'
import { assessAgentState, problemReason } from '../src/lib/agentSignals.ts'
import { stopReasonLabel } from '../src/lib/agentState.ts'
import { quickRemoval, removalConsequence, sessionMenu } from '../src/components/agents/sessionActions.ts'

const now = Date.parse('2026-09-29T09:00:00Z')
const ago = (ms: number) => new Date(now - ms).toISOString()

function view(session: Partial<SessionView['session']> = {}, state: SessionView['status']['state'] = 'working', ticket = true): SessionView {
  const s = {
    id: 's1', project_id: 'p1', harness: 'cursor', host: 'mba', management_mode: 'unmanaged', advertised_capabilities: [],
    phase: 'working', activity: 'busy', heartbeat_at: ago(30_000), stopped_at: null, stop_reason: null, archived_at: null,
    ...session,
  } as SessionView['session']
  return {
    session: s, status: { group: state === 'stopped' ? 'stopped' : 'working', tone: 'busy', label: '', state },
    name: 'cursor-275', harness: 'Cursor', account: '', model: '', projectKey: 'AEON', projectTitle: 'Aeon',
    ticket: ticket ? { id: 'n1', key: 'AEON-275', title: 'Worker', href: '/p/AEON/AEON-275' } : null,
  }
}
const access = { canControl: true, canRemove: true, product: 'Aeon', now }

test('Lost contact is an ended session, never a problem', () => {
  assert.equal(problemReason('heartbeat_lost'), false)
  assert.equal(problemReason('ownership_lost'), true)
  const lost = assessAgentState({ phase: 'stopped', activity: 'busy', stopped_at: ago(0), stop_reason: 'heartbeat_lost' }, now)
  assert.deepEqual([lost.state, lost.label], ['stopped', 'Lost contact'])
  assert.equal(assessAgentState({ phase: 'stopped', activity: 'busy', stopped_at: ago(0), stop_reason: 'process_exited' }, now).label, 'Stopped')
  assert.equal(stopReasonLabel('heartbeat_lost'), 'Lost contact')
})

test('an unmanaged session offers Remove, Open ticket and Copy id, with one quiet line while it runs', () => {
  const menu = sessionMenu(view(), access)
  assert.deepEqual(menu.control, [])
  assert.deepEqual(menu.other, ['ticket', 'copy'])
  assert.equal(menu.remove, true)
  assert.equal(menu.note, 'Runs outside Aeon — stop it in its terminal')
  const ended = sessionMenu(view({ phase: 'stopped', stopped_at: ago(0) }, 'stopped', false), access)
  assert.deepEqual([ended.other, ended.note], [['copy'], ''])
})

test('a managed session offers Interrupt, Stop and settings only when they work', () => {
  // Legacy managed sessions use the typed /controls route.
  const caps = ['interrupt', 'stop']
  assert.deepEqual(sessionMenu(view({ management_mode: 'managed', advertised_capabilities: caps }), access).control, ['interrupt', 'stop'])
  assert.deepEqual(sessionMenu(view({ management_mode: 'managed', advertised_capabilities: ['stop'] }), access).control, ['stop'])
  assert.deepEqual(sessionMenu(view({ management_mode: 'managed', advertised_capabilities: caps }), { ...access, pending: { state: 'claimed' } }).control, [])
  assert.deepEqual(sessionMenu(view({ management_mode: 'managed', advertised_capabilities: caps }), { ...access, canControl: false }).control, [])
  assert.deepEqual(sessionMenu(view({ management_mode: 'managed', advertised_capabilities: caps, phase: 'stopped', stopped_at: ago(0) }, 'stopped'), access).control, [])
  assert.equal(sessionMenu(view({ management_mode: 'managed' }), access).note, '')
  assert.equal(sessionMenu(view(), { ...access, canRemove: false }).remove, false)
  assert.equal(sessionMenu(view({ archived_at: ago(0), phase: 'stopped', stopped_at: ago(0) }, 'stopped'), access).remove, false)
})

test('managed_control_v1 offers controls only when the server would accept them', () => {
  const ownership = { daemon_id: 'd', generation: 'g', process_id: 'p', root_pid: 9, group_id: 9, started_at: ago(60_000) }
  const caps = ['interrupt', 'stop', 'managed_control_v1', 'rename']
  const fresh = view({ harness: 'claude', management_mode: 'managed', run_id: 'r1', run_status: 'running', advertised_capabilities: caps, process_ownership: ownership, process_observed_at: ago(10_000) })
  assert.deepEqual(sessionMenu(fresh, access).control, ['interrupt', 'stop', 'settings'])
  // Settings go through the same route, so they share every prerequisite.
  const stale = view({ ...fresh.session, process_observed_at: ago(60_000) })
  assert.deepEqual(sessionMenu(stale, access).control, [])
  const failed = view({ ...fresh.session, run_status: 'failed' })
  assert.deepEqual(sessionMenu(failed, access).control, [])
  const noStop = view({ ...fresh.session, advertised_capabilities: ['interrupt', 'managed_control_v1', 'rename'] })
  assert.deepEqual(sessionMenu(noStop, access).control, [])
  const noInterrupt = view({ ...fresh.session, advertised_capabilities: ['stop', 'managed_control_v1'] })
  assert.deepEqual(sessionMenu(noInterrupt, access).control, ['stop'])
  const other = view({ ...fresh.session, harness: 'codex' })
  assert.deepEqual(sessionMenu(other, access).control, [])
  // The run read fills in a status the session read does not carry.
  const fromRun = { ...view({ ...fresh.session, run_status: undefined }), run: { status: 'running' } as SessionView['run'] }
  assert.deepEqual(sessionMenu(fromRun, access).control, ['interrupt', 'stop', 'settings'])
})

test('No heartbeat, Lost contact and stopped rows remove in one click; live rows ask', () => {
  assert.equal(quickRemoval(view({}, 'unresponsive')), true)
  assert.equal(quickRemoval(view({ phase: 'stopped', stopped_at: ago(0), stop_reason: 'heartbeat_lost' }, 'stopped')), true)
  assert.equal(quickRemoval(view({}, 'working')), false)
  assert.equal(quickRemoval(view({}, 'awaiting')), false)
  assert.equal(quickRemoval(view({ phase: 'stopped', stopped_at: ago(0), archived_at: ago(0) }, 'stopped')), false)
})

test('"the process keeps running" is said only with a fresh heartbeat', () => {
  assert.match(removalConsequence(view().session, now), /keeps running/)
  assert.equal(removalConsequence(view({ heartbeat_at: ago(4 * 60_000) }).session, now), '')
  assert.equal(removalConsequence(view({ heartbeat_at: null }).session, now), '')
  assert.equal(removalConsequence(view({ phase: 'stopped', stopped_at: ago(0) }).session, now), '')
})
