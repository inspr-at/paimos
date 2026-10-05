// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { movedQueue, queueable, queueProjection, readyGaps, type QueueWireSnapshot } from '../src/lib/workQueue.ts'
import { releaseCadence, suggestedRelease, visibleQueueTiming, type SuggestionInput } from '../src/lib/suggestedRelease.ts'
import type { QueueSnapshot } from '../src/lib/workQueue.ts'
import type { Release } from '../src/lib/releases.ts'
const hour = 3_600_000, now = Date.parse('2026-10-01T18:00:00Z')
const history = [0, 16, 32].map((ago, i) => ({ state: 'published', published_at: new Date(now - ago * hour).toISOString(), codename: `Cut ${i}`, version: `26100118000${i}.0.0`, tickets: i === 1 ? ['AEON-1'] : [], changes: [] })) as Release[]
test('definition of ready rejects missing, invalid and unnamed inputs without adding a queued status', () => {
  assert.equal(queueable({ kind_slug: 'ticket', state: 'open' }), true)
  for (const state of ['queued', 'in_progress', 'qa', 'done', 'delivered', 'accepted']) assert.equal(queueable({ kind_slug: 'ticket', state }), false)
  assert.equal(queueable({ kind_slug: 'epic', state: 'open' }), false)
  assert.equal(queueable({ kind_slug: 'work', state: 'open' }), false)
  assert.equal(queueable({ kind_slug: 'work', state: 'open', is_leaf: true }), true)
  assert.equal(queueable({ kind_slug: 'work', state: 'open', estimate: { is_parent: false } }), true)
  assert.equal(queueable({ kind_slug: 'work', state: 'open', is_leaf: false, estimate: { is_parent: false } }), false)
  assert.equal(queueable({ kind_slug: 'work', state: 'open', estimate: { is_parent: true } }), false)
  assert.equal(queueable({ kind_slug: 'work', state: 'done', is_leaf: true }), false)
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
test('canonical work parents have no release suggestion even when done or named in a release', () => {
  const shapes = [
    { is_leaf: false },
    { estimate: { is_parent: true } },
    { is_leaf: false, estimate: { is_parent: false } },
  ]
  for (const kind_slug of ['work', 'ticket', 'task', 'epic']) for (const shape of shapes) {
    for (const state of ['done', 'Done', 'qa', 'in_progress', 'open', 'delivered', 'accepted']) {
      const input: SuggestionInput = { key: 'AEON-1', kind_slug, state, fields: { estimate_hours: 3 }, eta: { eta_ready_at: new Date(now + hour).toISOString() }, ...shape }
      const view = suggestedRelease(input, history, now, { expected_start: new Date(now + 2 * hour).toISOString() })
      assert.deepEqual(view, { text: '—', kind: 'empty', tip: 'Releases are suggested for leaf work; a parent’s leaves can span releases' }, `${kind_slug} ${state} ${JSON.stringify(shape)}`)
    }
  }
  assert.equal(suggestedRelease({ key: 'AEON-1', kind_slug: 'epic', state: 'done', fields: {} }, history, now).kind, 'empty')
})
test('canonical work leaves retain release membership, cadence, ETA and queue predictions', () => {
  const cases = [
    { state: 'done', text: 'Next release', kind: 'planned' },
    { state: 'Done', text: 'Next release', kind: 'planned' },
    { state: 'qa', text: 'Next', kind: 'planned' },
    { state: 'in_progress', text: 'Next', kind: 'planned' },
    { state: 'open', text: 'Next', kind: 'planned' },
    { state: 'delivered', text: 'Cut 1', kind: 'shipped' },
    { state: 'accepted', text: 'Cut 1', kind: 'shipped' },
  ]
  for (const shape of [{ is_leaf: true }, { estimate: { is_parent: false } }, { is_leaf: true, estimate: { is_parent: true } }]) {
    for (const { state, text, kind } of cases) {
      const input: SuggestionInput = { key: 'AEON-1', kind_slug: 'work', state, fields: { estimate_hours: 3 }, eta: { eta_ready_at: new Date(now + hour).toISOString() }, ...shape }
      const view = suggestedRelease(input, history, now, { expected_start: new Date(now + 2 * hour).toISOString() })
      assert.equal(view.text, text, `${state} ${JSON.stringify(shape)}`)
      assert.equal(view.kind, kind)
      if (kind === 'shipped') assert.equal(view.version, history[1]!.version)
    }
  }
  assert.equal(suggestedRelease({ key: 'AEON-1', kind_slug: 'work', is_leaf: true, state: 'done', fields: {} }, [], now).text, 'Next release')
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
test('queued suggestions with no server start use visible workload and measured capacity, with scope explained', () => {
  const snapshot: QueueSnapshot = { manual_order: false, capacity: { hours: 20, total: 2 }, items: [
    { ticket_id: 'a', position: 5, estimate_hours: 40, target_agent_id: null },
    { ticket_id: 'targeted', position: 1, estimate_hours: 100, target_agent_id: 'agent' },
    { ticket_id: 'b', position: 8, estimate_hours: 3, expected_start: null, target_agent_id: null },
  ] as QueueSnapshot['items'] }
  const input = { key: 'AEON-1', state: 'open', fields: { estimate_hours: 3 } }
  const timing = visibleQueueTiming(snapshot, 'b')
  assert.equal(timing?.visible_wait_hours, 20)
  const view = suggestedRelease(input, history, now, timing)
  assert.equal(view.text, 'Next +1'); assert.match(view.tip, /wait ~20 h at 2 parallel runs/)
  assert.match(view.tip, /Other projects not included/)
  assert.equal(suggestedRelease(input, history, now, visibleQueueTiming(snapshot, 'a')).text, 'Next +2')
  assert.equal(suggestedRelease({ ...input, state: 'blocked' }, history, now, timing).text, '—')
  snapshot.items[0]!.estimate_hours = 2
  assert.equal(suggestedRelease(input, history, now, visibleQueueTiming(snapshot, 'b')).text, 'Next')
  snapshot.items[0]!.estimate_hours = null
  assert.equal(suggestedRelease(input, history, now, visibleQueueTiming(snapshot, 'b')).text, '—')
  snapshot.items[0]!.estimate_hours = 2; snapshot.capacity.total = 0
  assert.equal(suggestedRelease(input, history, now, visibleQueueTiming(snapshot, 'b')).text, '—')
  snapshot.items[2]!.expected_start = new Date(now + 18 * hour).toISOString()
  const authoritative = suggestedRelease(input, history, now, visibleQueueTiming(snapshot, 'b'))
  assert.equal(authoritative.text, 'Next +1'); assert.doesNotMatch(authoritative.tip, /Other projects/)
  assert.equal(visibleQueueTiming(snapshot, 'unknown'), null)
})

test('stale progress is queueable only with authoritative idle evidence', () => {
  const idle = { kind_slug: 'ticket', state: 'in_progress', queue_stale: true }
  assert.equal(queueable(idle), true)
  for (const state of ['In progress', 'in-progress', 'active', 'progress']) assert.equal(queueable({ ...idle, state }), true)
  assert.equal(queueable({ ...idle, queue_stale: false }), false)
  assert.equal(queueable({ ...idle, kind_slug: 'epic' }), false)
  assert.equal(queueable({ ...idle, assignee: { id: 'person', name: 'Markus Barta' } }), false)
  assert.equal(queueable({ ...idle, lead_worker: { key: 'run', name: 'Builder' } }), false)
})
