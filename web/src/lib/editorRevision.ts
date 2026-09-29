// SPDX-License-Identifier: AGPL-3.0-only
// The revision an open editor saves against (AEON-326). The editor holds it
// when editing starts and is the only thing that moves it (its own save, or
// a conflict that adopted the newer copy). Every list and panel refresh
// calls keep, so a reload, Show, resync or gap read cannot point the next
// save at someone else's revision.
import { compareRevision } from './liveUpdates.ts'
import { ownWrites } from './ownWrites.ts'

interface Held { base: string; withheld: string | null }

export class EditorRevision {
  private held = new Map<string, Held>()

  // The revision the editor started from. A second hold (saving while the
  // editor is already open) does not move it.
  hold(id: string, revision: string) {
    if (!id || !revision || this.held.has(id)) return
    this.held.set(id, { base: revision, withheld: null })
  }

  // The revision a save must send, when this editor holds one.
  revision(id: string): string | undefined { return this.held.get(id)?.base }

  // This editor's own save, or the newer copy a conflict showed it: the
  // base moves, and a withheld server revision is no longer waiting.
  adopt(id: string, revision: string) {
    const current = this.held.get(id)
    if (!current || !revision) return
    current.base = revision
    current.withheld = null
  }

  // The revision a refresh may store on the row. A newer server copy is
  // remembered and given back by release, once the editor closes.
  keep(id: string, incoming: string | null | undefined): string | undefined {
    if (!incoming) return incoming ?? undefined
    const current = this.held.get(id)
    if (!current) return incoming
    if (ownWrites.made({ id, change: 'updated', revision: incoming })) {
      current.base = incoming
      current.withheld = null
      return incoming
    }
    if (compareRevision(incoming, current.base) > 0 && (!current.withheld || compareRevision(incoming, current.withheld) > 0)) current.withheld = incoming
    return current.base
  }

  // Editing ended. A server revision a refresh withheld is handed to write,
  // so the row catches up and the next open starts from it.
  release(id: string, write?: (revision: string) => void) {
    const current = this.held.get(id)
    if (!current) return
    this.held.delete(id)
    if (current.withheld) write?.(current.withheld)
  }

  clear() { this.held.clear() }
}

export const editorRevision = new EditorRevision()

// A row a load just read. Its revision stays the editor's while one is open.
export function acceptLoadedRow<T extends { id: string; updated_at: string }>(row: T): T {
  const kept = editorRevision.keep(row.id, row.updated_at)
  if (kept !== undefined) row.updated_at = kept
  return row
}

// Patch a row in place from a server copy. Same rule for its revision.
export function applyServerRow<T extends { id: string; updated_at: string }>(target: T, source: T): T {
  const kept = editorRevision.keep(target.id, source.updated_at)
  Object.assign(target, source)
  if (kept !== undefined) target.updated_at = kept
  return target
}
