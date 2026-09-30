// SPDX-License-Identifier: AGPL-3.0-only
// One revision authority per kind of entity (AEON-449). /agents holds the same
// session or run from several reads at once: the current list, History, a ticket's
// sessions, a per-agent list, a detail, a launch result and the answers to its own
// writes. Every one of them reaches the page through lib/agentRows.ts as a Wire row,
// which nothing can read, and the ledger is the only way to turn it into something
// that can be shown (Admitted). What the ledger holds is the row every view shows.
// An answer can never replace a row with an older one:
//
//   1. the row's own revision (row_version, bumped by the server inside the
//      statement that changes the row) decides when both rows carry one;
//   2. else, when both equal, the event-log position of the list snapshot each came
//      from (a mutation result has none: its position is only a write floor);
//   3. else the order the requests started in (the later one wins a tie).
//
// A row with no revision (a test double, an older server) can never replace one that
// has a revision; the server always sends it, so in production it cannot happen.
//
// A row is only ever refused against a row it is older than. One whose request
// started after the held row landed cannot be older, so it is taken even when it
// reads lower: the server's log went backwards (a restored database), and refusing
// it would freeze the row. The exception is a row without a revision against one
// with: that one is never taken.
import type { Raw } from 'vue'
import { tick } from './position'
import { openRow, type Wire } from './wire'

// A row the ledger has admitted. The brand is a private member of a class, so a value
// of this type cannot be made by an object literal, a spread copy or an assignment from
// the plain row type: only merge() below produces one, and everything that shows or
// keeps a session or run is typed to require it. Raw keeps the brand through Vue's ref
// and store unwrapping, which would otherwise drop a private member from the type.
declare class Admission { private readonly admitted: true }
export type Admitted<T> = Raw<T> & Admission

interface Entry<T> { row: Admitted<T>; revision?: number; position?: number; start: number; landed: number }

export interface LedgerOptions<T> {
  // Builds the row to keep from the one held and the one admitted, for evidence the
  // newer answer may omit (a bare mutation result has no list summaries). The default
  // keeps the admitted row as it is.
  combine?: (held: T | undefined, incoming: T) => T
}

export function createLedger<T extends { id: string }>(options: LedgerOptions<T> = {}) {
  const entries = new Map<string, Entry<T>>()
  const listeners = new Set<(ids: Set<string>) => void>()

  function admits(held: Entry<T>, incoming: Entry<T>) {
    if (held.revision !== undefined && incoming.revision === undefined) return false
    // Asked after the held row landed: it cannot be older, so a lower answer means the log moved back.
    const backwards = incoming.start > held.landed
    if (incoming.revision !== undefined && held.revision !== undefined && incoming.revision !== held.revision) {
      return incoming.revision > held.revision || backwards
    }
    if (incoming.position !== undefined && held.position !== undefined && incoming.position !== held.position) {
      return incoming.position > held.position || backwards
    }
    return incoming.start >= held.start
  }

  return {
    // Judges each wire row against the copy held and returns, in order, the row that
    // stands for it: the admitted one, or the row held when the incoming one is older.
    // Listeners hear which ids changed.
    merge(wired: Wire<T>[]): Admitted<T>[] {
      const landed = tick()
      const changed = new Set<string>()
      const standing = wired.map(wire => {
        const { row, stamp } = openRow(wire)
        const held = entries.get(row.id)
        const incoming: Entry<T> = { row: row as Admitted<T>, revision: stamp.rowVersion, position: stamp.position, start: stamp.start, landed }
        if (held && !admits(held, incoming)) return held.row
        incoming.row = (options.combine ? options.combine(held?.row, row) : row) as Admitted<T>
        entries.set(row.id, incoming)
        changed.add(row.id)
        return incoming.row
      })
      if (changed.size) for (const listen of listeners) listen(changed)
      return standing
    },
    get: (id: string): Admitted<T> | undefined => entries.get(id)?.row,
    // Called after a merge replaced rows, with their ids: a view that copied a row follows it.
    subscribe(listen: (ids: Set<string>) => void) {
      listeners.add(listen)
      return () => { listeners.delete(listen) }
    },
    clear() { entries.clear() },
  }
}
export type Ledger<T extends { id: string }> = ReturnType<typeof createLedger<T>>
