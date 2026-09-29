// SPDX-License-Identifier: AGPL-3.0-only
// Per-viewer chat state for the session panel (AEON-273, AEON-276): the last tab,
// a read watermark per session, and delivery receipt wording. The watermark is the
// highest sent_event_id this person has had in view. The server marker follows the
// person across devices; this browser's copy is the offline fallback.
import type { MessageGroup } from './sessionMessages.ts'

export type SessionTab = 'overview' | 'messages'
export interface ReadMark { event: number; id: string; at: number }
export interface InboxReceipt { message_id: string; state: 'queued' | 'handed_off' | 'failed'; handed_off_at: string | null; failure_reason: string }

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

export function receiptTip(receipt: InboxReceipt, format: (iso: string) => string): string {
  if (receipt.state === 'handed_off') return receipt.handed_off_at ? `Picked up by the session · ${format(receipt.handed_off_at)}` : 'Picked up by the session'
  if (receipt.state === 'failed') return `Not delivered${receipt.failure_reason ? ` · ${receipt.failure_reason.replaceAll('_', ' ')}` : ''}`
  return 'Sent · waiting for the session to pick it up'
}

// Within this distance of the end the thread counts as read to the bottom.
export const nearBottom = (el: { scrollHeight: number; scrollTop: number; clientHeight: number }, slack = 32) =>
  el.scrollHeight - el.scrollTop - el.clientHeight <= slack
