// SPDX-License-Identifier: AGPL-3.0-only
// Node writes this tab made (AEON-326): the revisions its own saves, moves,
// creations and bulk changes answered with, and one unmatched claim for a
// delete (that request answers 204, with no revision). Live views skip only
// the event of that write. A later delete, restore or update of the same
// node — from this account in another tab, or from anyone else — is not
// one of those events, so it is applied. The actor alone never proves it.
import { compareRevision } from './liveUpdates.ts'

const LIMIT = 1000
const PER_NODE = 4

export interface OwnChange { id: string; change: string; revision: string | null; eventId?: number }

export class OwnWrites {
  // Node id → the revisions this tab's writes produced, the newest last.
  private revisions = new Map<string, string[]>()
  // Node id → event ids already recognised as this tab's write.
  private events = new Map<string, number[]>()
  // A delete this tab sent whose event has not arrived yet.
  private claims = new Set<string>()

  // Nodes a write of this tab answered with. A delete claim stays: the
  // undo's answer can land before the delete event, and that event is still ours.
  wrote(nodes: Iterable<{ id: string; updated_at: string }>) {
    for (const node of nodes) {
      if (!node?.id || !node.updated_at) continue
      this.rememberRevision(node.id, node.updated_at)
    }
    this.trim()
  }

  // A node this tab deleted. The next delete event for it is that write.
  deleted(id: string) {
    if (!id) return
    this.claims.delete(id)
    this.claims.add(id)
    this.trim()
  }

  // Every live change, including ones a view then ignores. Anything that is
  // not a delete ends an unmatched claim: the node was restored or updated
  // since this tab deleted it, so a later delete is someone else's.
  observe(change: { id: string; change: string }) {
    if (!change?.id || change.change === 'deleted') return
    this.claims.delete(change.id)
  }

  // The change is one exact write of this tab, already on screen.
  made(change: OwnChange): boolean {
    if (!change?.id) return false
    const eventId = change.eventId ?? 0
    const knownEvent = eventId > 0 && !!this.events.get(change.id)?.includes(eventId)
    const knownRevision = !!change.revision && !!this.revisions.get(change.id)?.some(known => compareRevision(known, change.revision) === 0)
    if (knownEvent || knownRevision) { this.remember(change); return true }
    if (change.change === 'deleted' && this.claims.has(change.id)) {
      this.claims.delete(change.id)
      this.remember(change)
      return true
    }
    return false
  }

  clear() { this.revisions.clear(); this.events.clear(); this.claims.clear() }

  private remember(change: OwnChange) {
    if (change.revision) this.rememberRevision(change.id, change.revision)
    const eventId = change.eventId ?? 0
    if (eventId > 0) this.rememberEvent(change.id, eventId)
    this.trim()
  }

  private rememberRevision(id: string, revision: string) {
    const known = this.revisions.get(id) ?? []
    if (known.some(item => compareRevision(item, revision) === 0)) return
    this.revisions.delete(id)
    this.revisions.set(id, [...known.slice(-(PER_NODE - 1)), revision])
  }

  private rememberEvent(id: string, eventId: number) {
    const known = this.events.get(id) ?? []
    if (known.includes(eventId)) return
    this.events.delete(id)
    this.events.set(id, [...known.slice(-(PER_NODE - 1)), eventId])
  }

  private trim() {
    for (const id of this.revisions.keys()) { if (this.revisions.size <= LIMIT) break; this.revisions.delete(id) }
    for (const id of this.events.keys()) { if (this.events.size <= LIMIT) break; this.events.delete(id) }
    for (const id of this.claims) { if (this.claims.size <= LIMIT) break; this.claims.delete(id) }
  }
}

// The tab's record.
export const ownWrites = new OwnWrites()
