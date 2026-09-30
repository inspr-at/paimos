// SPDX-License-Identifier: AGPL-3.0-only
// AEON-449: one revision authority per entity. Every answer that carries a row
// merges through the ledger, whichever read or write brought it, and an older
// answer never replaces a newer row: by the row's own revision first, then by
// the position of the list snapshot it came from, then by the order the requests
// started in. A row with no revision never replaces one that has it.
import { expect, it, vi } from 'vitest'
import { createLedger } from '../src/lib/ledger'
import { tick } from '../src/lib/position'
import { wrapRow } from '../src/lib/wire'

interface Row { id: string; row_version?: number; value: string }
// An answer for `id`, as a request that started at `start` brought it back: a read
// names the position of its snapshot, a write names none.
const answer = (fields: Partial<Row> & { position?: number; start?: number }, id = 'a') => {
  const { position, start, ...row } = fields
  return wrapRow({ id, value: '', ...row }, position, start ?? tick())
}
const valueAfter = (ledger: ReturnType<typeof createLedger<Row>>, ...rows: ReturnType<typeof answer>[]) => ledger.merge(rows).map(row => row.value)

it('the row version decides first, whatever the position or start order says', () => {
  const ledger = createLedger<Row>()
  const askedEarly = tick()
  expect(valueAfter(ledger, answer({ row_version: 2, value: 'restored', position: 11 }))).toEqual(['restored'])
  // A lower version with a higher position, from a request that started before the row landed.
  expect(valueAfter(ledger, answer({ row_version: 1, value: 'archived', position: 99, start: askedEarly }))).toEqual(['restored'])
  expect(ledger.get('a')?.value).toBe('restored')
  // A higher version wins even with a lower position.
  expect(valueAfter(ledger, answer({ row_version: 3, value: 'newest', position: 5, start: askedEarly }))).toEqual(['newest'])
})

// Review of AEON-449 round 2, P1: a mutation result carried a watermark read after its
// commit, newer than its own body. It carries no position now, and its version outranks a
// read that only shares the position.
it('a write result older than a read at the position its watermark would have named does not replace the read', () => {
  const ledger = createLedger<Row>()
  // The override's request starts first; the read starts later, sees the run after a daemon claimed it, and lands first.
  const overrideStarted = tick()
  ledger.merge([answer({ row_version: 4, value: 'running', position: 11 })])
  // The override's own body was made at version 3 (queued); a request-order tie-break must not save it.
  expect(valueAfter(ledger, answer({ row_version: 3, value: 'queued', start: overrideStarted }))).toEqual(['running'])
})

it('with equal versions, the list snapshot decides before the start order', () => {
  const ledger = createLedger<Row>()
  const early = tick(), late = tick()
  expect(valueAfter(ledger, answer({ row_version: 5, value: 'at-10', position: 10, start: late }))).toEqual(['at-10'])
  // Started earlier, read later: newer derived fields.
  expect(valueAfter(ledger, answer({ row_version: 5, value: 'at-11', position: 11, start: early }))).toEqual(['at-11'])
  // Asked before the row landed and read lower: older.
  expect(valueAfter(ledger, answer({ row_version: 5, value: 'at-9', position: 9, start: early }))).toEqual(['at-11'])
})

it('equal versions and positions, or no position on one side, go to the request that started later', () => {
  const ledger = createLedger<Row>()
  const first = tick(), second = tick(), third = tick()
  ledger.merge([answer({ row_version: 7, value: 'second', position: 12, start: second })])
  expect(valueAfter(ledger, answer({ row_version: 7, value: 'first', position: 12, start: first }))).toEqual(['second'])
  expect(valueAfter(ledger, answer({ row_version: 7, value: 'third', position: 12, start: third }))).toEqual(['third'])
  // A write result names no position: it is judged by when its request started.
  expect(valueAfter(ledger, answer({ row_version: 7, value: 'write-earlier', start: first }))).toEqual(['third'])
  expect(valueAfter(ledger, answer({ row_version: 7, value: 'write-later', start: tick() }))).toEqual(['write-later'])
})

it('a row with no version never replaces one that has it; the other way round it does', () => {
  const ledger = createLedger<Row>()
  expect(valueAfter(ledger, answer({ value: 'bare' }))).toEqual(['bare'])
  expect(valueAfter(ledger, answer({ row_version: 1, value: 'versioned', position: 3 }))).toEqual(['versioned'])
  // Even one that started later and names a higher position: it cannot be judged, so it is refused.
  expect(valueAfter(ledger, answer({ value: 'bare-again', position: 50, start: tick() }))).toEqual(['versioned'])
  expect(ledger.get('a')?.value).toBe('versioned')
})

it('rows with no version are still ordered by position, then by start, among themselves', () => {
  const ledger = createLedger<Row>()
  const early = tick(), late = tick()
  ledger.merge([answer({ value: 'at-10', position: 10, start: late })])
  expect(valueAfter(ledger, answer({ value: 'at-11', position: 11, start: early }))).toEqual(['at-11'])
  expect(valueAfter(ledger, answer({ value: 'at-9', position: 9, start: early }))).toEqual(['at-11'])
})

it('an answer asked for after the held row landed may read lower: the log went backwards', () => {
  const ledger = createLedger<Row>()
  ledger.merge([answer({ row_version: 40, value: 'before-restore', position: 500 })])
  // A restored database: a read that began after the row landed answers far below it.
  expect(valueAfter(ledger, answer({ row_version: 7, value: 'restored', position: 60 }))).toEqual(['restored'])
  expect(valueAfter(ledger, answer({ row_version: 8, value: 'and-moves-on', position: 61 }))).toEqual(['and-moves-on'])
})

it('keeps the rows independent and answers in the order they came in', () => {
  const ledger = createLedger<Row>()
  const early = tick()
  ledger.merge([answer({ row_version: 2, value: 'a2', position: 11 }, 'a')])
  const standing = ledger.merge([
    answer({ row_version: 1, value: 'a1', position: 10, start: early }, 'a'),
    answer({ row_version: 1, value: 'b1', position: 10, start: early }, 'b'),
  ])
  expect(standing.map(row => row.value)).toEqual(['a2', 'b1'])
})

it('builds the row to keep from the held one and the admitted one, and tells followers which ids changed', () => {
  const combine = vi.fn((held: Row | undefined, incoming: Row): Row => ({ ...incoming, value: `${held?.value ?? ''}+${incoming.value}` }))
  const ledger = createLedger<Row>({ combine })
  const heard: string[][] = []
  const stop = ledger.subscribe(ids => heard.push([...ids].sort()))
  const askedEarly = tick()
  ledger.merge([answer({ row_version: 2, value: 'x' }, 'a'), answer({ row_version: 1, value: 'y' }, 'b')])
  expect(heard).toEqual([['a', 'b']])
  expect(ledger.get('a')?.value).toBe('+x')
  // Refused: nothing changes, nobody is told, combine is not asked.
  combine.mockClear()
  ledger.merge([answer({ row_version: 1, value: 'old', start: askedEarly }, 'a')])
  expect(combine).not.toHaveBeenCalled()
  expect(heard).toHaveLength(1)
  // Admitted: combine sees the held row first.
  ledger.merge([answer({ row_version: 3, value: 'z' }, 'a')])
  expect(combine).toHaveBeenCalledWith({ id: 'a', row_version: 2, value: '+x' }, expect.objectContaining({ row_version: 3, value: 'z' }))
  expect(heard).toEqual([['a', 'b'], ['a']])
  stop()
  ledger.merge([answer({ row_version: 4, value: 'unheard' }, 'a')])
  expect(heard).toHaveLength(2)
})

it('forgets every row when cleared', () => {
  const ledger = createLedger<Row>()
  ledger.merge([answer({ row_version: 9, value: 'held', position: 90 })])
  ledger.clear()
  expect(ledger.get('a')).toBeUndefined()
  expect(valueAfter(ledger, answer({ row_version: 1, value: 'fresh', position: 1 }))).toEqual(['fresh'])
})
