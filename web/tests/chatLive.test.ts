// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1071: the live chat turn. Risks: deltas that leak into the next turn or
// outlive the saved reply, unbounded live memory, silent holes in the text, a
// composer that offers Send now without native steering, and a stream that
// keeps retrying a refused thread.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { applyLiveFrame, chatFinals, elapsed, followChatLive, liveQueue, liveTextLimit, liveToolLimit, settleStop, settled, type LiveSource, type LiveTurn } from '../src/components/agents/chatLive.ts'
import { sessionChatFlags } from '../src/components/agents/sessionChat.ts'

const chunk = (text: string, dropped = 0) => JSON.stringify({ type: 'update', update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } }, dropped_events: dropped, source_sequence: 1 })
const tool = (id: string, status: string, title = 'Read file') => JSON.stringify({ type: 'update', update: { sessionUpdate: 'tool_call', toolCallId: id, title, status }, dropped_events: 0 })
const state = (value: string) => JSON.stringify({ type: 'update', update: { sessionUpdate: 'state', state: value }, dropped_events: 0 })

function run(frames: string[], start: LiveTurn | null = null, before = 'final-0', at = 1000) {
  let turn = start, dropped: number | undefined
  const hints: string[] = []
  for (const [i, data] of frames.entries()) {
    const applied = applyLiveFrame(turn, data, at + i * 100, before, dropped)
    turn = applied.turn
    if (applied.dropped !== undefined) dropped = applied.dropped
    if (applied.hint) hints.push(applied.hint)
  }
  return { turn, hints }
}

test('deltas build one turn, idle ends it, and the saved reply replaces it', () => {
  const { turn } = run([state('running'), chunk('Hello'), tool('t1', 'in_progress'), chunk(', world'), tool('t1', 'completed'), tool('t2', 'in_progress', 'Run tests'), state('idle')])
  assert.ok(turn)
  assert.equal(turn.text, 'Hello, world')
  assert.equal(turn.ended, true)
  assert.equal(turn.state, 'idle')
  assert.deepEqual(turn.tools.map(item => [item.id, item.status, item.ended !== undefined]), [['t1', 'completed', true], ['t2', 'in_progress', true]])
  // Ended but not yet replaced: the text stays on screen until a newer agent
  // message than the one before the turn arrives.
  assert.equal(settled(turn, 'final-0'), false)
  assert.equal(settled(turn, 'final-1'), true)
  assert.equal(settled({ ...turn, ended: false }, 'final-1'), false)
  // Activity after the end starts a fresh turn with no carried text.
  const next = run([chunk('Next')], turn, 'final-1').turn!
  assert.equal(next.text, 'Next')
  assert.equal(next.tools.length, 0)
  assert.equal(next.before, 'final-1')
  assert.equal(next.ended, false)
})

test('requires_action holds through text until idle; hints never touch the turn', () => {
  const { turn, hints } = run([chunk('Need approval'), state('requires_action'), chunk(' for the deploy'), JSON.stringify({ type: 'message', message_id: 'm' }), JSON.stringify({ type: 'receipt' }), JSON.stringify({ type: 'read_marker' })])
  assert.equal(turn!.state, 'requires_action')
  assert.equal(turn!.text, 'Need approval for the deploy')
  assert.deepEqual(hints, ['message', 'receipt', 'read_marker'])
  // Unknown or malformed frames are ignored without throwing.
  for (const bad of ['', 'null', '{"type":"update"}', '{"type":"update","update":{"sessionUpdate":"agent_thought_chunk"}}', '{"type":"update","update":{"sessionUpdate":"state","state":"exploded"}}']) {
    assert.equal(applyLiveFrame(turn, bad, 0, 'x', 0).turn, turn)
  }
  // An idle without a turn creates nothing.
  assert.equal(run([state('idle')]).turn, null)
})

test('live text and tools stay bounded and say when they were cut', () => {
  const big = 'x'.repeat(liveTextLimit - 10)
  const { turn } = run([chunk(big), chunk('0123456789ABCDEF'), chunk('more')])
  assert.equal(turn!.text.length, liveTextLimit)
  assert.equal(turn!.text.endsWith('0123456789'), true)
  assert.equal(turn!.truncated, true)
  const tools = run(Array.from({ length: liveToolLimit + 5 }, (_, i) => tool(`t${i}`, 'completed'))).turn!
  assert.equal(tools.tools.length, liveToolLimit)
  assert.equal(tools.gap, true)
})

test('a dropped-frame count above the baseline marks the hole', () => {
  // The first count on a connection is only the baseline.
  assert.equal(run([chunk('a', 7), chunk('b', 7)]).turn!.gap, false)
  assert.equal(run([chunk('a', 7), chunk('b', 9)]).turn!.gap, true)
})

test('the working timer is tabular m:ss', () => {
  assert.equal(elapsed(0), '0:00')
  assert.equal(elapsed(42_900), '0:42')
  assert.equal(elapsed(61_000), '1:01')
  assert.equal(elapsed(-5), '0:00')
})

test('composer flags mirror the agentd capability snapshot', () => {
  const caps = ['managed_control_v1', 'steer', 'interrupt']
  assert.deepEqual(sessionChatFlags({ harness: 'claude', management_mode: 'managed', advertised_capabilities: caps }), { steer: 'native', interrupt: true, deltas: true })
  assert.deepEqual(sessionChatFlags({ harness: 'codex', management_mode: 'managed', advertised_capabilities: ['steer'] }), { steer: 'native', interrupt: false, deltas: true })
  assert.deepEqual(sessionChatFlags({ harness: 'pi', management_mode: 'managed', advertised_capabilities: caps }), { steer: 'next-step', interrupt: true, deltas: true })
  assert.deepEqual(sessionChatFlags({ harness: 'claude', management_mode: 'managed', advertised_capabilities: ['interrupt'] }), { steer: 'queue', interrupt: true, deltas: true })
  assert.deepEqual(sessionChatFlags({ harness: 'gemini', management_mode: 'managed', advertised_capabilities: caps }), { steer: 'queue', interrupt: false, deltas: false })
})

class FakeSource implements LiveSource {
  listeners = new Map<string, ((event: MessageEvent<string>) => void)[]>()
  onerror: ((event: Event) => unknown) | null = null
  readyState = 0
  closed = false
  url: string
  constructor(url: string) { this.url = url }
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void) { this.listeners.set(type, [...this.listeners.get(type) ?? [], listener]) }
  emit(type: string, data = '') { for (const listener of this.listeners.get(type) ?? []) listener({ data } as MessageEvent<string>) }
  close() { this.closed = true; this.readyState = 2 }
}
const settle = () => new Promise(resolve => setImmediate(resolve))

test('the follower opens only a found thread and resyncs on refusal or handover', async () => {
  {
    // Backoff runs on an injected clock; `tick` fires every due retry.
    let clock = 0
    const due: { at: number; run: () => void; live: boolean }[] = []
    const later = (run: () => void, ms: number) => { const entry = { at: clock + ms, run, live: true }; due.push(entry); return () => { entry.live = false } }
    const tick = (ms: number) => { clock += ms; for (const entry of due.splice(0)) { if (entry.live && entry.at <= clock) entry.run(); else if (entry.live) due.push(entry) } }
    const sources: FakeSource[] = []
    const looked: string[] = []
    let answer: { status: number; thread?: string } = { status: 404 }
    const frames: string[] = []
    let resyncs = 0
    const bound: string[] = []
    const follow = followChatLive('s-1', { frame: data => frames.push(data), resync: () => { resyncs++ }, thread: id => bound.push(id), attached: () => {} }, {
      lookup: async id => { looked.push(id); return answer },
      open: url => { const source = new FakeSource(url); sources.push(source); return source },
      later,
    })
    await settle()
    // Unbound or disabled: no stream and no error.
    assert.equal(sources.length, 0)
    answer = { status: 200, thread: 'th/1' }
    follow.retry()
    await settle()
    assert.equal(sources.length, 1)
    assert.equal(sources[0]!.url, '/api/chat-threads/th%2F1/live')
    sources[0]!.emit('ready', '{}')
    sources[0]!.emit('chat', chunk('hi'))
    assert.deepEqual(frames, [chunk('hi')])
    // A transient drop resumes by itself (Last-Event-ID); nothing reopens.
    sources[0]!.onerror?.(new Event('error'))
    assert.equal(sources.length, 1)
    assert.equal(resyncs, 0)
    // Handover: resync, ask again, and follow the successor's thread.
    answer = { status: 200, thread: 'th-2' }
    sources[0]!.emit('resync', '{"reason":"binding_changed"}')
    await settle()
    assert.equal(resyncs, 1)
    assert.equal(sources[0]!.closed, true)
    assert.equal(sources.length, 2)
    assert.equal(sources[1]!.url, '/api/chat-threads/th-2/live')
    // Each found thread is announced, so the saved replies load from it.
    assert.deepEqual(bound, ['th/1', 'th-2'])
    // A stale source can no longer deliver.
    sources[0]!.emit('chat', chunk('stale'))
    assert.equal(frames.length, 1)
    // A refused stream closes for good: drop interim, back off, give up after four.
    for (let attempt = 1; attempt <= 5; attempt++) {
      const current = sources.at(-1)!
      current.readyState = 2
      current.onerror?.(new Event('error'))
      tick(30_000)
      await settle()
    }
    assert.equal(resyncs, 6)
    assert.equal(sources.length, 6)
    tick(120_000)
    await settle()
    assert.equal(sources.length, 6)
    follow.stop()
    follow.retry()
    await settle()
    assert.equal(sources.length, 6)
    assert.ok(looked.every(id => id === 's-1'))
  }
})

// Risk (AEON-1071 gate): a paused animation frame let the queue fill and
// silently drop the turn's end and the saved-reply hint, so the turn never
// settled.
test('a full frame queue applies at once and never drops the end of a turn', () => {
  const scheduled: (() => void)[] = []
  const cancelled: number[] = []
  const batches: string[][] = []
  const queue = liveQueue(frames => batches.push(frames), { schedule: run => scheduled.push(run), cancel: handle => cancelled.push(handle), limit: 4 })
  const sent = [state('running'), chunk('a'), chunk('b'), chunk('c'), chunk('d'), state('idle'), JSON.stringify({ type: 'message', message_id: 'final-1' })]
  // The animation frame never fires while the tab is hidden.
  for (const data of sent) queue.push(data)
  assert.deepEqual(batches, [sent.slice(0, 4)])
  assert.equal(cancelled.length, 1)
  scheduled.at(-1)!()
  assert.deepEqual(batches.flat(), sent)
  const { turn, hints } = run(batches.flat())
  assert.equal(turn?.ended, true)
  assert.equal(turn?.text, 'abcd')
  assert.deepEqual(hints, ['message'])
  assert.equal(settled(turn, 'final-1'), true)
  // Clearing drops what is queued and its pending frame.
  queue.push(chunk('stale'))
  queue.clear()
  assert.equal(cancelled.length, 2)
  assert.equal(batches.length, 2)
})

// Risk (AEON-1071 gate): the turn ended before the interrupt was refused, and
// the block still said the person stopped it.
test('only an applied interrupt may say the person stopped the turn', () => {
  const ended = { ...run([state('running'), chunk('x'), state('idle')]).turn!, stop: 'requested' as const }
  assert.equal(settleStop(ended, 'withdrawn')?.stop, 'none')
  assert.equal(settleStop(ended, 'withdrawn')?.ended, true)
  assert.equal(settleStop(ended, 'applied')?.stop, 'applied')
  const running = { ...run([state('running')]).turn!, stop: 'requested' as const }
  assert.equal(settleStop(running, 'withdrawn')?.stop, 'none')
  // A fresh turn carries no claim, and a settled claim is not rewritten.
  assert.equal(run([state('running')]).turn!.stop, 'none')
  assert.equal(settleStop({ ...ended, stop: 'applied' }, 'withdrawn')?.stop, 'applied')
  assert.equal(settleStop(null, 'applied'), null)
})

// Risk (AEON-1071 gate): chat-native finals live only in the chat thread, so
// reading the project message list never replaced the live block.
test('saved chat replies from the agent join the thread and settle the live turn', () => {
  const page = [
    { message: { message_id: 'in-1', sent_event_position: '40', body: 'input', sender_principal_id: 'person-1', created_at: '2026-10-10T08:00:00Z' } },
    { message: { message_id: 'final-2', sent_event_position: '41', body: 'Final answer', reply_to: 'in-1', sender_principal_id: 'agent-1', created_at: '2026-10-10T08:00:05Z' } },
    { message: { message_id: 'other', sent_event_position: '42', body: 'not ours', sender_principal_id: 'agent-2' } },
    { message: { message_id: 'broken', sent_event_position: 'x', body: 'bad', sender_principal_id: 'agent-1' } },
  ]
  const finals = chatFinals(page, 'agent-1', 'session-1', 'person-1', 'release-lead')
  assert.deepEqual(finals.map(message => [message.id, message.sent_event_id, message.body, message.reply_to, message.sender_session_id, message.sender_label, message.recipient_principal_id]),
    [['final-2', 41, 'Final answer', 'in-1', 'session-1', 'release-lead', 'person-1']])
  assert.equal(chatFinals(page, '', 'session-1', 'person-1', 'x').length, 0)
  const { turn } = run([state('running'), chunk('live'), state('idle')], null, 'final-1')
  assert.equal(settled(turn, finals.at(-1)!.id), true)
})
