// SPDX-License-Identifier: AGPL-3.0-only
// The doctrine inbox signal (AEON-444): how many agent proposals wait for a
// person, for the dot on Settings and on Agent rules, and one toast per new
// proposal. Doctrine events reach only their actor on the event stream, so the
// header polls the small summary instead. The server decides what is new: a
// proposal toasts only in the poll whose claim the server grants, once per
// person and proposal, in any tab or device, with or without storage. A
// person's action and a session change invalidate summary reads still in
// flight, so a late answer never restores a dot, claims a toast or brings back
// the previous principal. A proposal a person settled here never toasts, even
// when its claim was granted before the dismissal and answered after it.
import { reactive } from 'vue'
import { claimDoctrineInboxNotice, getDoctrineInboxSummary, inboxLabel, type DoctrineInboxHeadline } from './doctrine.ts'

export const doctrineInbox = reactive({ pending: 0, principal: '' })

// Bumped by every action and session change; a read from an older turn is dropped.
let generation = 0
// Bumped by a session change only.
let session = 0
// Proposals a person sent, edited or dismissed here in this session.
const settled = new Set<string>()

/** The headlines this person was not notified of yet, oldest first. */
export function unclaimed(items: DoctrineInboxHeadline[]): DoctrineInboxHeadline[] {
  return items.filter(item => !item.notified).sort((a, b) => a.created_at.localeCompare(b.created_at))
}

/** The toast's words for the new proposals, or null when there are none. */
export function toastText(fresh: DoctrineInboxHeadline[]): string | null {
  if (!fresh.length) return null
  if (fresh.length === 1) return `Doctrine change proposed: ${inboxLabel(fresh[0]!.label)}`
  return `${fresh.length} doctrine changes proposed`
}

/**
 * Reads the summary, updates the count and returns the text of a toast to
 * show for the proposals this poll claimed. A read that an action or a session
 * change overtook claims nothing; its proposals toast on the next poll.
 */
export async function pollDoctrineInbox(principal: string, read = getDoctrineInboxSummary, claim = claimDoctrineInboxNotice): Promise<string | null> {
  const turn = generation
  const since = session
  const summary = await read()
  if (turn !== generation) return null
  if (doctrineInbox.principal && doctrineInbox.principal !== principal) return null
  doctrineInbox.principal = principal
  doctrineInbox.pending = summary.pending
  const fresh: DoctrineInboxHeadline[] = []
  for (const item of unclaimed(summary.items)) {
    if (turn !== generation) break
    try {
      if (await claim(item.id)) fresh.push(item)
    } catch {
      break // the next poll claims the rest
    }
  }
  // A granted claim is this person's only toast for it; a session change
  // drops it, and so does settling the proposal while the claim was answered.
  if (since !== session) return null
  const waiting = turn === generation ? fresh : fresh.filter(item => !settled.has(item.id) && doctrineInbox.pending > 0)
  return toastText(waiting)
}

/** Drops every summary read still in flight. */
export function invalidateDoctrineInbox() { generation++ }

/** A person acted on a proposal here: the count follows at once. */
export function inboxChanged(pending: number, ...acted: string[]) {
  invalidateDoctrineInbox()
  for (const id of acted) settled.add(id)
  doctrineInbox.pending = Math.max(0, pending)
}

export function resetDoctrineInbox() { session++; invalidateDoctrineInbox(); settled.clear(); doctrineInbox.pending = 0; doctrineInbox.principal = '' }
