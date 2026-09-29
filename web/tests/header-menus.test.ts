// SPDX-License-Identifier: AGPL-3.0-only
// AEON-312: the gear menu's system status and the feedback message it sends.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { HOOK_COMMANDS, feedbackBody, hookDocsUrl, summarizeStatus } from '../src/lib/headerMenus.ts'

test('system status: words for every state, checking until the probe answers', () => {
  assert.deepEqual(summarizeStatus(null), { state: 'checking', label: 'Checking…', detail: '' })
  assert.equal(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: true }).state, 'healthy')
  assert.equal(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: true }).label, 'Operational')
  assert.deepEqual(summarizeStatus({ health: null, ready: null }), { state: 'down', label: 'Unreachable', detail: 'The server did not answer.' })
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'down' }, ready: false }), { state: 'degraded', label: 'Degraded', detail: 'The database is not answering.' })
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: false }), { state: 'degraded', label: 'Degraded', detail: 'The server is not ready for requests.' })
  // Readiness must be confirmed: an unreadable answer is never Operational.
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: null }), { state: 'degraded', label: 'Unavailable', detail: 'Readiness could not be checked.' })
  assert.equal(summarizeStatus({ health: { status: 'ok' }, ready: true }).state, 'degraded')
})

test('feedback says where it was written, then the words', () => {
  assert.equal(feedbackBody('  Lovely.  ', { path: '/p/AEON', version: '260929120000.0.0' }), 'Feedback from the app (on /p/AEON, version 260929120000.0.0)\n\nLovely.')
  assert.equal(feedbackBody('Hi', { path: '', version: '' }), 'Feedback from the app\n\nHi')
  assert.equal(feedbackBody('Hi', { path: '/', version: '' }), 'Feedback from the app (on /)\n\nHi')
})

test('inbox hooks: one user-scope command per harness and the guide section in this repository', () => {
  assert.deepEqual(HOOK_COMMANDS.map(hook => hook.command), ['aeon hook install --harness claude --scope user', 'aeon hook install --harness codex --scope user'])
  assert.equal(hookDocsUrl('inspr-at/aeon'), 'https://github.com/inspr-at/aeon/blob/main/docs/AGENT_INTEGRATION.md#operator-installed-turn-boundary-hooks-aeon-281')
  assert.equal(hookDocsUrl(''), 'https://github.com/inspr-at/paimos/blob/main/docs/AGENT_INTEGRATION.md#operator-installed-turn-boundary-hooks-aeon-281')
  assert.match(hookDocsUrl('evil/x"><script>'), /^https:\/\/github\.com\/inspr-at\/paimos\//)
})
