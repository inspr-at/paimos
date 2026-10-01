// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { movedQueue, queueable, queueProjection, readyGaps, type QueueWireSnapshot } from '../src/lib/workQueue.ts'
import { releaseCadence, suggestedRelease } from '../src/lib/suggestedRelease.ts'
import type { Release } from '../src/lib/releases.ts'
const hour = 3_600_000, now = Date.parse('2026-10-01T18:00:00Z')
const history = [0, 16, 32].map((ago, i) => ({ state: 'published', published_at: new Date(now - ago * hour).toISOString(), codename: `Cut ${i}`, version: `26100118000${i}.0.0`, tickets: i === 1 ? ['AEON-1'] : [], changes: [] })) as Release[]
test('definition of ready rejects missing, invalid and unnamed inputs without adding a queued status', () => {
  assert.equal(queueable({ kind_slug: 'ticket', state: 'open' }), true)
  for (const state of ['queued', 'in_progress', 'qa', 'done', 'delivered', 'accepted']) assert.equal(queueable({ kind_slug: 'ticket', state }), false)
  assert.equal(queueable({ kind_slug: 'epic', state: 'open' }), false)
  assert.deepEqual(readyGaps({ state: 'blocked', fields: {} }), ['estimate', 'criteria', 'blocker'])
  assert.deepEqual(readyGaps({ state: 'blocked', fields: { estimate_hours: 2, acceptance_criteria: '- Verified', blocker: 'unnamed' } }), ['blocker'])
  assert.deepEqual(readyGaps({ state: 'blocked', fields: { estimate_hours: 2, acceptance_criteria: ['Verified'], blocker: 'AEON-2' } }), [])
  assert.deepEqual(readyGaps({ state: 'open', fields: { estimate_hours: Infinity, acceptance_criteria: '  ' } }), ['estimate', 'criteria'])
})
test('manual moves preserve every member and refuse moves past an edge', () => {
  const ids = ['a', 'b', 'c']
  assert.deepEqual(movedQueue(ids, 'b', -1), ['b', 'a', 'c'])
  assert.deepEqual(movedQueue(ids, 'c', 'top'), ['c', 'a', 'b'])
  assert.equal(movedQueue(ids, 'a', -1), ids)
  assert.equal(movedQueue(ids, 'missing', 1), ids)
  assert.equal(movedQueue(ids, 'c', 1), ids)
})
test('release suggestions use observed cadence, actual membership, ETA and queue delay', () => {
  assert.deepEqual(releaseCadence(history), { hours: 16, latest: now, samples: 3 })
  const input = { key: 'AEON-1', state: 'open', fields: { estimate_hours: 3 } }
  assert.equal(suggestedRelease({ ...input, state: 'accepted' }, history, now).text, 'Cut 1')
  assert.equal(suggestedRelease({ ...input, state: 'done' }, [], now).text, 'Next release')
  assert.equal(suggestedRelease(input, history, now).text, '—')
  assert.equal(suggestedRelease(input, history, now, { expected_start: new Date(now + 2 * hour).toISOString() }).text, 'Next')
  assert.equal(suggestedRelease(input, history, now, { expected_start: new Date(now + 18 * hour).toISOString() }).text, 'Next +1')
  assert.equal(suggestedRelease({ ...input, state: 'in-progress', eta: { eta_ready_at: new Date(now + 19 * hour).toISOString() } }, history, now).text, 'Next +1')
  assert.equal(suggestedRelease({ ...input, state: 'in_progress', eta: { eta_ready_at: new Date(now + hour).toISOString(), ready_stale: true } }, history, now).text, '—')
  assert.equal(suggestedRelease({ ...input, state: 'in_progress', eta: { ready_stale: true } }, history, now, { expected_start: new Date(now).toISOString() }).text, '—')
  assert.equal(suggestedRelease(input, [history[0]!], now, { expected_start: new Date(now).toISOString() }).text, '—')
})
test('adapts Part A ticket projection without retaining an unadmitted run or inventing capacity', () => {
  const raw: QueueWireSnapshot = { manual_order: true, capacity: { queued_hours: 3, parallel_runs: 0, work_hours: null, warning: true }, items: [{
    node_id: 'ticket', key: 'AEON-1', title: 'Queued work', state: 'blocked', priority: 'high', estimate_hours: 3,
    queued: { run_id: 'run', position: 5, by: { id: 'person', name: 'Markus Barta', kind: 'person' }, at: '2026-10-01T18:00:00Z', target_agent_id: null, expected_agent_id: null, model_profile_id: null, expected_start_at: null, waiting: true, wait_reason: 'Blocked by AEON-2' },
  }] }
  const projected = queueProjection({ ...raw, items: raw.items.map(item => ({ ...item, run: { id: 'not-admitted', status: 'queued' } })) })
  assert.equal(projected.items[0]?.ticket_id, 'ticket'); assert.equal(projected.items[0]?.position, 5)
  assert.equal(projected.items[0]?.waiting_reason, 'Blocked by AEON-2')
  assert.equal(projected.items[0]?.expected_start, null); assert.equal(projected.capacity.hours, null)
  assert.equal(projected.capacity.warning, true); assert.equal('run' in projected.items[0]!, false)
})
