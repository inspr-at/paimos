// SPDX-License-Identifier: AGPL-3.0-only
// AEON-449: reads and writes are ordered by the server's event-log position, not by
// the order their answers arrive in.
import { beforeEach, expect, it } from 'vitest'
import { createReadOrder, lowestPosition, noteWrite, parsePosition, positionOf, POSITION_HEADER, readOrdered, resetPositions, stamp, stampAt, writeFloor, writeMark, wroteSince } from '../src/lib/position'
import { usePolledData } from '../src/lib/usePolledData'

const answer = (position?: number, status = 200) => new Response('{}', { status, headers: position === undefined ? {} : { [POSITION_HEADER]: String(position) } })
beforeEach(() => resetPositions())

it('reads a position only from a plain non-negative integer', () => {
  expect(parsePosition(answer(0))).toBe(0)
  expect(parsePosition(answer(41))).toBe(41)
  expect(parsePosition(answer())).toBeUndefined()
  for (const raw of ['-1', '1.5', 'abc', '', '1e3', '0x10', '9'.repeat(16)]) {
    expect(parsePosition(new Response('{}', { headers: { [POSITION_HEADER]: raw } }))).toBeUndefined()
  }
})

it('stamps a body beside it: the position never enters the data', () => {
  const body = stamp({ items: [1] }, answer(7))
  expect(positionOf(body)).toBe(7)
  expect(JSON.stringify(body)).toBe('{"items":[1]}')
  expect(positionOf({ ...body })).toBeUndefined()
  expect(positionOf(stamp([1, 2], answer(3)))).toBe(3)
  expect(positionOf(stamp({ items: [] }, answer()))).toBeUndefined()
  expect(positionOf(stamp(null, answer(3)))).toBeUndefined()
  expect(positionOf(stampAt('text', 9))).toBeUndefined()
})

it('takes the lowest position of several answers, and none when one has none', () => {
  expect(lowestPosition([12, 10, 11])).toBe(10)
  expect(lowestPosition([12, undefined])).toBeUndefined()
  expect(lowestPosition([])).toBeUndefined()
})

it('raises the write floor only from accepted writes, never lowers it, and counts writes without a position', () => {
  const mark = writeMark()
  expect(wroteSince(mark)).toBe(false)
  noteWrite(answer(10))
  noteWrite(answer(8))
  expect(writeFloor()).toBe(10)
  noteWrite(answer())
  expect(writeFloor()).toBe(10)
  expect(wroteSince(mark)).toBe(true)
  resetPositions()
  expect(writeFloor()).toBe(0)
})

it('applies the newest answer and drops an older one that arrives after it, whatever the start order', () => {
  const order = createReadOrder()
  const slow = order.begin(), fast = order.begin()
  expect(order.land(fast, 12)).toBe('apply')
  expect(order.land(slow, 10)).toBe('older')
  const later = order.begin()
  expect(order.land(later, 12)).toBe('apply')
})

it('an answer below a write of this tab is stale, at or above it is current', () => {
  const order = createReadOrder()
  const held = order.begin()
  noteWrite(answer(20))
  expect(order.land(held, 19)).toBe('stale')
  expect(order.land(held, 20)).toBe('apply')
  const after = order.begin()
  expect(order.land(after, 25)).toBe('apply')
})

it('an answer without a position is ordered by its start, and supersede drops the reads begun before it', () => {
  const order = createReadOrder()
  const first = order.begin(), second = order.begin()
  expect(order.land(second)).toBe('apply')
  expect(order.land(first)).toBe('older')
  const held = order.begin()
  order.supersede()
  expect(order.land(held)).toBe('older')
  // A positioned answer is judged by its data: supersede does not drop it.
  const positioned = order.begin()
  order.supersede()
  expect(order.land(positioned, 5)).toBe('apply')
  expect(order.land(order.begin())).toBe('apply')
})

it('follows a log that went backwards instead of freezing on the old floor', () => {
  const order = createReadOrder()
  noteWrite(answer(500))
  expect(order.land(order.begin(), 500)).toBe('apply')
  // A restored database: a read that began after the write answers far below it.
  expect(order.land(order.begin(), 40)).toBe('apply')
  expect(writeFloor()).toBe(40)
  expect(order.land(order.begin(), 41)).toBe('apply')
  // A read that began before the write and answers below it is still stale.
  const held = order.begin()
  noteWrite(answer(60))
  expect(order.land(held, 45)).toBe('stale')
})

it('readOrdered asks again once when the answer predates a write, and gives up after the second', async () => {
  const order = createReadOrder()
  const answers: number[] = [10, 25]
  const seen: number[] = []
  let reads = 0
  const read = async () => { reads++; if (reads === 1) noteWrite(answer(20)); return stampAt({ at: answers[reads - 1] }, answers[reads - 1]) }
  expect(await readOrdered(order, read, value => seen.push(value.at))).toBe(true)
  expect(seen).toEqual([25])
  expect(reads).toBe(2)

  // Writes keep landing while it reads: the answer is below the floor again, and it gives up until the next poll.
  const busy = createReadOrder()
  const floors = [101, 150], positions = [50, 120]
  let attempts = 0
  const churn = async () => { const n = attempts++; noteWrite(answer(floors[n])); return stampAt({}, positions[n]) }
  expect(await readOrdered(busy, churn, () => { throw new Error('applied') })).toBe(false)
  expect(attempts).toBe(2)
})

it('a polled collection drops an answer below a newer one and re-reads one that predates a write', async () => {
  const answers: { value: string; position: number }[] = []
  const gates: (() => void)[] = []
  const read = () => new Promise<string[]>(resolve => { const next = answers.shift()!; gates.push(() => resolve(stampAt([next.value], next.position))) })
  const list = usePolledData(read, [] as string[], undefined, { order: createReadOrder() })

  // Two reads in flight across an invalidation: the newer lands first, the older must not rewind it.
  answers.push({ value: 'old', position: 10 }, { value: 'new', position: 12 })
  const first = list.refresh()
  list.invalidate()
  const second = list.refresh()
  gates[1]!(); await second
  gates[0]!(); await first
  expect(list.data.value).toEqual(['new'])

  // A write of this tab between the start and the answer: the answer is asked for again.
  gates.length = 0
  answers.push({ value: 'before-write', position: 12 }, { value: 'after-write', position: 31 })
  const held = list.refresh()
  noteWrite(answer(30))
  gates[0]!()
  await vi_tick()
  gates[1]!()
  await held
  expect(list.data.value).toEqual(['after-write'])
})

const vi_tick = () => new Promise(resolve => setTimeout(resolve))
