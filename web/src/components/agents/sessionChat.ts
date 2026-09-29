// SPDX-License-Identifier: AGPL-3.0-only
// Per-viewer chat state for the session panel (AEON-273, AEON-276, AEON-280): the
// last tab, a read watermark per session, and delivery receipt wording. The
// watermark is the highest sent_event_id this person has had in view. The server
// marker follows the person across devices; this browser's copy is the offline
// fallback.
import type { MessageStatus } from '../../lib/agents.ts'
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
    const parsed = JSON.parse(store?.getItem(READ_KEY) ?? '{}')
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
}
export const reasonText = (reason?: string) => reason ? REASON[reason] ?? reason.replaceAll('_', ' ') : ''
export function statusLabel(status: MessageStatus): string {
  switch (status.status) {
    case 'read': return 'Read'
    case 'delivered': return 'Delivered'
    case 'not_delivered': return status.reason ? `Not delivered · ${reasonText(status.reason)}` : 'Not delivered'
    default: return 'Sent'
  }
}
export function statusTip(status: MessageStatus, format: (iso: string) => string): string {
  switch (status.status) {
    case 'read': return status.read_at ? `Read by the session · ${format(status.read_at)}` : 'Read by the session'
    case 'delivered': return status.delivered_at ? `Delivered to the session · ${format(status.delivered_at)}` : 'Delivered to the session'
    case 'not_delivered': return 'It will not arrive later. Send it again once the session is listening.'
    default: return 'Sent · waiting for the session to pick it up'
  }
}
export const statusDone = (status?: MessageStatus) => status?.status === 'read' || status?.status === 'not_delivered'

// Within this distance of the end the thread counts as read to the bottom.
export const nearBottom = (el: { scrollHeight: number; scrollTop: number; clientHeight: number }, slack = 32) =>
  el.scrollHeight - el.scrollTop - el.clientHeight <= slack
