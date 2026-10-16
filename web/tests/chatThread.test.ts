// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1438: the thread a person reads. Risks: a Desk delivery leaks its question
// id and CLI words into the thread, a delivery shows twice, and receipts repeat
// on history ("Sending" on old posts) instead of only on the latest own message.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clockTime, dayLabel, deskDelivery, messageChip, receiptTarget, threadRows } from '../src/components/agents/chatThread.ts'
import { collapseMessages } from '../src/components/agents/sessionMessages.ts'
import type { ProjectMessage } from '../src/lib/agents.ts'

const QUESTION = '55adee55-145a-4c2e-9b1f-0c7d2e1a9f30'
const OTHER = '7d1c0b55-0000-4000-8000-00000000abcd'
const base = Date.parse('2026-10-16T07:40:00')
const message = (id: string, minutes: number, fields: Partial<ProjectMessage> = {}): ProjectMessage => ({
  id, sender_principal_id: 'agent', recipient_principal_id: 'me', to: 'paimos:me', body: 'Ready.', sender_session_id: 'session',
  sent_event_id: 100 + minutes, is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted',
  reply_obligation: 'none', created_at: new Date(base + minutes * 60_000).toISOString(), ...fields,
})
const root = (id: string, minutes: number, question = QUESTION) => message(id, minutes, { body: `Decision Desk question ${question}; read through ask status`, is_action_request: true, expects_reply: true, human_resolution_outcome: 'resolved' })
const answer = (id: string, minutes: number, fields: Record<string, unknown> = {}) => message(id, minutes, {
  sender_principal_id: 'me', recipient_principal_id: 'agent', reply_to: 'root',
  body: JSON.stringify({ type: 'question.answer', question_id: QUESTION, answer_id: 'a1', revision: 2, answer: 'merge now', replaces: '', asker_id: 'k1', ...fields }),
})
const rows = (messages: ProjectMessage[]) => threadRows(collapseMessages(messages), base + 60 * 60_000, 'en')

test('a Desk delivery is recognised by its question root or its answer record, nothing else', () => {
  assert.deepEqual(deskDelivery(root('r', 0)), { kind: 'question', questionId: QUESTION })
  assert.deepEqual(deskDelivery(answer('a', 0)), { kind: 'answer', questionId: QUESTION, correction: false, answer: 'merge now' })
  assert.deepEqual(deskDelivery(answer('a', 0, { type: 'question.correction', answer: '  ' })), { kind: 'answer', questionId: QUESTION, correction: true })
  for (const body of [
    'Please read through ask status for the Decision Desk question.',
    `Decision Desk question ${QUESTION}; read through ask status — and more`,
    'Decision Desk question not-a-uuid; read through ask status',
    JSON.stringify({ type: 'question.answer', question_id: 'not-a-uuid', answer: 'x' }),
    JSON.stringify({ type: 'note', question_id: QUESTION }),
    JSON.stringify([{ type: 'question.answer', question_id: QUESTION }]),
    `{"type":"question.answer","question_id":"${QUESTION}"`,
    `{"type":"question.answer","question_id":"${QUESTION}","answer":"${'x'.repeat(70_000)}"}`,
  ]) assert.equal(deskDelivery({ body }), null, body.slice(0, 60))
})

test('the question root and its answers fold into one card at the first delivery', () => {
  const thread = rows([message('m1', 0), root('root', 4), answer('ans', 4), message('m2', 18), answer('fix', 20, { type: 'question.correction', answer: 'wait for the train' })])
  assert.deepEqual(thread.map(row => row.type), ['day', 'message', 'desk', 'message'])
  const desk = thread[2]!
  assert.equal(desk.type, 'desk')
  if (desk.type !== 'desk') return
  assert.equal(desk.questionId, QUESTION)
  assert.equal(desk.key, 'root')
  assert.deepEqual(desk.members, ['root', 'ans', 'fix'])
  assert.equal(desk.lastEvent, 120)
  assert.equal(desk.answer, 'wait for the train')
  assert.equal(desk.corrected, true)
  // An answer whose root is elsewhere (a held action request) still becomes the card.
  const alone = rows([message('ask', 0, { is_action_request: true }), answer('ans', 1)])
  assert.deepEqual(alone.map(row => row.type), ['day', 'message', 'desk'])
  // Two questions stay two cards.
  assert.equal(rows([root('r1', 0), root('r2', 1, OTHER)]).filter(row => row.type === 'desk').length, 2)
})

test('the thread never shows the question id or CLI words of a Desk delivery', () => {
  const visible = rows([root('root', 0), answer('ans', 0)]).flatMap(row => row.type === 'message' ? [row.message.body] : row.type === 'day' ? [row.label] : [])
  for (const text of visible) {
    assert.doesNotMatch(text, /[0-9a-f]{8}-[0-9a-f]{4}-/i)
    assert.doesNotMatch(text, /ask status|question_id|read through/i)
  }
})

test('day separators open each day: Today, Yesterday, then the date', () => {
  const now = Date.parse('2026-10-16T09:00:00')
  assert.equal(dayLabel(Date.parse('2026-10-16T00:05:00'), now, 'en'), 'Today')
  assert.equal(dayLabel(Date.parse('2026-10-15T23:55:00'), now, 'en'), 'Yesterday')
  assert.equal(dayLabel(Date.parse('2026-10-15T23:55:00'), now, 'de'), 'Gestern')
  assert.equal(dayLabel(Date.parse('2026-10-14T12:00:00'), now, 'en'), 'Wednesday 14 October')
  assert.match(dayLabel(Date.parse('2025-12-31T12:00:00'), now, 'en'), /2025/)
  const thread = threadRows(collapseMessages([message('a', -60 * 24), message('b', 0), message('c', 5)]), base + 60_000, 'en')
  assert.deepEqual(thread.map(row => row.type === 'day' ? row.label : row.key), ['Yesterday', 'a', 'Today', 'b', 'c'])
  assert.equal(clockTime(new Date(base).toISOString()), '07:40')
  assert.equal(clockTime(undefined), '')
})

test('receipts sit only on the latest own message; a Desk answer never takes it', () => {
  const mine = (id: string, minutes: number, fields: Partial<ProjectMessage> = {}) => message(id, minutes, { sender_principal_id: 'me', recipient_principal_id: 'agent', body: `Mine ${id}`, ...fields })
  assert.equal(receiptTarget(rows([mine('o1', 0), message('m1', 1), mine('o2', 2), message('m2', 3)]), 'me'), 'o2')
  assert.equal(receiptTarget(rows([mine('o1', 0), root('root', 1), answer('ans', 1)]), 'me'), 'o1')
  assert.equal(receiptTarget(rows([mine('o1', 0), mine('local', 4, { optimistic: true, sent_event_id: Number.MAX_SAFE_INTEGER })]), 'me'), 'local')
  assert.equal(receiptTarget(rows([message('m1', 0)]), 'me'), undefined)
})

test('a message carries at most one chip', () => {
  const words = { actionRequest: 'Action request', awaitingReply: 'Awaiting reply', resolved: 'Resolved', dismissed: 'Dismissed', resolvedState: 'resolved', dismissedState: 'dismissed', awaitingState: 'awaiting reply' }
  const chip = (fields: Partial<ProjectMessage>, answered = false) => messageChip({ is_action_request: false, reply_obligation: 'none', human_resolution_outcome: null, answered, ...fields }, words)
  assert.equal(chip({}), '')
  assert.equal(chip({ is_action_request: true }), 'Action request')
  assert.equal(chip({ is_action_request: true, human_resolution_outcome: 'resolved', reply_obligation: 'open' }), 'Action request · resolved')
  assert.equal(chip({ is_action_request: true, human_resolution_outcome: 'dismissed' }), 'Action request · dismissed')
  assert.equal(chip({ is_action_request: true, reply_obligation: 'open' }), 'Action request · awaiting reply')
  assert.equal(chip({ is_action_request: true, reply_obligation: 'open' }, true), 'Action request')
  assert.equal(chip({ reply_obligation: 'open' }), 'Awaiting reply')
  assert.equal(chip({ human_resolution_outcome: 'resolved' }), 'Resolved')
})
