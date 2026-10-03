// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readDeskProjection, loadDeskProjection, type DeskProjectionItem as DeskItem, type DeskProjection } from '../src/lib/decisionDesk.ts'

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

test('projection reader bounds streamed bytes before JSON decoding', async () => {
  const original = globalThis.fetch
  let cancelled = false
  globalThis.fetch = async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new Uint8Array(2 * 1024 * 1024 + 1)) },
    cancel() { cancelled = true },
  }))
  try {
    await assert.rejects(readDeskProjection(), /too large/)
    assert.equal(cancelled, true)
  } finally { globalThis.fetch = original }
})
test('invalid page bounds fail before starting a request', async () => {
  const original = globalThis.fetch
  globalThis.fetch = async () => { throw new Error('unexpected request') }
  try {
    await assert.rejects(readDeskProjection(101), /Invalid Decision Desk page/)
    await assert.rejects(readDeskProjection(10, 'x'.repeat(513)), /Invalid Decision Desk page/)
  } finally { globalThis.fetch = original }
})
