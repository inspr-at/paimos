// SPDX-License-Identifier: AGPL-3.0-only
// AEON-312: the gear menu's system status and the feedback message it sends.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { feedbackBody, summarizeStatus } from '../src/lib/headerMenus.ts'

test('system status: words for every state, checking until the probe answers', () => {
  assert.deepEqual(summarizeStatus(null), { state: 'checking', label: 'Checking…', detail: '' })
  assert.equal(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: true }).state, 'healthy')
  assert.equal(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: true }).label, 'Operational')
  assert.deepEqual(summarizeStatus({ health: null, ready: null }), { state: 'down', label: 'Unreachable', detail: 'The server did not answer.' })
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'down' }, ready: false }), { state: 'degraded', label: 'Degraded', detail: 'The database is not answering.' })
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: false }), { state: 'degraded', label: 'Degraded', detail: 'The server is not ready for requests.' })
  // An unreadable ready answer is not a failure on its own.
  assert.equal(summarizeStatus({ health: { status: 'ok' }, ready: null }).state, 'healthy')
})

test('feedback says where it was written, then the words', () => {
  assert.equal(feedbackBody('  Lovely.  ', { path: '/p/AEON', version: '260929120000.0.0' }), 'Feedback from the app (on /p/AEON, version 260929120000.0.0)\n\nLovely.')
  assert.equal(feedbackBody('Hi', { path: '', version: '' }), 'Feedback from the app\n\nHi')
  assert.equal(feedbackBody('Hi', { path: '/', version: '' }), 'Feedback from the app (on /)\n\nHi')
})
