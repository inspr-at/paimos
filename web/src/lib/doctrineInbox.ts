// SPDX-License-Identifier: AGPL-3.0-only
// The doctrine inbox signal (AEON-444): how many agent proposals wait for a
// person, for the dot on Settings and on Agent rules, and one toast per new
// proposal. Doctrine events reach only their actor on the event stream, so the
// header polls the small summary instead. A toast is shown once per proposal
// and principal, across tabs and reloads, and never again after it was seen:
// tabs claim new proposals under one Web Lock, and share what they claimed on
// a BroadcastChannel and in memory when localStorage is unavailable. A person's
// action and a session change invalidate summary reads still in flight, so a
// late answer never restores a dot, a toast or the previous principal.
import { reactive } from 'vue'
import { getDoctrineInboxSummary, inboxLabel, type DoctrineInboxHeadline } from './doctrine.ts'

export const doctrineInbox = reactive({ pending: 0, principal: '' })

const SEEN_LIMIT = 200
const seenKey = (principal: string) => `aeon.doctrine-inbox.seen.${principal}`
// Seen proposals this tab knows of: its own claims and those other tabs
// broadcast. It keeps toasts once-only when localStorage fails.
const remembered = new Map<string, string[]>()
let channel: BroadcastChannel | null | undefined
// Bumped by every action and session change; a read from an older turn is dropped.
let generation = 0

function remember(principal: string, ids: readonly string[]) {
  const known = remembered.get(principal) ?? []
  remembered.set(principal, [...known.filter(id => !ids.includes(id)), ...ids].slice(-SEEN_LIMIT))
}

function broadcast(): BroadcastChannel | null {
  if (channel !== undefined) return channel
  channel = null
  if (typeof BroadcastChannel !== 'function') return channel
  try {
    const next = new BroadcastChannel('aeon.doctrine-inbox')
    next.onmessage = (event: MessageEvent<{ principal?: unknown; ids?: unknown }>) => {
      const { principal, ids } = event.data ?? {}
      if (typeof principal === 'string' && Array.isArray(ids)) remember(principal, ids.filter((id): id is string => typeof id === 'string'))
    }
    // Never hold a process open (the unit tests run in Node).
    ;(next as { unref?: () => void }).unref?.()
    channel = next
  } catch { /* no channel: storage and memory still apply */ }
  return channel
}

export function readSeen(principal: string): string[] {
  let stored: string[] = []
  try {
    const raw = JSON.parse(localStorage.getItem(seenKey(principal)) ?? '[]') as unknown
    if (Array.isArray(raw)) stored = raw.filter((id): id is string => typeof id === 'string')
  } catch { /* private window: memory only */ }
  const memory = remembered.get(principal) ?? []
  return [...stored, ...memory.filter(id => !stored.includes(id))]
}
function writeSeen(principal: string, ids: string[]) {
  try { localStorage.setItem(seenKey(principal), JSON.stringify(ids.slice(-SEEN_LIMIT))) } catch { /* private window */ }
}

/** The headlines not seen yet, oldest first. */
export function unseen(items: DoctrineInboxHeadline[], seen: readonly string[]): DoctrineInboxHeadline[] {
  const known = new Set(seen)
  return items.filter(item => !known.has(item.id)).sort((a, b) => a.created_at.localeCompare(b.created_at))
}

/** The toast's words for the new proposals, or null when there are none. */
export function toastText(fresh: DoctrineInboxHeadline[]): string | null {
  if (!fresh.length) return null
  if (fresh.length === 1) return `Doctrine change proposed: ${inboxLabel(fresh[0]!.label)}`
  return `${fresh.length} doctrine changes proposed`
}

// Runs fn while holding the principal's cross-tab lock, where Web Locks exist.
async function exclusive<T>(principal: string, fn: () => T): Promise<T> {
  const locks = typeof navigator === 'undefined' ? undefined : navigator.locks
  if (!locks?.request) return fn()
  return locks.request(`aeon.doctrine-inbox.${principal}`, () => fn())
}

/**
 * Marks the headlines not seen yet as seen and returns them. Tabs take turns,
 * so two tabs polling at once never both claim a proposal.
 */
export function claimUnseen(principal: string, items: DoctrineInboxHeadline[]): Promise<DoctrineInboxHeadline[]> {
  return exclusive(principal, () => {
    const seen = readSeen(principal)
    const fresh = unseen(items, seen)
    if (!fresh.length) return fresh
    const ids = fresh.map(item => item.id)
    remember(principal, ids)
    writeSeen(principal, [...seen, ...ids])
    try { broadcast()?.postMessage({ principal, ids }) } catch { /* closed channel */ }
    return fresh
  })
}

/**
 * Reads the summary, updates the count and returns the text of a toast to
 * show, marking those proposals seen before anything is shown. A read that an
 * action or a session change overtook changes nothing.
 */
export async function pollDoctrineInbox(principal: string, read = getDoctrineInboxSummary): Promise<string | null> {
  const turn = generation
  const summary = await read()
  if (turn !== generation) return null
  if (doctrineInbox.principal && doctrineInbox.principal !== principal) return null
  doctrineInbox.principal = principal
  doctrineInbox.pending = summary.pending
  const fresh = await claimUnseen(principal, summary.items)
  return turn === generation ? toastText(fresh) : null
}

/** Drops every summary read still in flight. */
export function invalidateDoctrineInbox() { generation++ }

/** A person acted on a proposal here: the count follows at once. */
export function inboxChanged(pending: number) { invalidateDoctrineInbox(); doctrineInbox.pending = Math.max(0, pending) }

export function resetDoctrineInbox() { invalidateDoctrineInbox(); doctrineInbox.pending = 0; doctrineInbox.principal = '' }
