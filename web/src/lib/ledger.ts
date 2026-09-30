// SPDX-License-Identifier: AGPL-3.0-only
// One revision authority per kind of entity (AEON-449). /agents holds the same
// session or run from several reads at once: the current list, History, a ticket's
// sessions, a per-agent list, and the answers to its own writes. Each of them
// merges its rows here, keyed by the entity's id, and what the ledger holds is the
// row every view shows. A response can never replace a row with an older one:
//
//   1. the entity's own revision, when both rows carry one, decides;
//   2. else the event-log position the server stamped on the answer;
//   3. else the order the requests started in (the later one wins a tie, and a
//      row that knows none of these is taken).
//
// A row is only ever refused against a row it is older than. One whose request
// started after the held row landed cannot be older, so it is taken even when it
// reads lower: the server's log went backwards (a restored database), and
// refusing it would freeze the row.
import { stampOf, tick } from './position'

interface Entry<T> { row: T; revision?: number; position?: number; start?: number; landed: number }

export interface LedgerOptions<T> {
  // The entity's own server revision, when it has one.
  revisionOf?: (row: T) => number | undefined
  // Builds the row to keep from the one held and the one admitted, for evidence the
  // newer answer may omit. The default keeps the admitted row.
  combine?: (held: T | undefined, incoming: T) => T
}

export function createLedger<T extends { id: string }>(options: LedgerOptions<T> = {}) {
  const entries = new Map<string, Entry<T>>()
  const listeners = new Set<(ids: Set<string>) => void>()

  function admits(held: Entry<T>, incoming: Entry<T>) {
    // Asked after the held row landed: it cannot be older, so a lower answer means the log moved back.
    const backwards = incoming.start !== undefined && incoming.start > held.landed
    if (incoming.revision !== undefined && held.revision !== undefined && incoming.revision !== held.revision) {
      return incoming.revision > held.revision || backwards
    }
    if (incoming.position !== undefined && held.position !== undefined && incoming.position !== held.position) {
      return incoming.position > held.position || backwards
    }
    if (incoming.start === undefined || held.start === undefined) return true
    return incoming.start >= held.start
  }

  return {
    // Merges rows and returns, in order, the row that stands for each: the admitted
    // one, or the row held when the incoming one is older. Listeners hear which ids changed.
    merge(rows: T[]): T[] {
      const landed = tick()
      const changed = new Set<string>()
      const standing = rows.map(row => {
        const stamp = stampOf(row)
        const incoming: Entry<T> = { row, revision: options.revisionOf?.(row), position: stamp?.position, start: stamp?.start, landed }
        const held = entries.get(row.id)
        if (held && !admits(held, incoming)) return held.row
        incoming.row = options.combine ? options.combine(held?.row, row) : row
        entries.set(row.id, incoming)
        changed.add(row.id)
        return incoming.row
      })
      if (changed.size) for (const listen of listeners) listen(changed)
      return standing
    },
    get: (id: string): T | undefined => entries.get(id)?.row,
    // Called after a merge replaced rows, with their ids: a view that copied a row follows it.
    subscribe(listen: (ids: Set<string>) => void) {
      listeners.add(listen)
      return () => { listeners.delete(listen) }
    },
    clear() { entries.clear() },
  }
}
export type Ledger<T extends { id: string }> = ReturnType<typeof createLedger<T>>
