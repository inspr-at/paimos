// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { actionResults, assemble, keyQuery, projectResults, recentResults, ticketResults, type ActionResult } from '../src/lib/palette.ts'
import type { ListItem, WorkNode } from '../src/lib/api.ts'

function listed(key: string, title: string, kind = 'ticket'): ListItem {
  return { id: key, key, kind_id: `k-${kind}`, title, body: '', fields: {}, state: 'backlog', parent_id: null, position: '0', created_at: '', updated_at: '', kind_slug: kind, kind_label: kind, priority: null, assignee: null, parent: null, children_count: 0, project: null }
}
function hit(key: string, title: string, kind = 'ticket', extra: Partial<WorkNode> = {}): WorkNode {
  return { id: key, key, kind_id: `k-${kind}`, title, body: '', fields: {}, state: 'new', parent_id: null, position: '0', created_at: '', updated_at: '', ...extra } as WorkNode
}
const work = new Map([['k-ticket', 'ticket'], ['k-task', 'task'], ['k-epic', 'epic']])
const projectFor = (key: string) => ['PHAROS', 'AEON'].includes(key.split('-')[0]) ? key.split('-')[0] : null
const projects = [
  { id: 'p1', routeKey: 'PHAROS', title: 'Pharos', description: 'Fleet management', archived: false },
  { id: 'p2', routeKey: 'AEON', title: 'Aeon', description: 'Successor of Paimos', archived: false },
  { id: 'p3', routeKey: 'GLINT', title: 'Glint', description: 'Glanceable context', archived: true },
]
const actions: ActionResult[] = [
  { type: 'action', id: 'new-ticket', label: 'New ticket in PHAROS', hint: 'Pharos', icon: 'plus' },
  { type: 'action', id: 'theme', label: 'Switch to dark theme', icon: 'moon' },
]

test('key queries recognise a project key with an optional number', () => {
  assert.deepEqual(keyQuery('pharos-29'), { prefix: 'PHAROS', number: '29', exact: true })
  assert.deepEqual(keyQuery('PHAROS-'), { prefix: 'PHAROS', number: '', exact: false })
  assert.equal(keyQuery('pharos'), null)
  assert.equal(keyQuery('oracle cloud'), null)
  assert.equal(keyQuery('a-1'), null)
})

test('tickets: list matches first, then work search hits once, exact key leads', () => {
  const out = ticketResults('PHAROS-12', [listed('PHAROS-120', 'Later'), listed('PHAROS-12', 'Exact')], [hit('PHAROS-12', 'Exact'), hit('PHAROS-7', 'Meaning')], work, projectFor, null)
  assert.deepEqual(out.map(r => r.key), ['PHAROS-12', 'PHAROS-120', 'PHAROS-7'])
  assert.equal(out[0].projectKey, 'PHAROS')
})

test('tickets: search hits skip non-work kinds, deleted nodes and other projects in a scope', () => {
  const hits = [hit('PHAROS-1', 'Ticket'), hit('PRJ-1', 'Project', 'project'), hit('PHAROS-2', 'Gone', 'ticket', { deleted_at: '2026-09-01T00:00:00Z' }), hit('AEON-3', 'Elsewhere')]
  assert.deepEqual(ticketResults('word', [], hits, work, projectFor, null).map(r => r.key), ['PHAROS-1', 'AEON-3'])
  assert.deepEqual(ticketResults('word', [], hits, work, projectFor, 'PHAROS').map(r => r.key), ['PHAROS-1'])
})

test('tickets are capped', () => {
  const many = Array.from({ length: 12 }, (_, i) => listed(`AEON-${i + 1}`, 'x'))
  assert.equal(ticketResults('x', many, [], work, projectFor, null).length, 8)
})

test('projects: exact key first, archived projects only on an exact key', () => {
  assert.deepEqual(projectResults('aeon', projects).map(p => p.key), ['AEON'])
  assert.deepEqual(projectResults('glint', projects).map(p => p.key), ['GLINT'])
  assert.deepEqual(projectResults('glanceable', projects), [])
  assert.deepEqual(projectResults('', projects), [])
  assert.deepEqual(projectResults('fleet', projects).map(p => p.key), ['PHAROS'])
})

test('actions match every word of the query', () => {
  assert.deepEqual(actionResults('', actions).length, 2)
  assert.deepEqual(actionResults('new pharos', actions).map(a => a.id), ['new-ticket'])
  assert.deepEqual(actionResults('dark', actions).map(a => a.id), ['theme'])
})

test('recents follow the scope', () => {
  const recents = [
    { type: 'ticket' as const, key: 'PHAROS-12', title: 'Oracle', state: 'backlog', kind: 'ticket', projectKey: 'PHAROS' },
    { type: 'project' as const, key: 'AEON', title: 'Aeon' },
    { type: 'project' as const, key: 'PHAROS', title: 'Pharos' },
  ]
  assert.deepEqual(recentResults(recents, null).map(r => r.id), ['recent-PHAROS-12', 'recent-AEON', 'recent-PHAROS'])
  assert.deepEqual(recentResults(recents, 'PHAROS').map(r => r.id), ['recent-PHAROS-12', 'recent-PHAROS'])
})

test('groups: empty shows Recent and Actions; a bare project key puts Projects first', () => {
  const tickets = ticketResults('aeon', [listed('AEON-1', 'Aeon foundation')], [], work, projectFor, null)
  const recent = recentResults([{ type: 'project', key: 'AEON', title: 'Aeon' }], null)
  assert.deepEqual(assemble('', { recent, tickets, projects: [], actions }).map(g => g.id), ['recent', 'actions'])
  assert.deepEqual(assemble('', { recent: [], tickets, projects: [], actions }).map(g => g.id), ['actions'])
  assert.deepEqual(assemble('aeon', { recent, tickets, projects: projectResults('aeon', projects), actions: [] }).map(g => g.id), ['projects', 'tickets'])
  assert.deepEqual(assemble('foundation', { recent, tickets, projects: projectResults('foundation', projects), actions: actionResults('foundation', actions) }).map(g => g.id), ['tickets'])
})


test('canonical work survives palette exact-key and title results with scope and deduplication', () => {
  const canonicalKinds = new Map([...work, ['k-work', 'work']])
  assert.deepEqual(ticketResults('PHAROS-12', [listed('PHAROS-12', 'Exact', 'work')], [], canonicalKinds, projectFor, null).map(r => r.key), ['PHAROS-12'])
  const out = ticketResults('connector', [listed('PHAROS-12', 'Connector', 'work')], [hit('PHAROS-12', 'Connector', 'work'), hit('PHAROS-7', 'Connector meaning', 'work'), hit('AEON-3', 'Foreign project', 'work'), hit('PHAROS-9', 'Gone', 'work', { deleted_at: '2026-10-01' })], canonicalKinds, projectFor, 'PHAROS')
  assert.deepEqual(out.map(r => r.key), ['PHAROS-12', 'PHAROS-7'])
  assert.equal(out[0].kind, 'work')
})
