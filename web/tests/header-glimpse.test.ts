// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { glimpseControlSpot, glimpseOverlaps, glimpsePlaceControls, glimpseTextMask, headerGlimpseAllowed, headerGlimpseGraphReady, headerGlimpseWide, nextHeaderGlimpseWide, headerGlimpseViewportWide } from '../src/lib/headerGlimpse.ts'

const on = { enabled: true, reduced: false, wide: true, tickets: 8 }

test('the header glimpse needs a wide viewport, motion, and eight tickets', () => {
  assert.equal(headerGlimpseAllowed(on), true)
  assert.equal(headerGlimpseAllowed({ ...on, enabled: false }), false)
  assert.equal(headerGlimpseAllowed({ ...on, reduced: true }), false)
  assert.equal(headerGlimpseAllowed({ ...on, wide: false }), false)
  assert.equal(headerGlimpseAllowed({ ...on, tickets: 7 }), false)
  assert.equal(headerGlimpseWide(1280), true)
  assert.equal(headerGlimpseWide(1279), false)
  assert.equal(headerGlimpseWide(390), false)
  assert.equal(headerGlimpseWide(Number.NaN), false)
  // The CI failure: viewport 1280 turned the glimpse on, then 390 dropped the
  // MediaQueryList change and the previous flag stayed on.
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: null, layoutWidth: 390 }), false)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: null, layoutWidth: 1280 }), true)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 1279 }), true)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: false, layoutWidth: 1280 }), false)
})

test('a stale wide media query cannot keep the header glimpse once the layout is below 1280', () => {
  assert.equal(headerGlimpseViewportWide(true, 1280), true)
  assert.equal(headerGlimpseViewportWide(true, 1600), true)
  assert.equal(headerGlimpseViewportWide(true, 1279), false)
  assert.equal(headerGlimpseViewportWide(true, 390), false)
  assert.equal(headerGlimpseViewportWide(false, 1280), false)
  assert.equal(headerGlimpseViewportWide(false, 1600), false)
  assert.equal(headerGlimpseViewportWide(true, Number.NaN), false)
  assert.equal(headerGlimpseViewportWide(true, Number.POSITIVE_INFINITY), false)
  // PR #737: 1279 → 1280 → 390 delivered the earlier matches:true after the
  // layout had already left the threshold, and the glimpse stayed on.
  // One pixel of catch-up remains wide; 390 and 1278 do not.
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 390 }), false)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 1278 }), false)
})

test('a laid-out narrow root hides the header glimpse while the media query and inner width are still wide', () => {
  // PR #743: the shrink to 390 laid the root out narrow while matchMedia still
  // reported the previous wide viewport and window.innerWidth had not moved.
  // Two animation frames then expired, so the glimpse stayed on. A classic
  // scrollbar (1280 vs 1264) is still the wide layout.
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 1600, boxWidth: 390 }), false)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: null, layoutWidth: 1600, boxWidth: 390 }), false)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 1280, boxWidth: 390 }), false)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 1280, boxWidth: 1264 }), true)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 1280, boxWidth: 1280 }), true)
  assert.equal(nextHeaderGlimpseWide({ mediaMatches: true, layoutWidth: 1279 }), true)
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

test('hover controls use a fine search before dropping labels when the 8px grid misses the only opening', () => {
  // The only clear cell is exactly (10, 10). It is one pixel off the coarse
  // grid, so the labelled size still fits once the search steps by a pixel.
  const boxes = [
    { x: 0, y: 0, width: 10, height: 200 },
    { x: 110, y: 0, width: 290, height: 200 },
    { x: 0, y: 0, width: 400, height: 10 },
    { x: 0, y: 40, width: 400, height: 160 },
  ]
  const full = { width: 100, height: 30 }
  assert.equal(glimpseControlSpot(400, 200, boxes, full), null)
  const placed = glimpsePlaceControls(400, 200, boxes, { full, compact: { width: 40, height: 30 } })
  assert.deepEqual(placed, { spot: { x: 10, y: 10, width: 100, height: 30 }, compact: false })
  assert.ok(boxes.every(box => !glimpseOverlaps(placed!.spot, box, 0)))
})

test('hover controls drop to the compact size when the labelled row cannot fit', () => {
  // 140px of free header. The labelled row is 180px at every inset and step.
  const boxes = [
    { x: 0, y: 0, width: 220, height: 200 },
    { x: 360, y: 0, width: 40, height: 200 },
    { x: 0, y: 80, width: 400, height: 120 },
  ]
  const full = { width: 180, height: 34 }, compact = { width: 120, height: 34 }
  assert.equal(glimpseControlSpot(400, 200, boxes, full, 0, 8, 1), null)
  const placed = glimpsePlaceControls(400, 200, boxes, { full, compact })
  assert.equal(placed?.compact, true)
  assert.equal(placed?.spot.width, 120)
  assert.ok(boxes.every(box => !glimpseOverlaps(placed!.spot, box, 0)))
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
    return Response.json(parsed.searchParams.has('cursor') ? { items: [{ id: 'b' }, { id: 'outside-graph' }], next_cursor: null } : { items: [{ id: 'a' }], next_cursor: 'next' })
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
      // Type narrows by work level over every work kind (AEON-974): a legacy ticket is a leaf.
      assert.equal(query.get('level'), 'leaf')
      assert.equal(query.get('kind'), 'work,ticket,task,epic')
    }
    assert.deepEqual(result.visible.nodes.map(node => node.id), ['a', 'b'])
    assert.deepEqual(result.visible.links, [{ source: 'a', target: 'b' }])
    assert.equal(result.visible.truncated, true)
    assert.equal(result.data.nodes.length, 3)
  } finally { globalThis.fetch = original }
})
