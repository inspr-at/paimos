// SPDX-License-Identifier: AGPL-3.0-only
// Rows as lib/agentRows.ts hands them out (AEON-449): wire rows, stamped with their own
// row_version and the tick their request started at, unreadable until the ledger has
// admitted them. Specs that stand in for the fetch module return these.
import { tick } from '../src/lib/position'
import { wrapRow, type Wire } from '../src/lib/wire'

type Row = { id: string; row_version?: number }
// One row as an answer that started now brought it; a read names the position of its snapshot.
export const wired = <T extends Row>(row: T, position?: number): Wire<T> => wrapRow(row, position, tick())
// A page of wire rows, the way listAllSessions and listRuns answer.
export const wiredPage = <T extends Row>(items: T[], next_cursor: string | null = null): { items: Wire<T>[]; next_cursor: string | null } => ({ items: items.map(item => wired(item)), next_cursor })
