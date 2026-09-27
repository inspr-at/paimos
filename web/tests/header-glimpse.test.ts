// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { glimpseControlSpot, glimpseOverlaps, glimpseTextMask, headerGlimpseAllowed, headerGlimpseGraphReady } from '../src/lib/headerGlimpse.ts'

const on = { enabled: true, reduced: false, wide: true, tickets: 8 }

test('the header glimpse needs a wide viewport, motion, and eight tickets', () => {
  assert.equal(headerGlimpseAllowed(on), true)
  assert.equal(headerGlimpseAllowed({ ...on, enabled: false }), false)
  assert.equal(headerGlimpseAllowed({ ...on, reduced: true }), false)
  assert.equal(headerGlimpseAllowed({ ...on, wide: false }), false)
  assert.equal(headerGlimpseAllowed({ ...on, tickets: 7 }), false)
})

test('a ready graph has eight tickets and at least one link', () => {
  assert.equal(headerGlimpseGraphReady(8, 1), true)
  assert.equal(headerGlimpseGraphReady(8, 0), false)
  assert.equal(headerGlimpseGraphReady(7, 4), false)
})

test('hover controls find room around text, including a long title and a crowded saved view strip', () => {
  const boxes = [
    { x: 0, y: 20, width: 800, height: 85 },
    { x: 820, y: 20, width: 280, height: 85 },
    { x: 0, y: 130, width: 450, height: 36 },
    { x: 0, y: 180, width: 1100, height: 36 },
  ]
  const spot = glimpseControlSpot(1100, 224, boxes, { width: 310, height: 34 })
  assert.ok(spot)
  assert.ok(boxes.every(box => !glimpseOverlaps(spot, box, 10)))
  assert.equal(glimpseControlSpot(250, 100, boxes, { width: 310, height: 34 }), null)
})

test('text exclusion masks are bounded SVG images with hard and feathered cutouts', () => {
  const mask = decodeURIComponent(glimpseTextMask(1100, 224, [{ x: 20, y: 30, width: 200, height: 40 }]))
  assert.match(mask, /viewBox="0 0 1100 224"/)
  assert.match(mask, /feGaussianBlur/)
  assert.equal(mask.match(/x="14" y="24" width="212" height="52"/g)?.length, 2)
})

test('the shared context pages the Tickets query and drops links outside its membership', async () => {
  const { loadTicketGraphContext } = await import('../src/lib/headerGlimpse.ts')
  const { filtersFromQuery } = await import('../src/lib/ticketList.ts')
  const original = globalThis.fetch, calls: URL[] = []
  globalThis.fetch = async url => {
    const parsed = new URL(String(url), 'http://local.test'); calls.push(parsed)
    if (parsed.pathname === '/api/tickets/graph') return Response.json({ nodes: [{ id: 'a' }, { id: 'b' }, { id: 'c' }], links: [{ source: 'a', target: 'b' }, { source: 'b', target: 'c' }], truncated: true })
    return Response.json(parsed.searchParams.has('cursor') ? { items: [{ id: 'b' }], next_cursor: null } : { items: [{ id: 'a' }], next_cursor: 'next' })
  }
  try {
    const filters = filtersFromQuery({ assignee: 'person', status: 'in_progress', priority: 'high', type: 'ticket', q: 'body match', closed: '1' })
    const result = await loadTicketGraphContext('project', filters, new AbortController().signal)
    assert.equal(calls.length, 3)
    assert.equal(calls[0].searchParams.get('include_closed'), 'true')
    for (const query of calls.slice(1).map(call => call.searchParams)) {
      assert.equal(query.get('within'), 'project')
      assert.equal(query.get('assignee'), 'person')
      assert.deepEqual(query.get('state')?.split(','), ['in_progress', 'in-progress'])
      assert.equal(query.get('priority'), 'high')
      assert.equal(query.get('q'), 'body match')
      assert.equal(query.get('kind'), 'ticket')
    }
    assert.deepEqual(result.visible.nodes.map(node => node.id), ['a', 'b'])
    assert.deepEqual(result.visible.links, [{ source: 'a', target: 'b' }])
    assert.equal(result.visible.truncated, true)
    assert.equal(result.data.nodes.length, 3)
  } finally { globalThis.fetch = original }
})
