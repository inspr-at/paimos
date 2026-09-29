// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { LISTENING_WINDOW_MS, listeningState } from '../src/components/agents/listening.ts'

const now = Date.parse('2026-09-29T06:00:00Z')
const live = { phase: 'working' as const, stopped_at: null, archived_at: null }
const at = (ms: number) => new Date(now - ms).toISOString()

test('a recent pull is Listening, with how it pulled in the tooltip (AEON-280)', () => {
  const state = listeningState({ ...live, inbox_seen_at: at(20_000), inbox_seen_via: 'stream' }, now)
  assert.deepEqual(state, { listening: true, label: 'Listening', detail: '', tip: 'Pulled its inbox just now through a live stream.' })
  assert.equal(listeningState({ ...live, inbox_seen_at: at(LISTENING_WINDOW_MS - 1), inbox_seen_via: 'hook' }, now)?.listening, true)
})

test('a hook pull shows the Listening label, not an acknowledgement (AEON-307)', () => {
  const state = listeningState({ ...live, inbox_seen_at: at(1_000), inbox_seen_via: 'hook' }, now)
  assert.equal(state?.label, 'Listening')
  assert.equal(state?.detail, '')
  assert.equal(state?.tip, 'Pulled its inbox just now through a turn hook.')
})

test('an old pull or none is Not listening with a plain detail', () => {
  const old = listeningState({ ...live, inbox_seen_at: at(12 * 60_000), inbox_seen_via: 'drain' }, now)
  assert.equal(old?.listening, false)
  assert.equal(old?.label, 'Not listening')
  assert.equal(old?.detail, 'last pulled 12m ago')
  assert.match(old!.tip, /through managed delivery/)
  const never = listeningState({ ...live, inbox_seen_at: undefined, inbox_seen_via: undefined }, now)
  assert.equal(never?.detail, 'never pulled')
  assert.equal(listeningState({ ...live, inbox_seen_at: at(3 * 3600_000) }, now)?.detail, 'last pulled 3h ago')
})

test('ended sessions show nothing', () => {
  assert.equal(listeningState({ ...live, phase: 'stopped', inbox_seen_at: at(1000) }, now), null)
  assert.equal(listeningState({ ...live, stopped_at: at(1000) }, now), null)
  assert.equal(listeningState({ ...live, archived_at: at(1000) }, now), null)
})
