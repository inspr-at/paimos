// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { loadTicketKinds, placementPatch, placementSuggested, ticketKinds, type PlacementKind } from '../src/lib/ticketPlacement.ts'

const kind = (slug: string, extra = {}): PlacementKind => ({ id: slug, slug, label: slug, position: 0, ...extra })
test('ticket areas include security and visible project kinds, excluding matrix-only and archived rows', () => {
  const kinds = [kind('security'), kind('backend'), kind('review'), kind('other'), kind('old', { archived_at: '2026-10-02' }), kind('firmware', { project_id: 'P' }), kind('foreign', { project_id: 'Q' })]
  assert.deepEqual(ticketKinds(kinds, 'P').map(k => k.slug), ['backend', 'firmware', 'security'])
})
test('confirming suggested or agent classifications removes provenance while preserving the exact value', () => {
  assert.equal(placementSuggested({ area_source: 'suggested' }, 'area'), true)
  assert.equal(placementSuggested({ complexity_source: 'agent', complexity_confirmed: false }, 'complexity'), true)
  assert.equal(placementSuggested({ complexity_source: 'person', complexity_confirmed: true }, 'complexity'), false)
  assert.deepEqual(placementPatch('area', 'security'), { area: 'security', area_source: undefined, area_by: undefined, area_at: undefined, area_confirmed: undefined })
  assert.equal(placementPatch('complexity', '').complexity, null)
})
test('kind pagination loads all pages and rejects loops or truncation instead of returning partial choices', async () => {
  const seen: (string | undefined)[] = []
  const kinds = await loadTicketKinds(async cursor => {
    seen.push(cursor)
    return cursor ? { items: [kind('security')], next_cursor: null } : { items: [kind('backend')], next_cursor: 'next' }
  })
  assert.deepEqual(seen, [undefined, 'next']); assert.equal(kinds.length, 2)
  await assert.rejects(loadTicketKinds(async () => ({ items: [], next_cursor: 'loop' })), /completely/)
  await assert.rejects(loadTicketKinds(async () => ({ items: Array.from({ length: 101 }, () => kind('backend')), next_cursor: null })), /Too many/)
  await assert.rejects(loadTicketKinds(async () => { throw new Error('Denied') }), /Denied/)
})
