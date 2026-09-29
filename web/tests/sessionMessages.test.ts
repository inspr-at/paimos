// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { answeredMessages, collapseMessages, historicalSender } from '../src/components/agents/sessionMessages.ts'
import type { ProjectMessage } from '../src/lib/agents.ts'

const message = (id: string, seconds: number, fields: Partial<ProjectMessage> = {}): ProjectMessage => ({
  id, sender_principal_id: 'agent', recipient_principal_id: 'person', to: 'paimos:person', body: 'Ready.',
  sender_session_id: 'session-one', sender_label: 'Original lead', sent_event_id: seconds,
  is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none',
  created_at: new Date(1_800_000_000_000 + seconds * 1000).toISOString(), ...fields,
})
test('only an accepted counterpart reply marks a question answered', () => {
  const question = message('q', 0, { sender_principal_id: 'person', recipient_principal_id: 'agent' })
  const reply = message('r', 1, { reply_to: 'q' })
  assert.deepEqual([...answeredMessages([question, reply])], ['q'])
  for (const fields of [{ is_action_request: true }, { status: 'held' }, { sender_principal_id: 'other' }, { recipient_principal_id: 'other' }, { reply_to: undefined }]) {
    assert.equal(answeredMessages([question, { ...reply, ...fields }]).size, 0)
  }
  assert.equal(collapseMessages([question, reply])[0]!.answered, true)
  assert.equal(collapseMessages([question])[0]!.answered, false)
})
test('repeated text across a turn stays in order and answered duplicates remain distinct', () => {
  const question = message('q', 1, { sender_principal_id: 'person', recipient_principal_id: 'agent' })
  assert.deepEqual(collapseMessages([message('1', 0), question, message('2', 2)]).map(m => m.id), ['1', 'q', '2'])
  const again = { ...question, id: 'q2', sent_event_id: 2 }
  const groups = collapseMessages([question, again, message('r', 3, { reply_to: 'q2' })])
  assert.deepEqual(groups.map(m => [m.id, m.answered]), [['q', false], ['q2', true], ['r', false]])
})
test('historical sender never resolves through the live principal label', () => {
  assert.equal(historicalSender(message('1', 0), 'person'), 'Original lead')
  assert.equal(historicalSender(message('1', 0, { sender_label: undefined, from: 'paimos:old-lead' }), 'person'), 'old-lead')
  assert.equal(historicalSender(message('1', 0, { sender_label: 'paimos:old-lead', from: 'paimos:old-lead' }), 'person'), 'old-lead')
  assert.equal(historicalSender(message('1', 0, { sender_label: 'Frozen label', from: 'paimos:old-lead' }), 'person'), 'Frozen label')
  assert.equal(historicalSender(message('1', 0), 'agent'), 'You')
})
test('exact duplicate posts collapse within 60 seconds from the first post', () => {
  const input = [message('1', 0), message('2', 60), message('3', 61), message('4', 62, { sender_session_id: 'session-two' }), message('5', 63, { body: 'Ready. ' })]
  const groups = collapseMessages(input)
  assert.deepEqual(groups.map(g => [g.id, g.count]), [['1', 2], ['3', 1], ['4', 1], ['5', 1]])
  assert.equal(input.length, 5)
  assert.equal(collapseMessages([message('1', 0, { created_at: undefined }), message('2', 0, { created_at: undefined })]).length, 2)
})
