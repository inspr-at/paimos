// SPDX-License-Identifier: AGPL-3.0-only
// Node writes this tab made (AEON-326): the revisions its own saves, moves,
// creations and bulk changes answered with, and the nodes it deleted. Live
// views skip the events of those writes, because the code that made them
// already shows the result. The same person's writes in another tab are not
// here: the actor alone never proves that a change is on screen.
import { compareRevision } from './liveUpdates.ts'

const LIMIT = 1000

export class OwnWrites {
  // Node id → the revisions this tab's writes produced, the newest last.
  private revisions = new Map<string, string[]>()
  private deletions = new Set<string>()

  // Nodes a write of this tab answered with.
  wrote(nodes: Iterable<{ id: string; updated_at: string }>) {
    for (const node of nodes) {
      if (!node?.id || !node.updated_at) continue
      const known = this.revisions.get(node.id) ?? []
      this.revisions.delete(node.id)
      this.revisions.set(node.id, [...known.slice(-3), node.updated_at])
      this.deletions.delete(node.id)
    }
    this.trim()
  }
  // A node this tab deleted.
  deleted(id: string) {
    this.deletions.delete(id)
    this.deletions.add(id)
    this.trim()
  }
  // The change is one of this tab's own writes, already on screen.
  made(change: { id: string; change: string; revision: string | null }): boolean {
    if (change.change === 'deleted') return this.deletions.has(change.id)
    const revision = change.revision
    return !!revision && !!this.revisions.get(change.id)?.some(known => compareRevision(known, revision) === 0)
  }
  clear() { this.revisions.clear(); this.deletions.clear() }
  private trim() {
    for (const id of this.revisions.keys()) { if (this.revisions.size <= LIMIT) break; this.revisions.delete(id) }
    for (const id of this.deletions) { if (this.deletions.size <= LIMIT) break; this.deletions.delete(id) }
  }
}

// The tab's record.
export const ownWrites = new OwnWrites()
