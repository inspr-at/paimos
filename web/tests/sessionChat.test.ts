// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { awaitsInboxHook, hookDeliveryNotice, hookNoticeVisible, hookReceipts, initialTab, keepFailedReadMark, loadReadMark, loadTab, markerFromServer, nearBottom, preferReadMark, queueReadMark, saveReadMark, saveTab, sessionBoundSends, statusDone, statusLabel, statusTip, unreadGroups } from '../src/components/agents/sessionChat.ts'
import type { HarnessSession } from '../src/lib/agents.ts'
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

test('the server marker wins when it is ahead, and local stands when the server has none', () => {
  const local = { event: 5, id: 'm5', at: 1 }
  const server = { event: 8, id: 'm8', at: 2 }
  assert.deepEqual(preferReadMark(local, server), server)
  assert.deepEqual(preferReadMark(server, local), server)
  assert.equal(preferReadMark(local, null), local)
  assert.equal(preferReadMark(null, null), null)
  assert.deepEqual(markerFromServer({ last_read_message_id: 'm8', last_read_event_id: 8, read_at: '2026-09-29T06:00:00Z' })?.event, 8)
  assert.equal(markerFromServer({ last_read_message_id: null, last_read_event_id: null, read_at: null }), null)
  assert.equal(markerFromServer(null), null)
})

test('a flush keeps the highest mark and a failed send stays for the next one', () => {
  const low = { event: 2, id: 'a', at: 1 }
  const high = { event: 5, id: 'b', at: 2 }
  const later = { event: 9, id: 'c', at: 3 }
  assert.deepEqual(queueReadMark(null, low), low)
  assert.deepEqual(queueReadMark(low, high), high)
  assert.deepEqual(queueReadMark(high, low), high)
  assert.deepEqual(keepFailedReadMark(null, high), high)
  assert.deepEqual(keepFailedReadMark(later, high), later)
  assert.deepEqual(keepFailedReadMark(low, high), high)
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

test('delivery wording is short and specific (AEON-280)', () => {
  const at = (iso: string) => iso.slice(11, 16)
  const base = { message_id: 'm', delivered_at: null, read_at: null, deliver_by: '2026-09-29T06:15:00Z' }
  const sent = { ...base, status: 'sent' as const }
  const delivered = { ...base, status: 'delivered' as const, delivered_at: '2026-09-29T06:09:00Z' }
  const read = { ...base, status: 'read' as const, delivered_at: '2026-09-29T06:09:00Z', read_at: '2026-09-29T06:10:00Z' }
  assert.deepEqual([sent, delivered, read].map(statusLabel), ['Sent', 'Delivered', 'Read'])
  assert.equal(statusTip(sent, at), 'Sent · waiting for the session to pick it up')
  assert.equal(statusTip(delivered, at), 'Delivered to the session · 06:09')
  assert.equal(statusTip(read, at), 'Read by the session · 06:10')
  for (const [reason, text] of [['session_ended', 'the session ended'], ['no_listener', 'the session was not listening'], ['deadline', 'not confirmed in time'], ['attempts', 'every attempt failed'], ['http_error', 'http error']]) {
    assert.equal(statusLabel({ ...base, status: 'not_delivered', reason }), `Not delivered · ${text}`)
  }
  assert.equal(statusDone(sent), false)
  assert.equal(statusDone(read), true)
  assert.equal(statusDone({ ...base, status: 'not_delivered', reason: 'deadline' }), true)
})

test('a live session without a hook binding waits for the inbox hook (AEON-369)', () => {
  const live = { id: 's', project_id: 'p', agent_principal_id: 'a', run_id: null, ticket_node_id: null, work_order_id: null, parent_harness_session_id: null, harness: 'claude', host: 'h', management_mode: 'unmanaged', role: 'worker', work_shape: 'unknown', advertised_capabilities: [], phase: 'working', activity: 'idle', activity_sequence: 1, revision: 1, heartbeat_at: null, stopped_at: null, stop_reason: null, created_at: '2026-09-29T00:00:00Z' } satisfies HarnessSession
  assert.equal(awaitsInboxHook(live), true)
  assert.equal(awaitsInboxHook({ ...live, has_vendor_session_ref: true }), false)
  assert.equal(awaitsInboxHook({ ...live, inbox_seen_via: 'hook' }), false)
  assert.equal(awaitsInboxHook({ ...live, inbox_seen_via: 'drain' }), true)
  assert.equal(awaitsInboxHook({ ...live, phase: 'stopped', stopped_at: '2026-09-29T00:00:00Z' }), false)
  assert.equal(awaitsInboxHook({ ...live, archived_at: '2026-09-29T00:00:00Z' }), false)
  assert.equal(hookDeliveryNotice, "Delivered when the session's inbox hook runs.")
  assert.equal(hookDeliveryNotice.toLowerCase().includes("delivered when the session's inbox hook runs"), true)
})

test('the hook notice follows each pending send, including a terminal failure (AEON-369)', () => {
  const base = { delivered_at: null, read_at: null, deliver_by: '2026-09-29T06:15:00Z' }
  const sent = { ...base, message_id: 'new', status: 'sent' as const }
  const delivered = { ...base, message_id: 'old', status: 'delivered' as const, delivered_at: '2026-09-29T06:09:00Z' }
  const read = { ...delivered, status: 'read' as const, read_at: '2026-09-29T06:10:00Z' }
  const failed = { ...base, message_id: 'new', status: 'not_delivered' as const, reason: 'deadline' }
  // An older delivered or read receipt is not in the pending list, so it cannot hide a new wait.
  const afterDelivered = hookReceipts(['new'], { old: delivered, new: sent })
  assert.deepEqual(afterDelivered, { waiting: ['new'], failed: [] })
  assert.equal(hookNoticeVisible(true, afterDelivered), true)
  const afterRead = hookReceipts(['new'], { old: read, new: sent })
  assert.equal(hookNoticeVisible(true, afterRead), true)
  // A receipt that has not arrived yet is still waiting. Delivered and read are finished.
  assert.deepEqual(hookReceipts(['a', 'b', 'c'], { b: delivered, c: read }), { waiting: ['a'], failed: [] })
  const terminal = hookReceipts(['new'], { new: failed })
  assert.deepEqual(terminal.waiting, [])
  assert.equal(terminal.failed[0]?.reason, 'deadline')
  assert.equal(hookNoticeVisible(true, terminal), false)
  assert.equal(statusLabel(failed), 'Not delivered · not confirmed in time')
  // One failure does not hide a send that is still pending.
  const mixed = hookReceipts(['old-fail', 'new'], { 'old-fail': { ...failed, message_id: 'old-fail' }, new: sent })
  assert.deepEqual(mixed.waiting, ['new'])
  assert.equal(mixed.failed.length, 1)
  assert.equal(hookNoticeVisible(true, mixed), true)
  assert.equal(hookNoticeVisible(false, afterDelivered), false)
  assert.equal(hookNoticeVisible(true, hookReceipts([], { old: delivered })), false)
})

test('a reload rebuilds the hook wait from the loaded thread and its receipts (AEON-369)', () => {
  const send = (id: string, session = 's', sender = 'me') => ({ id, sender_principal_id: sender, recipient_session_id: session })
  const thread = [
    send('fail'), send('done'), send('wait'),
    send('elsewhere', 'other'), send('from-agent', 's', 'agent'),
    { id: 'unbound', sender_principal_id: 'me' },
  ]
  // Nothing in memory: the thread is the only source, as after a reload.
  const ids = sessionBoundSends(thread, 's', 'me')
  assert.deepEqual(ids, ['fail', 'done', 'wait'])
  assert.deepEqual(sessionBoundSends(thread, 's', ''), [])
  const base = { delivered_at: null, read_at: null, deliver_by: '2026-09-29T06:15:00Z' }
  const fail = { ...base, message_id: 'fail', status: 'not_delivered' as const, reason: 'deadline' }
  const done = { ...base, message_id: 'done', status: 'delivered' as const, delivered_at: '2026-09-29T06:09:00Z' }
  const waiting = { ...base, message_id: 'wait', status: 'sent' as const }
  const before = hookReceipts(ids, { fail, done, wait: waiting })
  assert.deepEqual(before, { waiting: ['wait'], failed: [fail] })
  assert.equal(hookNoticeVisible(true, before), true)
  // A missing receipt is still outstanding. Delivering the pending one clears the notice.
  assert.deepEqual(hookReceipts(ids, { fail, done }).waiting, ['wait'])
  const after = hookReceipts(ids, { fail, done, wait: { ...done, message_id: 'wait' } })
  assert.deepEqual(after.waiting, [])
  assert.equal(hookNoticeVisible(true, after), false)
})

test('near the bottom allows a small slack', () => {
  assert.equal(nearBottom({ scrollHeight: 1000, scrollTop: 570, clientHeight: 400 }), true)
  assert.equal(nearBottom({ scrollHeight: 1000, scrollTop: 500, clientHeight: 400 }), false)
})
