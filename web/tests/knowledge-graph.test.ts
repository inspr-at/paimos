// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { filterGraph, graphLayout, graphMatches, type KnowledgeGraphData, type GraphNode } from '../src/lib/knowledgeGraph.ts'

const node = (id: string, type: GraphNode['type']): GraphNode => ({ id, type, kind: type === 'ticket' ? 'ticket' : 'knowledge', key: `TEST-${id}`, slug: `entry-${id}`, title: `Entry ${id}`, status: 'active', degree: 1, updated_at: '2026-09-25T12:00:00Z' })
const data: KnowledgeGraphData = { nodes: [node('a', 'runbook'), node('b', 'memory'), node('c', 'ticket'), node('d', 'ticket')], edges: [
  { source: 'a', target: 'b', kind: 'mention', label: 'mentions' },
  { source: 'c', target: 'a', kind: 'relation', label: 'cites' },
  { source: 'b', target: 'd', kind: 'mention', label: 'mentions' },
], truncated: false }
test('type filtering keeps only directly attached ticket satellites and valid edges', () => {
  const filtered = filterGraph(data, 'runbook')
  assert.deepEqual(filtered.nodes.map(n => n.id), ['a', 'c'])
  assert.deepEqual(filtered.edges, [data.edges[1]])
})
test('search highlights matches without changing graph topology', () => {
  assert.deepEqual([...graphMatches(data.nodes, 'ENTRY A')], ['a'])
  assert.deepEqual([...graphMatches(data.nodes, 'test-b')], ['b'])
  assert.deepEqual([...graphMatches(data.nodes, 'unknown')], [])
  assert.equal(data.edges.length, 3)
})
test('force-engine mutation cannot corrupt cached API data', () => {
  const layout = graphLayout(data)
  layout.nodes[0].x = 100
  layout.links[0].source = layout.nodes[0]
  assert.equal('x' in data.nodes[0], false)
  assert.equal(data.edges[0].source, 'a')
})
