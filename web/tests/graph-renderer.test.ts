// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { graphLayout, labelPosition, type GraphData } from '../src/lib/graphRenderer.ts'

test('a ticket adapter needs no knowledge schema and engines cannot mutate the source', () => {
  const data: GraphData = { nodes: [
    { id: 'ticket-a', label: 'Ship the graph', group: 'doing', color: '--teal', weight: 2, href: '/p/AEON/AEON-194' },
    { id: 'ticket-b', label: 'Reuse it', group: 'backlog', color: '--ink-3', weight: 1 },
  ], links: [{ source: 'ticket-a', target: 'ticket-b', kind: 'blocks', directed: true }] }
  const copy = graphLayout(data)
  assert.equal(copy.nodes[0].degree, 1)
  assert.equal(copy.nodes[0].href, '/p/AEON/AEON-194')
  copy.nodes[0].x = 42; copy.links[0].source = copy.nodes[0]
  assert.equal('x' in data.nodes[0], false)
  assert.equal(data.links[0].source, 'ticket-a')
})

test('smart labels try alternative positions and reject a fully occupied screen', () => {
  const first = labelPosition(100, 100, 12, 80, 20, [], 300, 300, false)!
  const second = labelPosition(100, 100, 12, 80, 20, [first], 300, 300, false)!
  assert.ok(first.y > 100)
  assert.ok(second.y < 100)
  const full = [{ x: 150, y: 150, w: 300, h: 300 }]
  assert.equal(labelPosition(100, 100, 12, 80, 20, full, 300, 300, false), undefined)
  assert.ok(labelPosition(100, 100, 12, 80, 20, full, 300, 300, true))
})

test('mandatory labels clamp to the viewport edge', () => {
  const box = labelPosition(4, 4, 40, 100, 20, [], 300, 300, true)!
  assert.ok(box.x - box.w / 2 >= 0 && box.y - box.h / 2 >= 0)
  assert.ok(box.x + box.w / 2 <= 300 && box.y + box.h / 2 <= 300)
})
