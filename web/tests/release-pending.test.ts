// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { getPendingChanges, pendingLine, type PendingChanges } from '../src/lib/releasePending.ts'

function pending(overrides: Partial<PendingChanges> = {}): PendingChanges {
  return { live_version: '261003065316.0.0', base_commit: 'a'.repeat(40), head_commit: 'b'.repeat(40), checked_at: '2026-10-03T12:00:00Z', source: 'github-main', status: 'available', total: 0, known_total: 0, changes: [], next_cursor: null, unavailable: [], ...overrides }
}

test('waiting counts distinguish exact zero, incomplete evidence and unavailable reads', () => {
  assert.equal(pendingLine(pending(), false, ''), '0 changes waiting for the next release')
  assert.equal(pendingLine(pending({ total: 1, known_total: 1 }), false, ''), '1 change waiting for the next release')
  assert.equal(pendingLine(pending({ status: 'partial', total: null, known_total: 250 }), false, ''), 'At least 250 changes waiting for the next release')
  assert.equal(pendingLine(pending({ status: 'unavailable', total: null }), false, ''), 'Waiting changes unavailable')
  assert.equal(pendingLine(null, true, ''), 'Checking changes waiting for the next release')
  assert.equal(pendingLine(pending(), false, 'No connection'), 'Waiting changes unavailable')
})

test('waiting pages use an encoded cursor, fixed limit and caller cancellation', async t => {
  const controller = new AbortController()
  const cursor = 'snapshot:after/a+b='
  t.mock.method(globalThis, 'fetch', async (url: string | URL | Request, options?: RequestInit) => {
    const query = new URL(String(url), 'http://localhost').searchParams
    assert.equal(query.get('limit'), '50')
    assert.equal(query.get('cursor'), cursor)
    assert.ok(options?.signal instanceof AbortSignal)
    controller.abort()
    assert.equal(options?.signal?.aborted, true)
    return Response.json(pending())
  })
  assert.deepEqual(await getPendingChanges(cursor, controller.signal), pending())
})

test('a moved snapshot error is surfaced rather than replacing an incomplete list with success', async t => {
  t.mock.method(globalThis, 'fetch', async () => Response.json({ error: 'pending snapshot changed; restart without a cursor' }, { status: 409 }))
  await assert.rejects(getPendingChanges('old-snapshot'), /pending snapshot changed/)
})

test('malformed, unbounded and inconsistent waiting counts are rejected', async t => {
  for (const value of [pending({ known_total: -1 }), pending({ known_total: 251 }), pending({ total: null }), pending({ status: 'partial', total: 1 }), { ...pending(), next_cursor: 42 }]) {
    const fetch = t.mock.method(globalThis, 'fetch', async () => Response.json(value))
    await assert.rejects(getPendingChanges(), /unknown waiting changes format/)
    fetch.mock.restore()
  }
})
