// SPDX-License-Identifier: AGPL-3.0-only
// Merge by server revision (AEON-449). The server names the tenant event-log
// position every read and every accepted write stands for (Aeon-Event-Position).
// A client that holds several reads of one collection, or a read and a write of its
// own, keeps the newest answer per entity: an older one never overwrites a newer
// one, whatever order the answers arrive in.
//
//   - A read's position names the exact snapshot it returns: no event committed
//     while it ran. A read an event interrupted carries no position.
//   - A write's position is at or above the write's own event. A read below the
//     newest write this tab made may predate it, and is asked again.
//
// Rows are judged by ledger.ts, one authority per entity. This file holds what
// it and the collection readers share: the clock, the stamps, the write floor and
// the order of the reads of one collection. An answer without a position (an older
// server, an interrupted read, a test double) is merged by the order it started in.

export const POSITION_HEADER = 'aeon-event-position'

export function parsePosition(response: Response): number | undefined {
  const raw = response.headers.get(POSITION_HEADER)
  return raw !== null && /^\d{1,15}$/.test(raw) ? Number(raw) : undefined
}

// One clock for the page. A request takes a tick when it starts, an answer takes
// one when it is merged: a read that started after an answer landed cannot be
// older than it, unless the server's log went backwards.
let clock = 0
export const tick = () => ++clock

// What an answer knows about itself: the position the server named (none when it
// named none) and the tick its request started at.
export interface Stamp { position?: number; start?: number }

// A stamp lives beside the data, never in it, so spreading or serialising the data
// is unchanged. Every object of a response body keeps the stamp of the response it
// came from, so a row knows its own answer wherever it is merged.
const stamps = new WeakMap<object, Stamp>()
const STAMP_DEPTH = 3
export function stampAt<T>(body: T, stamp: Stamp | undefined, depth = 0): T {
  if (!stamp || typeof body !== 'object' || body === null) return body
  stamps.set(body, stamp)
  if (depth > 0) for (const value of Array.isArray(body) ? body : Object.values(body)) stampAt(value, stamp, depth - 1)
  return body
}
export const stamp = <T>(body: T, response: Response, start?: number): T => stampAt(body, { position: parsePosition(response), start }, STAMP_DEPTH)
export const stampOf = (body: unknown): Stamp | undefined => typeof body === 'object' && body !== null ? stamps.get(body) : undefined
export const positionOf = (body: unknown): number | undefined => stampOf(body)?.position
// A copy made by spreading is a new object: it keeps the stamp of the one it copies.
export function carry<T>(copy: T, from: unknown): T {
  return stampAt(copy, stampOf(from))
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
const resets = new Set<() => void>()
export function onReset(reset: () => void) {
  resets.add(reset)
  return () => { resets.delete(reset) }
}
export function resetPositions() {
  floor = 0
  for (const reset of resets) reset()
}

export type ReadVerdict = 'apply' | 'stale' | 'older'
export interface ReadTicket { turn: number; bar: number }

// The order of the reads of one collection. begin() when a read starts, land()
// when its answer arrives:
//   apply  the answer is the newest this collection has seen;
//   stale  it predates a write of this tab, so ask again (it cannot predate that write);
//   older  a newer answer has landed, drop it.
// The server's position decides first; the order the reads started in only breaks
// a tie and judges an answer with no position. A read that started after a write,
// or after a newer answer landed, cannot answer below it. If one does, the
// server's log went backwards (a restored database): its answer is applied and the
// order follows it, so nothing freezes.
export function createReadOrder() {
  let applied: number | undefined
  let appliedTurn = 0
  let superseded = 0
  return {
    begin: (): ReadTicket => ({ turn: tick(), bar: Math.max(floor, applied ?? 0) }),
    land(ticket: ReadTicket, position?: number): ReadVerdict {
      if (position === undefined) {
        if (ticket.turn < appliedTurn || ticket.turn <= superseded) return 'older'
      } else if (position < ticket.bar) { applied = position; floor = Math.min(floor, position) }
      else if (applied !== undefined && position < applied) return 'older'
      else if (position < floor) return 'stale'
      else if (position === applied && ticket.turn < appliedTurn) return 'older'
      else applied = position
      appliedTurn = ticket.turn
      return 'apply'
    },
    // An answer without a position cannot be judged by its data. Drops the ones
    // from reads begun so far; a positioned answer never needs the call.
    supersede() { superseded = tick() },
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
