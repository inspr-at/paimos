// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { initialTab, loadReadMark, loadTab, nearBottom, receiptTip, saveReadMark, saveTab, unreadGroups } from '../src/components/agents/sessionChat.ts'
import { collapseMessages } from '../src/components/agents/sessionMessages.ts'
import type { ProjectMessage } from '../src/lib/agents.ts'

function memory() {
  const data = new Map<string, string>()
  return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => { data.set(k, v) }, data }
}
const broken = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
const message = (id: string, event: number, sender = 'agent', body = `Body ${id}`): ProjectMessage => ({
  id, sender_principal_id: sender, recipient_principal_id: 'person', to: 'paimos:person', body, sender_session_id: 's1', sent_event_id: event,
  is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none',
  created_at: new Date(1_800_000_000_000 + event * 1000).toISOString(),
})

test('the last tab is remembered per viewer and a deep link wins', () => {
  const store = memory()
  assert.equal(loadTab(store), 'overview')
  saveTab('messages', store)
  assert.equal(loadTab(store), 'messages')
  assert.equal(initialTab(undefined, store), 'messages')
  assert.equal(initialTab('overview', store), 'overview')
  assert.equal(initialTab(['messages'], memory()), 'messages')
  assert.equal(initialTab('bogus', memory()), 'overview')
  // Blocked storage never breaks the panel.
  assert.equal(loadTab(broken), 'overview')
  assert.doesNotThrow(() => saveTab('messages', broken))
  assert.equal(loadReadMark('me', 's1', broken), null)
  assert.doesNotThrow(() => saveReadMark('me', 's1', 5, 'm5', broken))
})

test('the read watermark only moves forward and is keyed by viewer and session', () => {
  const store = memory()
  assert.equal(loadReadMark('me', 's1', store), null)
  saveReadMark('me', 's1', 5, 'm5', store, 1)
  saveReadMark('me', 's1', 3, 'm3', store, 2)
  assert.deepEqual(loadReadMark('me', 's1', store), { event: 5, id: 'm5', at: 1 })
  assert.equal(loadReadMark('other', 's1', store), null)
  assert.equal(loadReadMark('me', 's2', store), null)
  for (let i = 0; i < 205; i++) saveReadMark('me', `x${i}`, 1, 'm', store, 10 + i)
  const kept = Object.keys(JSON.parse(store.data.get('aeon.session-read.v1')!))
  assert.equal(kept.length, 200)
  assert.equal(kept.includes('me:s1'), false)
})

test('unread counts other people’s posts above the watermark, duplicates by their newest post', () => {
  const groups = collapseMessages([message('1', 1), message('2', 2, 'me'), message('3', 3), message('4', 4, 'agent', 'Body 3')])
  assert.deepEqual(groups.map(g => [g.id, g.last_event]), [['1', 1], ['2', 2], ['3', 4]])
  assert.deepEqual(unreadGroups(groups, 'me', null, false).map(g => g.id), ['1', '3'])
  assert.deepEqual(unreadGroups(groups, 'me', { event: 3, id: '3', at: 0 }, false).map(g => g.id), ['3'])
  assert.deepEqual(unreadGroups(groups, 'me', { event: 4, id: '4', at: 0 }, false), [])
  // A new browser does not flag an ended session's history.
  assert.deepEqual(unreadGroups(groups, 'me', null, true), [])
})

test('receipt wording is short and specific', () => {
  const at = (iso: string) => iso.slice(11, 16)
  assert.equal(receiptTip({ message_id: 'm', state: 'queued', handed_off_at: null, failure_reason: '' }, at), 'Sent · waiting for the session to pick it up')
  assert.equal(receiptTip({ message_id: 'm', state: 'handed_off', handed_off_at: '2026-09-29T06:10:00Z', failure_reason: '' }, at), 'Picked up by the session · 06:10')
  assert.equal(receiptTip({ message_id: 'm', state: 'failed', handed_off_at: null, failure_reason: 'target_missing' }, at), 'Not delivered · target missing')
})

test('near the bottom allows a small slack', () => {
  assert.equal(nearBottom({ scrollHeight: 1000, scrollTop: 570, clientHeight: 400 }), true)
  assert.equal(nearBottom({ scrollHeight: 1000, scrollTop: 500, clientHeight: 400 }), false)
})
