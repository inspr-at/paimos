// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { sessionForest, sessionStatus } from '../src/lib/agentState'
import type { HarnessSession } from '../src/lib/agents'
import type { SessionView } from '../src/stores/agents'
import { nextSort, orderForest, readSort, sortStorageKey, writeSort, type SessionSort } from '../src/components/agents/sessionOrder'

const now = Date.parse('2026-09-30T12:00:00Z')
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
function session(fields: Partial<HarnessSession> = {}): HarnessSession {
  return {
    id: 's1', project_id: 'p1', agent_principal_id: 'a1', run_id: 'r1', ticket_node_id: null, work_order_id: null, parent_harness_session_id: null,
    harness: 'claude', host: 'imac0', management_mode: 'managed', role: 'worker', work_shape: 'ship', advertised_capabilities: ['interrupt', 'stop'],
    phase: 'working', activity: 'busy', activity_sequence: 1, revision: 1, heartbeat_at: ago(0.5), stopped_at: null, stop_reason: null, created_at: ago(90), ...fields,
  }
}
type Extra = { ticket?: string; model?: string; brief?: string }
function view(fields: Partial<HarnessSession> & Extra = {}, at = now): SessionView {
  const { ticket, model, brief, ...rest } = fields
  const s = session({ brief: brief ?? `Result ${rest.id}`, model: model ?? null, ...rest })
  return {
    session: s, status: sessionStatus(s, at), name: s.id, harness: 'Claude', account: '', model: '', projectKey: 'AEON', projectTitle: 'Aeon',
    ticket: ticket ? { id: `n-${ticket}`, key: ticket, title: ticket, href: `/p/AEON/${ticket}` } : null,
  }
}
const ids = (branches: { view: SessionView }[]) => branches.map(b => b.view.session.id)
const order = (views: SessionView[], sort: SessionSort | null, at = now) => orderForest(sessionForest(views, at), sort, at)
// Deterministic shuffle so a failure reproduces.
function shuffled<T>(items: T[], seed: number) {
  const out = [...items]
  for (let i = out.length - 1; i > 0; i--) { seed = (seed * 9301 + 49297) % 233280; const j = seed % (i + 1); [out[i], out[j]] = [out[j]!, out[i]!] }
  return out
}

describe('default order: state, then start time, then id', () => {
  const fleet = (beats: (id: string) => string) => [
    view({ id: 'w-late', created_at: ago(10), heartbeat_at: beats('w-late') }),
    view({ id: 'w-early', created_at: ago(80), heartbeat_at: beats('w-early') }),
    view({ id: 'w-tie-b', created_at: ago(40), heartbeat_at: beats('w-tie-b') }),
    view({ id: 'w-tie-a', created_at: ago(40), heartbeat_at: beats('w-tie-a') }),
    view({ id: 'idle', activity: 'idle', created_at: ago(120), heartbeat_at: beats('idle') }),
    view({ id: 'lead', role: 'coordinator', created_at: ago(200), heartbeat_at: beats('lead') }),
    view({ id: 'kid-2', parent_harness_session_id: 'lead', created_at: ago(20), heartbeat_at: beats('kid-2') }),
    view({ id: 'kid-1', parent_harness_session_id: 'lead', created_at: ago(30), heartbeat_at: beats('kid-1') }),
    view({ id: 'problem', has_problem: true, created_at: ago(5), heartbeat_at: beats('problem') }),
  ]

  it('never changes while heartbeats arrive in any order and states stay the same', () => {
    const first = order(fleet(() => ago(0.2)), null)
    expect(ids(first)).toEqual(['problem', 'lead', 'w-early', 'w-tie-a', 'w-tie-b', 'w-late', 'idle'])
    expect(ids(first[1]!.children)).toEqual(['kid-1', 'kid-2'])
    for (let round = 1; round <= 40; round++) {
      const beat = (id: string) => ago(((round * 31 + id.length * 17 + id.charCodeAt(0)) % 50) / 60)
      const tree = order(shuffled(fleet(beat), round), null)
      expect(ids(tree)).toEqual(ids(first))
      expect(ids(tree[1]!.children)).toEqual(['kid-1', 'kid-2'])
    }
  })

  it('moves a row only when its state changes, into its new group', () => {
    const views = fleet(() => ago(0.2))
    views[1] = view({ id: 'w-early', created_at: ago(80), heartbeat_at: ago(12) }) // silent: No heartbeat
    expect(ids(order(views, null))).toEqual(['problem', 'w-early', 'lead', 'w-tie-a', 'w-tie-b', 'w-late', 'idle'])
  })

  it('lists ended families by their latest stop and History by its latest end', () => {
    const views = [
      view({ id: 'stop-old', phase: 'stopped', stopped_at: ago(50), created_at: ago(60) }),
      view({ id: 'stop-new', phase: 'stopped', stopped_at: ago(5), created_at: ago(300) }),
      view({ id: 'removed', phase: 'stopped', stopped_at: ago(90), archived_at: ago(1), created_at: ago(100) }),
    ]
    expect(ids(order(views.slice(0, 2), null))).toEqual(['stop-new', 'stop-old'])
    expect(ids(orderForest(sessionForest(views, now), null, now, true))).toEqual(['removed', 'stop-new', 'stop-old'])
  })
})

describe('manual order by column', () => {
  const sorted = (views: SessionView[], key: SessionSort['key'], dir: SessionSort['dir'], at = now) => ids(order(views, { key, dir }, at))

  it('Intended result: natural text order, ties by start then id', () => {
    const views = [view({ id: 'b', brief: 'Ship step 10' }), view({ id: 'a', brief: 'ship step 2' }), view({ id: 'c', brief: 'Audit logs' }), view({ id: 'd', brief: 'Audit logs' })]
    expect(sorted(views, 'result', 'asc')).toEqual(['c', 'd', 'a', 'b'])
    expect(sorted(views, 'result', 'desc')).toEqual(['b', 'a', 'c', 'd'])
  })

  it('Ticket: key order with numbers, sessions without a ticket last either way', () => {
    const views = [view({ id: 'none', ticket: undefined }), view({ id: 't10', ticket: 'AEON-10' }), view({ id: 't9', ticket: 'AEON-9' }), view({ id: 'x', ticket: 'AEON-9' })]
    expect(sorted(views, 'ticket', 'asc')).toEqual(['t9', 'x', 't10', 'none'])
    expect(sorted(views, 'ticket', 'desc')).toEqual(['t10', 't9', 'x', 'none'])
  })

  it('Execution: model line, models unknown last, ties by id', () => {
    const views = [view({ id: 'b', model: 'gpt-6.1-sol' }), view({ id: 'a', model: 'claude-opus-5-5' }), view({ id: 'c', model: 'claude-opus-5-5' }), view({ id: 'none' })]
    expect(sorted(views, 'execution', 'asc')).toEqual(['a', 'c', 'b', 'none'])
    expect(sorted(views, 'execution', 'desc')).toEqual(['b', 'a', 'c', 'none'])
  })

  it('Heartbeat: on-time beats tie and keep start order; silence and never sort after', () => {
    const views = [
      view({ id: 'fresh-late', heartbeat_at: ago(0.1), created_at: ago(5) }), view({ id: 'fresh-early', heartbeat_at: ago(0.9), created_at: ago(50) }),
      view({ id: 'silent', heartbeat_at: ago(20) }), view({ id: 'never', heartbeat_at: null, created_at: ago(30) }), view({ id: 'quiet', heartbeat_at: ago(6) }),
    ]
    expect(sorted(views, 'heartbeat', 'asc')).toEqual(['fresh-early', 'fresh-late', 'quiet', 'silent', 'never'])
    expect(sorted(views, 'heartbeat', 'desc')).toEqual(['never', 'silent', 'quiet', 'fresh-early', 'fresh-late'])
    // A new beat on either fresh row never swaps them.
    const beaten = views.map(v => v.session.id === 'fresh-early' ? view({ id: 'fresh-early', heartbeat_at: ago(0), created_at: ago(50) }) : v)
    expect(sorted(beaten, 'heartbeat', 'asc')).toEqual(['fresh-early', 'fresh-late', 'quiet', 'silent', 'never'])
  })

  it('Running: shortest first ascending, ties by id', () => {
    const views = [view({ id: 'long', created_at: ago(300) }), view({ id: 'b', created_at: ago(20) }), view({ id: 'a', created_at: ago(20) }), view({ id: 'mid', created_at: ago(90) })]
    expect(sorted(views, 'running', 'asc')).toEqual(['a', 'b', 'mid', 'long'])
    expect(sorted(views, 'running', 'desc')).toEqual(['long', 'mid', 'a', 'b'])
  })

  it('State: descending reverses the groups, ties by start then id', () => {
    const views = [view({ id: 'p', has_problem: true }), view({ id: 'w2' }), view({ id: 'w1' }), view({ id: 'i', activity: 'idle' })]
    expect(sorted(views, 'state', 'desc')).toEqual(['i', 'w1', 'w2', 'p'])
  })

  it('keeps families together and orders children by the same key', () => {
    const views = [
      view({ id: 'lead', role: 'coordinator', ticket: 'AEON-5' }), view({ id: 'solo', ticket: 'AEON-7' }),
      view({ id: 'k1', parent_harness_session_id: 'lead', ticket: 'AEON-30' }), view({ id: 'k2', parent_harness_session_id: 'lead', ticket: 'AEON-4' }),
    ]
    const desc = order(views, { key: 'ticket', dir: 'desc' })
    expect(ids(desc)).toEqual(['solo', 'lead'])
    expect(ids(desc[1]!.children)).toEqual(['k1', 'k2'])
    expect(ids(order(views, { key: 'ticket', dir: 'asc' })[0]!.children)).toEqual(['k2', 'k1'])
  })
})

describe('choosing, remembering and resetting the order', () => {
  it('toggles a column and treats State ascending as the default', () => {
    expect(nextSort(null, 'ticket')).toEqual({ key: 'ticket', dir: 'asc' })
    expect(nextSort({ key: 'ticket', dir: 'asc' }, 'ticket')).toEqual({ key: 'ticket', dir: 'desc' })
    expect(nextSort({ key: 'ticket', dir: 'desc' }, 'ticket')).toEqual({ key: 'ticket', dir: 'asc' })
    expect(nextSort({ key: 'ticket', dir: 'desc' }, 'running')).toEqual({ key: 'running', dir: 'asc' })
    expect(nextSort(null, 'state')).toEqual({ key: 'state', dir: 'desc' })
    expect(nextSort({ key: 'state', dir: 'desc' }, 'state')).toBeNull()
  })

  it('round-trips per viewer, resets, and ignores unusable storage', () => {
    const map = new Map<string, string>()
    const storage = { getItem: (k: string) => map.get(k) ?? null, setItem: (k: string, v: string) => { map.set(k, v) }, removeItem: (k: string) => { map.delete(k) } }
    writeSort('t.anna', { key: 'heartbeat', dir: 'desc' }, storage)
    expect(readSort('t.anna', storage)).toEqual({ key: 'heartbeat', dir: 'desc' })
    expect(readSort('t.ben', storage)).toBeNull()
    writeSort('t.anna', null, storage)
    expect(map.has(sortStorageKey('t.anna'))).toBe(false)
    expect(readSort('t.anna', storage)).toBeNull()
    for (const bad of ['{', '{"key":"nope","dir":"asc"}', '{"key":"ticket","dir":"up"}', '{"key":"state","dir":"asc"}', '7']) {
      map.set(sortStorageKey('t.anna'), bad)
      expect(readSort('t.anna', storage)).toBeNull()
    }
    const broken = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') }, removeItem: () => { throw new Error('blocked') } }
    expect(readSort('t.anna', broken)).toBeNull()
    expect(() => writeSort('t.anna', { key: 'ticket', dir: 'asc' }, broken)).not.toThrow()
    expect(() => writeSort('', { key: 'ticket', dir: 'asc' }, storage)).not.toThrow()
    expect(readSort('', storage)).toBeNull()
  })
})
