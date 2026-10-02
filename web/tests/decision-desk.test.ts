// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { loadDeskProjection, type DeskItem, type DeskProjection } from '../src/lib/decisionDesk.ts'

const item = (id: string, held = false): DeskItem => ({ id, kind: 'question', revision: 1, title: id, held, created_at: '', href: `/agents?needs=q:${id}`, source: `/api/questions/${id}` })
const page = (items: DeskItem[], next?: string, open = 3): DeskProjection => ({ items, counts: { open, held: 1, chores: 2 }, has_more: !!next, next_cursor: next, as_of: '2026-10-02T12:00:00Z' })

test('briefing preserves server order, exact count and source links across pages', async () => {
  const result = await loadDeskProjection(undefined, async (_limit, cursor) => cursor ? page([item('old'), item('new')]) : page([item('held', true), item('old')], 'next'))
  assert.deepEqual(result.items.map(i => i.id), ['held', 'old', 'new'])
  assert.equal(result.counts.open, 3)
  assert.equal(result.counts.chores, 2)
  assert.equal(result.items[2]!.source, '/api/questions/new')
  assert.equal(result.truncated, false)
})
test('a repeated keyset cursor fails without looping or reporting completeness', async () => {
  await assert.rejects(loadDeskProjection(undefined, async () => page([item('one')], 'same')), /did not advance/)
})
test('bounded or changing coverage stays explicit while the count stays server-defined', async () => {
  const bounded = await loadDeskProjection(undefined, async (_limit, cursor) => page([item(cursor ?? 'first')], `${cursor ?? ''}next`, 1001))
  assert.equal(bounded.items.length, 10)
  assert.equal(bounded.counts.open, 1001)
  assert.equal(bounded.truncated, true)
  const changed = await loadDeskProjection(undefined, async () => page([item('one')], undefined, 2))
  assert.equal(changed.truncated, true)
})
