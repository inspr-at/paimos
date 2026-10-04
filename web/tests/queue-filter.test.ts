// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { ListItem, ListQuery } from '../src/lib/api.ts'
import type { QueueSnapshot } from '../src/lib/workQueue.ts'
import { queueFilteredNodes } from '../src/lib/queueFilter.ts'
const items = Array.from({ length: 245 }, (_, i) => ({ id: `id-${i}`, key: `AEON-${i}`, title: `Item ${i}`, state: 'open', kind_slug: 'ticket', priority: 'high', fields: {}, assignee: null, updated_at: '2026-10-01T18:00:00Z' })) as ListItem[]
const snapshot = { items: items.map(row => ({ ticket_id: row.id })), manual_order: false, capacity: { hours: 5, total: 2 } } as QueueSnapshot
test('Queued reads all projection IDs in batches of 200 and pages without truncating membership', async () => {
  const queries: ListQuery[] = []
  const read = async (query: ListQuery) => { queries.push(query); return { items: items.filter(row => query.ids?.includes(row.id)), next_cursor: null } }
  const first = await queueFilteredNodes({ state: ['queued'], limit: 200, sort: '-updated_at', facets: ['kind', 'state'] }, snapshot, read)
  assert.equal(first.items.length, 200); assert.equal(first.next_cursor, 'queue:200')
  assert.equal(first.facets?.kind?.ticket, 245); assert.equal(first.facets?.state?.open, 245)
  assert.equal(queries.length, 2); assert.equal(queries[0]?.ids?.length, 200); assert.equal(queries[1]?.ids?.length, 45)
  assert.ok(queries.every(query => !query.state?.includes('queued')))
  const second = await queueFilteredNodes({ state: ['queued'], limit: 200, sort: '-updated_at', cursor: first.next_cursor! }, snapshot, read)
  assert.equal(second.items.length, 45); assert.equal(second.next_cursor, null)
})
test('Queued alternatives deduplicate, exclusions combine, and live reads restrict IDs', async () => {
  const done = { ...items[0]!, id: 'done', key: 'AEON-999', state: 'done' }
  const read = async (query: ListQuery) => ({ items: query.ids ? items.filter(row => query.ids!.includes(row.id)) : [items[0]!, done], next_cursor: null })
  const mixed = await queueFilteredNodes({ state: ['queued', 'done'], ids: undefined, limit: 500 }, snapshot, read)
  assert.equal(mixed.items.length, 246)
  const live = await queueFilteredNodes({ state: ['queued'], ids: ['id-244'] }, snapshot, read)
  assert.deepEqual(live.items.map(row => row.id), ['id-244'])
  const excluded = await queueFilteredNodes({ state: ['!queued'] }, snapshot, read)
  assert.deepEqual(excluded.items.map(row => row.id), ['done'])
})
