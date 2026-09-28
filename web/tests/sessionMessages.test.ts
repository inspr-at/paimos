// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { belongsToSession, collapseMessages, historicalSender } from '../src/components/agents/sessionMessages.ts'
import type { ProjectMessage } from '../src/lib/agents.ts'

const message = (id: string, seconds: number, fields: Partial<ProjectMessage> = {}): ProjectMessage => ({
  id, sender_principal_id: 'agent', recipient_principal_id: 'person', to: 'paimos:person', body: 'Ready.',
  sender_session_id: 'session-one', sender_label: 'Original lead', sent_event_id: seconds,
  is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none',
  created_at: new Date(1_800_000_000_000 + seconds * 1000).toISOString(), ...fields,
})
test('the exact session owns its conversation; unbound history owns neither generation', () => {
  assert.equal(belongsToSession(message('1', 0), 'session-one'), true)
  assert.equal(belongsToSession(message('1', 0), 'session-two'), false)
  assert.equal(belongsToSession(message('1', 0, { sender_session_id: undefined }), 'session-one'), false)
  assert.equal(belongsToSession(message('1', 0, { recipient_session_id: 'session-two' }), 'session-two'), true)
})
test('historical sender never resolves through the live principal label', () => {
  assert.equal(historicalSender(message('1', 0), 'person'), 'Original lead')
  assert.equal(historicalSender(message('1', 0, { sender_label: undefined, from: 'paimos:old-lead' }), 'person'), 'old-lead')
})
test('exact duplicate posts collapse within 60 seconds from the first post', () => {
  const input = [message('1', 0), message('2', 60), message('3', 61), message('4', 62, { sender_session_id: 'session-two' }), message('5', 63, { body: 'Ready. ' })]
  const groups = collapseMessages(input)
  assert.deepEqual(groups.map(g => [g.id, g.count]), [['1', 2], ['3', 1], ['4', 1], ['5', 1]])
  assert.equal(input.length, 5)
  assert.equal(collapseMessages([message('1', 0, { created_at: undefined }), message('2', 0, { created_at: undefined })]).length, 2)
})
