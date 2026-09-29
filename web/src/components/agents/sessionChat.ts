// SPDX-License-Identifier: AGPL-3.0-only
// Per-viewer chat state for the session panel (AEON-273): the last tab, a read
// watermark per session, and delivery receipt wording. The watermark is the highest
// sent_event_id this person has had in view; it lives in this browser only until
// the server offers a per-person read marker.
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
