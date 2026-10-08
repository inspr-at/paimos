// SPDX-License-Identifier: AGPL-3.0-only
// Per-viewer chat state for the session panel (AEON-273, AEON-276, AEON-280): the
// last tab, a read watermark per session, and delivery receipt wording. The
// watermark is the highest sent_event_id this person has had in view. The server
// marker follows the person across devices; this browser's copy is the offline
// fallback.
import { parseJson } from '../../lib/json.ts'
import type { HarnessSession, MessageStatus, ProjectMessage } from '../../lib/agents.ts'
import type { MessageGroup } from './sessionMessages.ts'

export type SessionTab = 'overview' | 'messages'
export interface ReadMark { event: number; id: string; at: number }

const TAB_KEY = 'aeon.session-tab'
const READ_KEY = 'aeon.session-read.v1'
const MAX_MARKS = 200

type Store = Pick<Storage, 'getItem' | 'setItem'>
const storage = (): Store | null => { try { return globalThis.localStorage ?? null } catch { return null } }

export function loadTab(store: Store | null = storage()): SessionTab {
  try { return store?.getItem(TAB_KEY) === 'messages' ? 'messages' : 'overview' } catch { return 'overview' }
}
export function saveTab(tab: SessionTab, store: Store | null = storage()) {
  try { store?.setItem(TAB_KEY, tab) } catch { /* The choice holds for this visit. */ }
}
// A deep link wins over the remembered tab.
export function initialTab(query: unknown, store: Store | null = storage()): SessionTab {
  const value = Array.isArray(query) ? query[0] : query
  return value === 'messages' || value === 'overview' ? value : loadTab(store)
}

function readAll(store: Store | null): Record<string, ReadMark> {
  try {
    const parsed = parseJson(store?.getItem(READ_KEY) ?? '{}')
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, ReadMark> : {}
  } catch { return {} }
}
const markKey = (viewer: string, sessionId: string) => `${viewer}:${sessionId}`
export interface ServerReadMarker {
  session_id?: string
  last_read_message_id: string | null
  last_read_event_id: number | null
  read_at: string | null
}

export function markerFromServer(body: unknown): ReadMark | null {
  if (!body || typeof body !== 'object') return null
  const marker = body as Partial<ServerReadMarker>
  if (typeof marker.last_read_event_id !== 'number' || !Number.isFinite(marker.last_read_event_id)) return null
  if (typeof marker.last_read_message_id !== 'string' || !marker.last_read_message_id) return null
  const at = Date.parse(marker.read_at ?? '')
  return { event: marker.last_read_event_id, id: marker.last_read_message_id, at: Number.isFinite(at) ? at : 0 }
}

// The further watermark wins. A missing server marker leaves the local one.
export function preferReadMark(local: ReadMark | null, server: ReadMark | null): ReadMark | null {
  if (!server) return local
  if (!local || server.event > local.event) return server
  return local
}

// One trailing flush sends only the furthest mark. A failed send stays queued
// until the next flush, unless a later mark has already replaced it.
export const readMarkFlushDelay = 1500
export function queueReadMark(pending: ReadMark | null, next: ReadMark): ReadMark {
  return pending && pending.event >= next.event ? pending : next
}
export function keepFailedReadMark(pending: ReadMark | null, failed: ReadMark): ReadMark {
  return pending && pending.event >= failed.event ? pending : failed
}

export function loadReadMark(viewer: string, sessionId: string, store: Store | null = storage()): ReadMark | null {
  const mark = readAll(store)[markKey(viewer, sessionId)]
  return mark && Number.isFinite(mark.event) ? mark : null
}
// Only ever moves forward; the oldest sessions fall out beyond MAX_MARKS.
export function saveReadMark(viewer: string, sessionId: string, event: number, id: string, store: Store | null = storage(), now = Date.now()): ReadMark {
  const all = readAll(store)
  const key = markKey(viewer, sessionId)
  const current = all[key]
  if (current && current.event >= event) return current
  all[key] = { event, id, at: now }
  const keys = Object.keys(all)
  if (keys.length > MAX_MARKS) keys.sort((a, b) => (all[a]!.at ?? 0) - (all[b]!.at ?? 0)).slice(0, keys.length - MAX_MARKS).forEach(k => delete all[k])
  try { store?.setItem(READ_KEY, JSON.stringify(all)) } catch { /* Read state holds for this visit. */ }
  return all[key]!
}

// Unread: someone else's post above the watermark. Without a watermark an ended
// session starts read, so a new browser does not flag old history.
export function unreadGroups(items: MessageGroup[], me: string, mark: ReadMark | null, ended: boolean): MessageGroup[] {
  if (!mark && ended) return []
  const floor = mark?.event ?? -1
  return items.filter(m => m.sender_principal_id !== me && m.last_event > floor)
}

// Delivery progress of the viewer's own posts (AEON-280): Sent, Delivered (the
// session pulled it), Read (the session confirmed it) or Not delivered, loudly.
const REASON: Record<string, string> = {
  deadline: 'not confirmed in time', attempts: 'every attempt failed', session_ended: 'the session ended', no_listener: 'the session was not listening',
  unavailable: 'the session could not take it', transport_error: 'the hand-off was not confirmed',
}
export const reasonText = (reason?: string) => reason ? REASON[reason] ?? reason.replaceAll('_', ' ') : ''
export function statusLabel(status: MessageStatus): string {
  switch (status.status) {
    case 'read': return 'Read'
    case 'delivered': return 'Delivered'
    case 'not_delivered': return status.reason ? `Not delivered · ${reasonText(status.reason)}` : 'Not delivered'
    default: return 'Sending'
  }
}
export function statusTip(status: MessageStatus, format: (iso: string) => string): string {
  switch (status.status) {
    case 'read': return status.read_at ? `Read by the session · ${format(status.read_at)}` : 'Read by the session'
    case 'delivered': return status.delivered_at ? `Delivered to the session · ${format(status.delivered_at)}` : 'Delivered to the session'
    case 'not_delivered': return 'It will not arrive later. Send it again once the session is listening.'
    default: return 'On its way to the agent.'
  }
}
export const statusDone = (status?: MessageStatus) => status?.status === 'read' || status?.status === 'not_delivered'

// The status route accepts at most this many ids. Delivered, read and
// not_delivered are terminal for the outstanding queue. A missing receipt, or
// one that is still sent, is outstanding. The first look, when nothing is
// known, asks for the newest batch so the thread can paint those receipts.
// The next look asks for every id still outstanding, in batches of this size,
// so 250 sends are covered within two refreshes and a delivered receipt cannot
// keep an older send out. Messages on screen that are already delivered are
// asked after the outstanding ids, so they can still move to read.
export const receiptBatchLimit = 100
const terminalReceipt = (status?: MessageStatus) =>
  status?.status === 'delivered' || status?.status === 'read' || status?.status === 'not_delivered'

export function receiptQueryBatches(
  boundIds: readonly string[],
  visibleIds: readonly string[],
  statuses: Readonly<Record<string, MessageStatus | undefined>>,
  limit = receiptBatchLimit,
): string[][] {
  const unique: string[] = []
  const seen = new Set<string>()
  for (const id of [...boundIds, ...visibleIds]) {
    if (!id || seen.has(id)) continue
    seen.add(id)
    unique.push(id)
  }
  const outstanding = unique.filter(id => !terminalReceipt(statuses[id]))
  const known = unique.some(id => statuses[id])
  const queued = !known && outstanding.length > limit ? outstanding.slice(-limit) : outstanding
  const batches: string[][] = []
  for (let i = 0; i < queued.length; i += limit) batches.push(queued.slice(i, i + limit))
  const taken = new Set(queued)
  const readWatch: string[] = []
  const watched = new Set<string>()
  for (const id of visibleIds) {
    if (!id || watched.has(id) || taken.has(id) || statuses[id]?.status !== 'delivered') continue
    watched.add(id)
    readWatch.push(id)
  }
  const follow = readWatch.slice(-limit)
  if (follow.length) {
    const last = batches.at(-1)
    if (last && last.length + follow.length <= limit) last.push(...follow)
    else batches.push(follow)
  }
  return batches
}

// A live session with neither a stored vendor reference nor a hook read so far
// cannot take the message until its inbox hook runs (AEON-369).
export const hookDeliveryNotice = "Delivered when the session's inbox hook runs."
export function awaitsInboxHook(session: Pick<HarnessSession, 'phase' | 'stopped_at' | 'archived_at' | 'has_vendor_session_ref' | 'inbox_seen_via'>): boolean {
  if (session.phase === 'stopped' || session.stopped_at || session.archived_at) return false
  if (session.has_vendor_session_ref || session.inbox_seen_via === 'hook') return false
  return true
}

// This viewer's sends to the session, taken from the loaded thread. Receipts
// are sender-only, so the outstanding set can be rebuilt after a reload.
export function sessionBoundSends(
  messages: readonly Pick<ProjectMessage, 'id' | 'sender_principal_id' | 'recipient_session_id'>[],
  sessionId: string,
  viewerId: string,
): string[] {
  if (!sessionId || !viewerId) return []
  const ids: string[] = []
  const seen = new Set<string>()
  for (const message of messages) {
    if (message.recipient_session_id !== sessionId || message.sender_principal_id !== viewerId || seen.has(message.id)) continue
    seen.add(message.id)
    ids.push(message.id)
  }
  return ids
}

// Receipts for sends this view is waiting on. Delivered and read are finished.
// not_delivered is finished too: that message shows its own failure.
// A missing receipt is still waiting.
export interface HookReceipts { waiting: string[]; failed: MessageStatus[] }
export function hookReceipts(pendingIds: readonly string[], statuses: Readonly<Record<string, MessageStatus | undefined>>): HookReceipts {
  const waiting: string[] = []
  const failed: MessageStatus[] = []
  for (const id of pendingIds) {
    const status = statuses[id]
    if (status?.status === 'not_delivered') failed.push(status)
    else if (status?.status === 'delivered' || status?.status === 'read') continue
    else waiting.push(id)
  }
  return { waiting, failed }
}
export const hookNoticeVisible = (awaits: boolean, receipts: HookReceipts) => awaits && receipts.waiting.length > 0

// Within this distance of the end the thread counts as read to the bottom.
export const nearBottom = (el: { scrollHeight: number; scrollTop: number; clientHeight: number }, slack = 32) =>
  el.scrollHeight - el.scrollTop - el.clientHeight <= slack

// Receipt reads may complete out of order. Evidence never moves backwards.
export function advanceReceipt(held: MessageStatus | undefined, next: MessageStatus): MessageStatus {
  if (!held) return next
  const rank = { sent: 0, delivered: 1, read: 2, not_delivered: 2 }
  if (rank[next.status] < rank[held.status] || held.status === 'read' || held.status === 'not_delivered') return held
  return next
}
// Chat's approved exception to the general field-submit convention. IME and
// native browser shortcuts remain untouched.
export function chatSendLevel(event: Pick<KeyboardEvent, 'key' | 'shiftKey' | 'altKey' | 'metaKey' | 'ctrlKey' | 'isComposing'>, canSteer: boolean): 'simple' | 'steer' | null {
  if (event.key !== 'Enter' || event.isComposing || event.shiftKey || event.altKey) return null
  return canSteer && (event.metaKey || event.ctrlKey) ? 'steer' : 'simple'
}

export const chatWords = {
  en: { older: 'Earlier messages', start: 'Start of the conversation', historyError: 'Earlier messages could not be loaded. Try again.', stop: 'Stop', stopTip: 'Stop the turn', latest: 'Go to the latest message', newCount: (n: number) => `${n} new`, pendingLimit: 'Too many pending messages. Retry an existing message or wait for it to appear in the thread.', send: 'Send', now: 'Send now', after: 'After this turn', newline: 'new line', sending: 'Sending', delivered: 'Delivered', read: 'Read', failed: 'Not delivered', retry: 'Retry', message: 'Message to', placeholder: 'Message', reply: 'Reply' },
  de: { older: 'Frühere Nachrichten', start: 'Anfang des Gesprächs', historyError: 'Frühere Nachrichten konnten nicht geladen werden. Erneut versuchen.', stop: 'Stoppen', stopTip: 'Runde stoppen', latest: 'Zur neuesten Nachricht', newCount: (n: number) => `${n} neu`, pendingLimit: 'Zu viele ausstehende Nachrichten. Eine vorhandene Nachricht erneut senden oder warten, bis sie im Gespräch erscheint.', send: 'Senden', now: 'Jetzt senden', after: 'Nach dieser Runde', newline: 'neue Zeile', sending: 'Wird gesendet', delivered: 'Zugestellt', read: 'Gelesen', failed: 'Nicht zugestellt', retry: 'Erneut senden', message: 'Nachricht an', placeholder: 'Nachricht an', reply: 'Antworten' },
} as const
