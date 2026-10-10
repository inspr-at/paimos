// SPDX-License-Identifier: AGPL-3.0-only
// Live chat turn (AEON-1071): the person's open thread follows the
// GET /api/chat-threads/{id}/live stream (AEON-1070). Under transcript policy A
// text deltas and tool activity exist only here, in memory, for the open view;
// they are never stored, logged or replayed. The persisted final message
// replaces the live block once it lands in the thread.
import { parseJson } from '../../lib/json.ts'
import type { ChatHistoryMessage, ProjectMessage } from '../../lib/agents.ts'
import type { ReadMark } from './sessionChat.ts'

export type LiveState = 'running' | 'idle' | 'requires_action'
export type ToolStatus = 'in_progress' | 'completed' | 'failed'
export interface LiveTool { id: string; title: string; status: ToolStatus; started: number; ended?: number }
export interface LiveTurn {
  text: string
  // Text beyond the bound is cut, and the block says so.
  truncated: boolean
  tools: LiveTool[]
  state: LiveState
  // The relay skipped frames for this viewer: the text has holes.
  gap: boolean
  started: number
  ended: boolean
  // The agent message id that was newest when the turn began. A newer one
  // after the turn ended is its final reply. Undefined while the thread's
  // saved history is still loading: an older reply arriving then is no proof.
  before?: string
  // Saved messages the stream announced during this turn. One of them in the
  // thread from the agent is the turn's own final reply.
  finals: string[]
  // 'requested' while an interrupt is pending, 'applied' once the harness
  // confirmed it. Only 'applied' may say the person stopped the turn.
  stop: LiveStop
}
export type LiveStop = 'none' | 'requested' | 'applied'
export type LiveHint = 'message' | 'receipt' | 'read_marker'

// Display bounds, independent of the server's replay window.
export const liveTextLimit = 256 * 1024
export const liveToolLimit = 64

interface Update { sessionUpdate?: unknown; content?: { type?: unknown; text?: unknown }; toolCallId?: unknown; title?: unknown; status?: unknown; state?: unknown }
interface Frame { type?: unknown; update?: Update; dropped_events?: unknown; message_id?: unknown }

const newTurn = (now: number, before: string | undefined, state: LiveState = 'running'): LiveTurn =>
  ({ text: '', truncated: false, tools: [], state, gap: false, started: now, ended: false, before, finals: [], stop: 'none' })
const finalLimit = 16

export interface Applied { turn: LiveTurn | null; hint?: LiveHint; dropped?: number }
// One chat frame. `dropped` is the relay's cumulative per-session overflow
// count; the caller passes back the last one it saw, or undefined on a fresh
// connection, whose first count is only the baseline. `before` is undefined
// while the saved history is not yet known.
export function applyLiveFrame(turn: LiveTurn | null, data: string, now: number, before: string | undefined, dropped?: number): Applied {
  let frame: Frame | null
  try { frame = parseJson(data) as Frame | null } catch { return { turn } }
  if (!frame || typeof frame !== 'object') return { turn }
  if (frame.type === 'message') {
    // A saved message announced while a turn is on screen may be its final.
    const id = typeof frame.message_id === 'string' ? frame.message_id : ''
    if (!turn || !id || turn.finals.includes(id)) return { turn, hint: 'message' }
    return { turn: { ...turn, finals: [...turn.finals, id].slice(-finalLimit) }, hint: 'message' }
  }
  if (frame.type === 'receipt' || frame.type === 'read_marker') return { turn, hint: frame.type }
  if (frame.type !== 'update' || !frame.update || typeof frame.update !== 'object') return { turn }
  const update = frame.update
  const count = typeof frame.dropped_events === 'number' && Number.isFinite(frame.dropped_events) ? frame.dropped_events : dropped
  const skipped = dropped !== undefined && count !== undefined && count > dropped
  // A finished turn stays on screen until its final reply lands. New
  // activity starts the next turn.
  const open = () => !turn || turn.ended ? newTurn(now, before) : { ...turn }
  if (update.sessionUpdate === 'agent_message_chunk') {
    const text = update.content?.type === 'text' && typeof update.content.text === 'string' ? update.content.text : ''
    if (!text) return { turn, dropped: count }
    const next = open()
    if (skipped && next.text) next.gap = true
    if (next.truncated) return { turn: next, dropped: count }
    const room = liveTextLimit - next.text.length
    next.text += text.length > room ? text.slice(0, room) : text
    next.truncated = text.length > room
    if (next.state !== 'requires_action') next.state = 'running'
    return { turn: next, dropped: count }
  }
  if (update.sessionUpdate === 'tool_call') {
    const id = typeof update.toolCallId === 'string' ? update.toolCallId : ''
    const status: ToolStatus = update.status === 'completed' || update.status === 'failed' ? update.status : 'in_progress'
    if (!id) return { turn, dropped: count }
    const next = open()
    if (skipped) next.gap = true
    const index = next.tools.findIndex(tool => tool.id === id)
    const title = typeof update.title === 'string' && update.title ? update.title : index >= 0 ? next.tools[index]!.title : ''
    if (index >= 0) {
      const held = next.tools[index]!
      next.tools = next.tools.map((tool, i) => i === index ? { ...held, title, status, ended: status === 'in_progress' ? undefined : held.ended ?? now } : tool)
    } else if (next.tools.length < liveToolLimit) {
      next.tools = [...next.tools, { id, title, status, started: now, ...(status === 'in_progress' ? {} : { ended: now }) }]
    } else next.gap = true
    return { turn: next, dropped: count }
  }
  if (update.sessionUpdate === 'state') {
    const state = update.state
    if (state !== 'running' && state !== 'idle' && state !== 'requires_action') return { turn, dropped: count }
    if (state === 'idle') {
      if (!turn || turn.ended) return { turn, dropped: count }
      // Tools the turn never closed end with it.
      return { turn: { ...turn, state, ended: true, tools: turn.tools.map(tool => tool.status === 'in_progress' ? { ...tool, ended: now } : tool) }, dropped: count }
    }
    const next = open()
    next.state = state
    return { turn: next, dropped: count }
  }
  return { turn, dropped: count }
}

// The live block gives way to the persisted reply once the turn ended and its
// final is in the thread: a message the stream announced during the turn, or
// an agent message newer than the one before it. Without a known history
// baseline only the announced final counts, so a late read of older history
// never dismisses the turn.
export function settled(turn: LiveTurn | null, latestAgentMessage: string, agentMessages: ReadonlySet<string> = new Set([latestAgentMessage])) {
  if (!turn || !turn.ended) return false
  if (turn.finals.some(id => agentMessages.has(id))) return true
  return turn.before !== undefined && !!latestAgentMessage && latestAgentMessage !== turn.before
}
// The first saved-history read establishes the baseline of a turn that is
// still running. A turn that already ended waits for its announced final.
export const rebaseTurn = (turn: LiveTurn | null, latestAgentMessage: string): LiveTurn | null =>
  turn && turn.before === undefined && !turn.ended ? { ...turn, before: latestAgentMessage } : turn

export const liveWorking = (turn: LiveTurn | null) => !!turn && !turn.ended

// The interrupt's fate decides the stop claim. A refusal, or no interrupt on
// record at all, withdraws it, also when the turn has meanwhile ended on its
// own; an applied interrupt confirms it.
export function settleStop(turn: LiveTurn | null, outcome: 'applied' | 'withdrawn'): LiveTurn | null {
  if (!turn || turn.stop !== 'requested') return turn
  return { ...turn, stop: outcome === 'applied' ? 'applied' : 'none' }
}

// Frames wait for the next animation frame. A full queue is applied at once
// instead of dropping frames: a paused tab must not lose the end of a turn or
// a saved-reply hint.
export const liveQueueLimit = 256
export interface LiveQueueDeps { schedule: (run: () => void) => number; cancel: (handle: number) => void; limit?: number }
export function liveQueue(apply: (frames: string[]) => void, deps: LiveQueueDeps) {
  let frames: string[] = []
  let handle: number | undefined
  const limit = deps.limit ?? liveQueueLimit
  const flush = () => {
    if (handle !== undefined) { deps.cancel(handle); handle = undefined }
    const batch = frames
    frames = []
    if (batch.length) apply(batch)
  }
  return {
    push(data: string) {
      frames.push(data)
      if (frames.length >= limit) flush()
      else if (handle === undefined) handle = deps.schedule(() => { handle = undefined; flush() })
    },
    clear() {
      if (handle !== undefined) { deps.cancel(handle); handle = undefined }
      frames = []
    },
  }
}

// The saved chat-native messages of the bound thread between the agent and
// the viewer, shaped like the session's project messages so the thread
// renders them in place. They are stored only in the chat thread, never in the
// project message list, so each keeps its thread: replies and read evidence
// for it go through the chat routes.
export function chatFinals(items: readonly { message: ChatHistoryMessage; person_read_state?: string }[], agent: string, session: string, viewer: string, label: string, thread: string): ProjectMessage[] {
  return items.flatMap(({ message, person_read_state }) => {
    const position = Number(message.sent_event_position)
    const sender = message.sender_principal_id
    if (!agent || !viewer || (sender !== agent && sender !== viewer) || !Number.isSafeInteger(position) || position <= 0) return []
    const fromAgent = sender === agent
    return [{
      id: message.message_id, sender_principal_id: sender, recipient_principal_id: fromAgent ? viewer : agent, to: '', body: message.body, reply_to: message.reply_to ?? null,
      sent_event_id: position, created_at: message.created_at, ...(fromAgent ? { sender_session_id: session, sender_label: label } : { recipient_session_id: session }),
      is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none',
      chat_thread: thread, chat_seen: person_read_state === 'seen',
    } satisfies ProjectMessage]
  })
}
// A chat input the viewer just sent, as the thread shows it. The outbox
// answer names neither author nor time.
export function chatSent(message: ChatHistoryMessage, viewer: string, agent: string, session: string, thread: string, at: string): ProjectMessage {
  return {
    id: message.message_id, sender_principal_id: viewer, recipient_principal_id: agent, recipient_session_id: session, to: '', body: message.body, reply_to: message.reply_to ?? null,
    sent_event_id: Number(message.sent_event_position), created_at: message.created_at ?? at,
    is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none', chat_thread: thread,
  }
}

// Read state. The session's read marker accepts only its project messages, so
// a watermark on a chat-native message is written as the newest project
// message at or before it. The chat message itself goes to its thread's
// read marker.
const projectMessage = (message: ProjectMessage | undefined) => !!message && !message.chat_thread && !message.optimistic
export function sessionReadMark(mark: ReadMark, messages: readonly ProjectMessage[]): ReadMark | null {
  const own = messages.find(message => message.id === mark.id)
  const at = messages.find(message => message.sent_event_id === mark.event)
  if ((!own || projectMessage(own)) && (!at || projectMessage(at))) return mark
  let best: ProjectMessage | undefined
  for (const message of messages) if (projectMessage(message) && message.sent_event_id <= mark.event && (!best || message.sent_event_id > best.sent_event_id)) best = message
  return best ? { event: best.sent_event_id, id: best.id, at: mark.at } : null
}
// The chat-native messages a visible row stands for: its own id, and a
// collapsed group's newest post.
export function chatSeenIds(messages: readonly ProjectMessage[], id: string, event: number): string[] {
  return messages.filter(message => message.chat_thread && !message.chat_seen && (message.id === id || message.sent_event_id === event)).map(message => message.id)
}
// The furthest chat-native message the thread's read marker says this person
// saw, on any device.
export function chatSeenMark(messages: readonly ProjectMessage[]): { event: number; id: string } | null {
  let best: ProjectMessage | undefined
  for (const message of messages) if (message.chat_thread && message.chat_seen && (!best || message.sent_event_id > best.sent_event_id)) best = message
  return best ? { event: best.sent_event_id, id: best.id } : null
}

// m:ss, tabular so the timer never changes width within a minute range.
export function elapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`
}

export const liveWords = {
  en: { working: 'Working', escStop: 'to stop', stopping: 'Stopping…', stopped: 'You stopped this turn', waiting: 'Waiting for you', gap: 'Some live output was skipped. The saved reply is complete.', truncated: 'Live output is cut here. The saved reply is complete.', liveOnly: 'Live only: this output is not saved to the transcript.', tools: (n: number) => n === 1 ? 'Used 1 tool' : `Used ${n} tools`, toolFailed: 'failed', live: 'Live reply' },
  de: { working: 'Arbeitet', escStop: 'stoppt', stopping: 'Wird gestoppt …', stopped: 'Du hast diese Runde gestoppt', waiting: 'Wartet auf dich', gap: 'Ein Teil der Live-Ausgabe wurde übersprungen. Die gespeicherte Antwort ist vollständig.', truncated: 'Die Live-Ausgabe endet hier. Die gespeicherte Antwort ist vollständig.', liveOnly: 'Nur live: diese Ausgabe wird nicht im Verlauf gespeichert.', tools: (n: number) => n === 1 ? '1 Werkzeug genutzt' : `${n} Werkzeuge genutzt`, toolFailed: 'fehlgeschlagen', live: 'Live-Antwort' },
} as const

// ---------- Stream ----------
export interface LiveSource {
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void
  close(): void
  onerror: ((event: Event) => unknown) | null
  // 2 = closed for good (an HTTP refusal); otherwise the browser reconnects
  // on its own with Last-Event-ID.
  readonly readyState: number
}
export interface LiveDeps {
  lookup: (sessionId: string) => Promise<{ status: number; thread?: string }>
  open: (url: string) => LiveSource | null
  // Injected in tests; the browser's timers otherwise.
  later?: (run: () => void, ms: number) => () => void
}
export interface LiveHandlers {
  frame: (data: string) => void
  // The replay window is gone (restart, eviction, handover). Reload final
  // history; the caller drops interim content.
  resync: () => void
  // The thread this session is bound to, once found.
  thread?: (id: string) => void
  // Whether a stream is attached right now.
  attached: (live: boolean) => void
}

// Follows one session's thread. A missing binding, a disabled chat module or a
// refusal leaves the view on its final-history path without any error.
// `retry()` asks again, for example when a new turn starts.
export function followChatLive(sessionId: string, handlers: LiveHandlers, deps: LiveDeps) {
  let stopped = false
  let source: LiveSource | null = null
  let looking = false
  let thread = ''
  let failures = 0
  let timer: (() => void) | undefined
  const later = deps.later ?? ((run: () => void, ms: number) => { const handle = setTimeout(run, ms); return () => clearTimeout(handle) })
  const detach = () => {
    source?.close(); source = null
    timer?.(); timer = undefined
  }
  function connect() {
    if (stopped || !thread) return
    detach()
    // EventSource replays from its own Last-Event-ID after a network drop;
    // a fresh connection starts at the live edge.
    const current = source = deps.open(`/api/chat-threads/${encodeURIComponent(thread)}/live`)
    if (!current) return
    const mine = (run: (event: MessageEvent<string>) => void) => (event: MessageEvent<string>) => { if (!stopped && source === current) run(event) }
    current.addEventListener('ready', mine(() => { failures = 0; handlers.attached(true) }))
    current.addEventListener('chat', mine(event => handlers.frame(event.data)))
    current.addEventListener('keepalive', mine(() => {}))
    current.addEventListener('resync', mine(() => {
      detach(); handlers.attached(false); handlers.resync()
      // The binding may have moved to a successor session.
      thread = ''
      void look()
    }))
    current.onerror = () => {
      if (stopped || source !== current) return
      handlers.attached(false)
      // A dropped connection or the server's five-minute rotation resumes by
      // itself from Last-Event-ID. A refused or expired stream (404/409/429)
      // closes for good: interim content may be missing, so drop it and ask
      // for the thread again with backoff, a few attempts per turn.
      if (current.readyState !== 2) return
      detach()
      handlers.resync()
      thread = ''
      if (++failures > 4) return
      timer = later(() => { timer = undefined; void look() }, Math.min(30_000, 1000 * 2 ** failures))
    }
  }
  async function look() {
    if (stopped || looking || thread) return
    looking = true
    try {
      const found = await deps.lookup(sessionId)
      if (stopped) return
      if (found.status === 200 && found.thread) { thread = found.thread; handlers.thread?.(thread); connect() }
    } catch { /* Final history stays authoritative. */ }
    finally { looking = false }
  }
  void look()
  return {
    retry() { if (stopped || source || timer !== undefined) return; failures = 0; if (thread) connect(); else void look() },
    stop() { stopped = true; detach(); handlers.attached(false) },
  }
}
