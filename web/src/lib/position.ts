// SPDX-License-Identifier: AGPL-3.0-only
// Merge by server revision (AEON-449). The server names the tenant event-log
// position every read and every accepted write stands for (Aeon-Event-Position).
// A client that holds several reads of one collection, or a read and a write of
// its own, keeps the newest position per entity: an older answer never
// overwrites a newer one, whatever order the answers arrive in.
//
//   - A read's position is a floor: it includes every event up to it.
//   - A write's position is at or above the write's own event. A read below the
//     newest write this tab made may predate it, and is asked again.
//
// An answer without a position (an older server, a test double) is merged the
// way it was before: by the order its read started in.

export const POSITION_HEADER = 'aeon-event-position'

export function parsePosition(response: Response): number | undefined {
  const raw = response.headers.get(POSITION_HEADER)
  return raw !== null && /^\d{1,15}$/.test(raw) ? Number(raw) : undefined
}

// A body is stamped with the position of the read that answered it. The stamp
// lives beside the body, never in it, so spreading or serialising it is unchanged.
const stamps = new WeakMap<object, number>()
export function stampAt<T>(body: T, position: number | undefined): T {
  if (position !== undefined && typeof body === 'object' && body !== null) stamps.set(body, position)
  return body
}
export const stamp = <T>(body: T, response: Response): T => stampAt(body, parsePosition(response))
export function positionOf(body: unknown): number | undefined {
  return typeof body === 'object' && body !== null ? stamps.get(body) : undefined
}
// The lowest position of several answers: what a read assembled from all of them includes.
export function lowestPosition(positions: (number | undefined)[]): number | undefined {
  if (!positions.length || positions.some(position => position === undefined)) return undefined
  return Math.min(...positions as number[])
}

// What this tab's own accepted writes have proven. One ledger for the page: every
// write goes through api(), so no path can write without raising the floor.
let floor = 0
let accepted = 0
export function noteWrite(response: Response) {
  accepted++
  const position = parsePosition(response)
  if (position !== undefined && position > floor) floor = position
}
export const writeFloor = () => floor
// Accepted writes so far; a caller compares two marks to learn whether one landed between.
export const writeMark = () => accepted
export const wroteSince = (mark: number) => accepted !== mark
// Positions belong to one workspace: a different person or workspace starts at zero.
export function resetPositions() { floor = 0 }

export type ReadVerdict = 'apply' | 'stale' | 'older'
export interface ReadTicket { turn: number; bar: number }

// The order of the reads of one collection. begin() when a read starts, land()
// when its answer arrives:
//   apply  the answer is the newest this collection has seen;
//   stale  it predates a write of this tab, so ask again (it cannot predate that write);
//   older  a newer answer has landed, drop it.
// A read that started after a write, or after a newer answer landed, cannot
// answer below it. If one does, the server's log went backwards (a restored
// database): its answer is applied and the ledger follows it, so nothing freezes.
export function createReadOrder() {
  let started = 0
  let landed = 0
  let applied = 0
  let superseded = 0
  return {
    begin: (): ReadTicket => ({ turn: ++started, bar: Math.max(floor, applied) }),
    land(ticket: ReadTicket, position?: number): ReadVerdict {
      if (ticket.turn < landed) return 'older'
      if (position === undefined) {
        if (ticket.turn <= superseded) return 'older'
      } else if (position < ticket.bar) { applied = position; floor = Math.min(floor, position) }
      else if (position < applied) return 'older'
      else if (position < floor) return 'stale'
      else applied = position
      landed = ticket.turn
      return 'apply'
    },
    // An answer without a position cannot be judged by its data. Drops the ones
    // from reads begun so far; a positioned answer never needs the call.
    supersede() { superseded = started },
  }
}
export type ReadOrder = ReturnType<typeof createReadOrder>

// Run one read of an ordered collection: ask again once when the answer predates
// a write of this tab (the second read starts after the write, so it cannot), and
// hand the answer to apply only when it is the newest. Returns whether it applied.
export async function readOrdered<T>(order: ReadOrder, read: () => Promise<T>, apply: (value: T) => void, position: (value: T) => number | undefined = positionOf): Promise<boolean> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const ticket = order.begin()
    const value = await read()
    const verdict = order.land(ticket, position(value))
    if (verdict === 'apply') { apply(value); return true }
    if (verdict === 'older') return false
  }
  return false
}
