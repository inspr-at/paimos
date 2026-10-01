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
import type { Wire } from './wire'

// Types only: the factory and the ledgers live privately in stores/agents.ts.
// The brand survives Vue unwrapping; every nested field and array is readonly.
export type DeepReadonly<T> = T extends object ? { readonly [K in keyof T]: DeepReadonly<T[K]> } : T
declare class Admission { private readonly admitted: true }
export type Admitted<T> = Raw<DeepReadonly<T>> & Admission
export interface Ledger<T extends { id: string }> {
  merge(rows: Wire<T>[]): Admitted<T>[]
  get(id: string): Admitted<T> | undefined
  subscribe(listen: (ids: Set<string>) => void): () => void
  clear(): void
}
