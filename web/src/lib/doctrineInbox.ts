// SPDX-License-Identifier: AGPL-3.0-only
// The doctrine inbox signal (AEON-444): how many agent proposals wait for a
// person, for the dot on Settings and on Agent rules, and one toast per new
// proposal. Doctrine events reach only their actor on the event stream, so the
// header polls the small summary instead. A toast is shown once per proposal
// and principal, across tabs and reloads, and never again after it was seen.
import { reactive } from 'vue'
import { getDoctrineInboxSummary, inboxLabel, type DoctrineInboxHeadline } from './doctrine.ts'

export const doctrineInbox = reactive({ pending: 0, principal: '' })

const SEEN_LIMIT = 200
const seenKey = (principal: string) => `aeon.doctrine-inbox.seen.${principal}`

export function readSeen(principal: string): string[] {
  try {
    const raw = JSON.parse(localStorage.getItem(seenKey(principal)) ?? '[]') as unknown
    return Array.isArray(raw) ? raw.filter((id): id is string => typeof id === 'string') : []
  } catch { return [] }
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

/**
 * Reads the summary, updates the count and returns the text of a toast to
 * show, marking those proposals seen before anything is shown.
 */
export async function pollDoctrineInbox(principal: string): Promise<string | null> {
  const summary = await getDoctrineInboxSummary()
  if (doctrineInbox.principal && doctrineInbox.principal !== principal) return null
  doctrineInbox.principal = principal
  doctrineInbox.pending = summary.pending
  // Re-read just before writing: another tab may have shown them already.
  const seen = readSeen(principal)
  const fresh = unseen(summary.items, seen)
  if (!fresh.length) return null
  writeSeen(principal, [...seen, ...fresh.map(item => item.id)])
  return toastText(fresh)
}

/** A person acted on a proposal here: the count follows at once. */
export function inboxChanged(pending: number) { doctrineInbox.pending = Math.max(0, pending) }

export function resetDoctrineInbox() { doctrineInbox.pending = 0; doctrineInbox.principal = '' }
