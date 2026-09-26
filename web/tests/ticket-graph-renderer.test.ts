// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { filterTicketGraph, ticketGraphData, ticketLinkStyles, ticketStatusTokens } from '../src/lib/ticketGraphRenderer.ts'
import { graphLayout, graphRadius } from '../src/lib/graphRenderer.ts'
import { filtersFromQuery } from '../src/lib/ticketList.ts'
import type { TicketGraph, TicketGraphNode } from '../src/lib/ticketGraph.ts'

const node = (id: string, extra: Partial<TicketGraphNode> = {}): TicketGraphNode => ({ id, key: `TG-${id}`, title: 'Ship reliable connections', type: 'ticket', status: 'backlog', status_category: 'open', priority: null, parent_id: null, release_id: null, updated_at: '2026-09-26T12:00:00Z', link_count: 0, ...extra })
const data: TicketGraph = { nodes: [
  node('1', { type: 'epic', link_count: 8 }), node('2', { parent_id: '1', status: 'in-progress', status_category: 'doing', priority: 'high', link_count: 2 }),
  node('3', { parent_id: '2', status: 'accepted', status_category: 'done', priority: 'low', link_count: 1 }),
], links: [{ source: '1', target: '2', kind: 'parent' }, { source: '2', target: '3', kind: 'blocks' }], truncated: true }

test('adapter maps status tokens, epic ancestry, sizes and safe links without mutating API data', () => {
  const before = structuredClone(data), mapped = ticketGraphData(data, 'A & B')
  assert.deepEqual(mapped.nodes.map(n => n.color), Object.values(ticketStatusTokens))
  assert.deepEqual(mapped.nodes.map(n => n.group), ['1', '1', '1'])
  assert.equal(mapped.nodes[1].label, 'TG-2 Ship reliable connections')
  assert.equal(mapped.nodes[1].href, '/p/A%20%26%20B/TG-2')
  assert.ok(graphRadius(mapped.nodes[0]) > graphRadius(mapped.nodes[1]))
  assert.ok(graphRadius(mapped.nodes[1]) > graphRadius(mapped.nodes[2]))
  const layout = graphLayout(mapped)
  layout.nodes[0].x = 99; layout.links[0].source = layout.nodes[0]
  assert.deepEqual(data, before)
})
test('all relation kinds retain direction and distinct calm styling; dangling links are dropped', () => {
  const kinds = Object.keys(ticketLinkStyles) as (keyof typeof ticketLinkStyles)[]
  const mapped = ticketGraphData({ ...data, links: [...kinds.map(kind => ({ source: '1', target: '2', kind })), { source: 'missing', target: '1', kind: 'blocks' }] }, 'TG')
  assert.equal(mapped.links.length, 5)
  assert.equal(mapped.links[0].directed, false)
  assert.equal(mapped.links[1].directed, true)
  assert.equal(mapped.links[1].color, '--warn')
  assert.equal(new Set(mapped.links.map(l => `${l.color}:${l.width}:${l.curvature}`)).size, 5)
})
test('client filters combine AND across dimensions, OR includes, and exclusions; links never dangle', () => {
  const result = filterTicketGraph(data, filtersFromQuery({ status: 'in_progress,backlog,!backlog', priority: 'high', type: 'ticket', q: 'tg-2 reliable', closed: '1' }))
  assert.deepEqual(result.nodes.map(n => n.id), ['2'])
  assert.deepEqual(result.links, [])
  assert.equal(result.truncated, true)
  assert.deepEqual(filterTicketGraph(data, filtersFromQuery({ priority: 'none' })).nodes.map(n => n.id), ['1'])
  assert.deepEqual(filterTicketGraph(data, filtersFromQuery({ type: '!epic' })).nodes.map(n => n.id), ['2'])
})
test('Hide closed follows server categories including accepted, not only a done status', () => {
  assert.equal(filterTicketGraph(data, filtersFromQuery({})).nodes.length, 2)
  assert.equal(filterTicketGraph(data, filtersFromQuery({ closed: '1' })).nodes.length, 3)
})
test('unavailable list filters do not silently remove graph tickets; search never reads a body', () => {
  assert.equal(filterTicketGraph(data, filtersFromQuery({ assignee: 'someone', tag: 'some-label', date: 'created:today' })).nodes.length, 2)
  assert.equal(filterTicketGraph(data, filtersFromQuery({ q: 'only in a body' })).nodes.length, 0)
})
test('filtered or cyclic parents are safe and an epic remains larger than a busy ordinary ticket', () => {
  const nodes = [node('1', { parent_id: '2' }), node('2', { parent_id: '1' }), node('3', { type: 'epic' }), node('4', { link_count: 2000 })]
  const mapped = ticketGraphData({ nodes, links: [], truncated: false }, 'TG')
  assert.equal(mapped.nodes[0].group, 'unparented')
  assert.ok(graphRadius(mapped.nodes[2]) > graphRadius(mapped.nodes[3]))
})
