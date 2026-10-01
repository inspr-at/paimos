// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { currentActivity, currentStep, workerActivity, activityDurations } from '../src/components/agents/activity.ts'
import type { SessionView } from '../src/stores/agents.ts'

const now = Date.parse('2026-10-01T21:00:00Z')
const view = (text: string, source: 'auto' | 'agent' = 'auto', age = 0, mode = 'agent_summary') => ({ session: { agent_activity_mode: mode, current_activity: { text, source, at: new Date(now - age).toISOString() } } }) as unknown as SessionView
test('policy and freshness govern the current line, including stopped sessions', () => {
  assert.equal(currentActivity(view('Running Go tests'), now), 'Running Go tests')
  assert.equal(currentActivity(view('Implementing', 'agent', 599_999), now), 'Implementing')
  assert.equal(currentActivity(view('Implementing', 'agent', 600_000), now), '')
  assert.equal(currentActivity(view('Implementing', 'agent', 0, 'tool_activity'), now), '')
  assert.equal(currentActivity(view('Running Go tests', 'auto', 0, 'off'), now), '')
  assert.equal(currentActivity({ ...view('Running Go tests'), session: { ...view('').session, stopped_at: new Date(now).toISOString() } } as unknown as SessionView, now), '')
})
test('coordinator summaries combine testing workers and exclude disabled or ended activity', () => {
  const coordinator = { ...view('Working'), session: { ...view('Working').session, role: 'coordinator' as const } }
  assert.equal(workerActivity([view('Running Go tests'), view('Running web tests'), view('Running browser tests'), view('Waiting for CI'), view('Pushing', 'auto', 0, 'off'), coordinator], now), '3 workers testing · 1 worker waiting for CI')
})
test('summary policy preserves the existing heartbeat note for older reporters', () => {
  const legacy = { session: { agent_activity_mode: 'agent_summary', activity_note: 'Reviewing the change' }, status: { state: 'working' } } as unknown as SessionView
  assert.equal(currentStep(legacy), 'Reviewing the change')
  assert.equal(currentStep({ ...legacy, session: { ...legacy.session, agent_activity_mode: 'off' } }), 'Working on this project')
})
test('history reports phase durations using the next change and stops at session end', () => {
  const history = [{ text: 'Committing', source: 'auto' as const, at: new Date(now - 300_000).toISOString() }, { text: 'Running Go tests', source: 'auto' as const, at: new Date(now - 1_020_000).toISOString() }]
  assert.deepEqual(activityDurations(history, now).map(item => item.duration), ['5 min', '12 min'])
  assert.deepEqual(activityDurations(history, now + 600_000, new Date(now).toISOString()).map(item => item.duration), ['5 min', '12 min'])
})
