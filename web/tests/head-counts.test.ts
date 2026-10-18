// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { headCounts, matchesFilter, type Countable } from '../src/components/agents/headCounts.ts'

const now = Date.parse('2026-10-06T12:00:00Z')
const view = (group: string, state: string, fields: Partial<Countable['session']> = {}, name = ''): Countable => ({
  name, status: { group, state, label: 'Lost contact.' } as Countable['status'],
  session: { phase: 'working', created_at: '2026-10-06T11:00:00Z', heartbeat_at: '2026-10-06T11:44:00Z', ...fields },
})

test('working, waiting and problems always show; the rest only while non-zero and after them', () => {
  const calm = headCounts([view('working', 'working')], now)
  assert.deepEqual(calm.map(c => [c.filter, c.n]), [['working', 1], ['waiting', 0], ['problem', 0]])
  assert.equal(calm[2]!.label, 'problems')
  const busy = headCounts([view('throttled', 'throttled'), view('paused', 'paused', { phase: 'paused' }), view('pausing', 'pausing')], now)
  assert.deepEqual(busy.map(c => c.filter), ['working', 'waiting', 'problem', 'throttled', 'pausing', 'paused'])
})

test('the head count and the Sessions filter use the same rule', () => {
  const ended = view('stopped', 'stopped', { phase: 'stopped', stopped_at: '2026-10-06T11:50:00Z' })
  const lost = view('unresponsive', 'unresponsive')
  const asks = view('needs', 'waiting')
  for (const filter of ['working', 'waiting', 'problem', 'throttled', 'pausing', 'paused'] as const) assert.equal(matchesFilter(ended, filter), false, filter)
  // Lost contact counts as a problem, as the live line's "in trouble" did.
  assert.equal(matchesFilter(lost, 'problem'), true)
  assert.equal(matchesFilter(asks, 'waiting'), true)
  assert.equal(matchesFilter(asks, 'working'), false)
  const counts = headCounts([lost, asks, ended], now)
  assert.deepEqual(counts.map(c => [c.filter, c.n]), [['working', 0], ['waiting', 1], ['problem', 1]])
})

test('one problem keeps its name, reason and time since in the tip; zeros say none', () => {
  const [working, , problem] = headCounts([view('problem', 'problem', {}, 'aeon-715-replay')], now)
  assert.equal(problem!.label, 'problem')
  assert.equal(problem!.tip, 'Show aeon-715-replay: Lost contact · 16 min')
  assert.equal(working!.tip, 'No session is working')
  const two = headCounts([view('problem', 'problem'), view('unresponsive', 'unresponsive')], now)[2]!
  assert.equal(two.tip, 'Show sessions with a problem or lost contact')
})
