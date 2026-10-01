// SPDX-License-Identifier: AGPL-3.0-only
// A session or run row as the server sent it, carried with what it knows about its
// own answer (AEON-449). The type is opaque on purpose: nothing can read a field of a
// Wire row, render it or keep it in a list. The only way out is stores/agents.ts, which judges
// it against the copy it already holds, and the only way in is lib/agentRows.ts, the
// one module that talks to the session and run endpoints. So a row that never passed
// the ledger has no way to reach a component: the compiler refuses it.
//
// Two imports are fenced by eslint.config.js: wrapRow belongs to agentRows.ts and
// openRow to stores/agents.ts.

// What an answer knows about one row. The server's own revision decides first. The
// event-log position is snapshot metadata of a list read (a mutation result carries
// none: its position is a write floor, never a statement about its body). The tick the
// request started at breaks the remaining ties.
export interface RowStamp { rowVersion?: number; position?: number; start: number }

// A class with a private member is nominal, and a spread copy drops it: a value of this
// type can be made only by wrapRow, never by object literal, cast-free assignment or spread.
export declare class Wire<T> { private readonly wired: T }

interface Envelope<T> { row: T; stamp: RowStamp }

export function wrapRow<T extends { id: string; row_version?: number }>(row: T, position: number | undefined, start: number): Wire<T> {
  const version = row.row_version
  const rowVersion = typeof version === 'number' && Number.isSafeInteger(version) && version > 0 ? version : undefined
  return { row, stamp: { rowVersion, position, start } } as unknown as Wire<T>
}

export function openRow<T>(wire: Wire<T>): { row: T; stamp: RowStamp } {
  return wire as unknown as Envelope<T>
}
