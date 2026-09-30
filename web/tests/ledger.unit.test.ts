// SPDX-License-Identifier: AGPL-3.0-only
// AEON-449: one revision authority per entity. Every answer that carries a row
// merges through the ledger, whichever read or write brought it, and an older
// answer never replaces a newer row: by the entity's own revision first, then by
// the position the server stamped, then by the order the requests started in.
import { expect, it, vi } from 'vitest'
import { createLedger } from '../src/lib/ledger'
import { stampAt, tick } from '../src/lib/position'

interface Row { id: string; rev?: number; value: string }
// An answer for `id`, as a request that started at `start` brought it back.
const answer = (fields: Partial<Row> & { position?: number; start?: number }, id = 'a'): Row => {
  const { position, start, ...row } = fields
  return stampAt({ id, value: '', ...row }, { position, start })
}
const valueAfter = (ledger: ReturnType<typeof createLedger<Row>>, ...rows: Row[]) => ledger.merge(rows).map(row => row.value)

it('the entity revision decides first, whatever the position or start order says', () => {
  const ledger = createLedger<Row>({ revisionOf: row => row.rev })
  const askedEarly = tick()
  expect(valueAfter(ledger, answer({ rev: 2, value: 'restored', position: 11, start: tick() }))).toEqual(['restored'])
  // A lower revision with a higher position, from a request that started before the row landed.
  expect(valueAfter(ledger, answer({ rev: 1, value: 'archived', position: 99, start: askedEarly }))).toEqual(['restored'])
  expect(ledger.get('a')?.value).toBe('restored')
  // A higher revision wins even with a lower position.
  expect(valueAfter(ledger, answer({ rev: 3, value: 'newest', position: 5, start: askedEarly }))).toEqual(['newest'])
})

it('with no revision to compare, the server position decides before the start order', () => {
  const ledger = createLedger<Row>()
  const early = tick(), late = tick()
  expect(valueAfter(ledger, answer({ value: 'at-10', position: 10, start: late }))).toEqual(['at-10'])
  // Started earlier, answered higher: newer.
  expect(valueAfter(ledger, answer({ value: 'at-11', position: 11, start: early }))).toEqual(['at-11'])
  // Asked before the row landed and answering lower: older.
  expect(valueAfter(ledger, answer({ value: 'at-9', position: 9, start: early }))).toEqual(['at-11'])
})

it('equal positions, or no position on one side, go to the request that started later', () => {
  const ledger = createLedger<Row>()
  const first = tick(), second = tick(), third = tick()
  ledger.merge([answer({ value: 'second', position: 12, start: second })])
  expect(valueAfter(ledger, answer({ value: 'first', position: 12, start: first }))).toEqual(['second'])
  expect(valueAfter(ledger, answer({ value: 'third', position: 12, start: third }))).toEqual(['third'])
  // A read an event interrupted names no position: it is judged by when it started.
  const straddled = answer({ value: 'straddled', start: tick() })
  expect(valueAfter(ledger, straddled)).toEqual(['straddled'])
  expect(valueAfter(ledger, answer({ value: 'exact-but-earlier', position: 12, start: first }))).toEqual(['straddled'])
})

it('a row that knows none of it is taken, and so is one when the held row knows none', () => {
  const ledger = createLedger<Row>({ revisionOf: row => row.rev })
  expect(valueAfter(ledger, { id: 'a', value: 'bare' })).toEqual(['bare'])
  expect(valueAfter(ledger, answer({ rev: 1, value: 'stamped', position: 3, start: tick() }))).toEqual(['stamped'])
  expect(valueAfter(ledger, { id: 'a', value: 'bare-again' })).toEqual(['bare-again'])
})

it('an answer asked for after the held row landed may read lower: the log went backwards', () => {
  const ledger = createLedger<Row>({ revisionOf: row => row.rev })
  ledger.merge([answer({ rev: 40, value: 'before-restore', position: 500, start: tick() })])
  // A restored database: a read that began after the row landed answers far below it.
  expect(valueAfter(ledger, answer({ rev: 7, value: 'restored', position: 60, start: tick() }))).toEqual(['restored'])
  expect(valueAfter(ledger, answer({ rev: 8, value: 'and-moves-on', position: 61, start: tick() }))).toEqual(['and-moves-on'])
})

it('keeps the rows independent and answers in the order they came in', () => {
  const ledger = createLedger<Row>({ revisionOf: row => row.rev })
  const early = tick()
  ledger.merge([answer({ rev: 2, value: 'a2', position: 11, start: tick() }, 'a')])
  const standing = ledger.merge([
    answer({ rev: 1, value: 'a1', position: 10, start: early }, 'a'),
    answer({ rev: 1, value: 'b1', position: 10, start: early }, 'b'),
  ])
  expect(standing.map(row => row.value)).toEqual(['a2', 'b1'])
})

it('builds the row to keep from the held one and the admitted one, and tells followers which ids changed', () => {
  const combine = vi.fn((held: Row | undefined, incoming: Row): Row => ({ ...incoming, value: `${held?.value ?? ''}+${incoming.value}` }))
  const ledger = createLedger<Row>({ revisionOf: row => row.rev, combine })
  const heard: string[][] = []
  const stop = ledger.subscribe(ids => heard.push([...ids].sort()))
  const askedEarly = tick()
  ledger.merge([answer({ rev: 2, value: 'x', start: tick() }, 'a'), answer({ rev: 1, value: 'y', start: tick() }, 'b')])
  expect(heard).toEqual([['a', 'b']])
  expect(ledger.get('a')?.value).toBe('+x')
  // Refused: nothing changes, nobody is told, combine is not asked.
  combine.mockClear()
  ledger.merge([answer({ rev: 1, value: 'old', start: askedEarly }, 'a')])
  expect(combine).not.toHaveBeenCalled()
  expect(heard).toHaveLength(1)
  // Admitted: combine sees the held row first.
  ledger.merge([answer({ rev: 3, value: 'z', start: tick() }, 'a')])
  expect(combine).toHaveBeenCalledWith({ id: 'a', rev: 2, value: '+x' }, expect.objectContaining({ rev: 3, value: 'z' }))
  expect(heard).toEqual([['a', 'b'], ['a']])
  stop()
  ledger.merge([answer({ rev: 4, value: 'unheard', start: tick() }, 'a')])
  expect(heard).toHaveLength(2)
})

it('forgets every row when cleared', () => {
  const ledger = createLedger<Row>({ revisionOf: row => row.rev })
  ledger.merge([answer({ rev: 9, value: 'held', position: 90, start: tick() })])
  ledger.clear()
  expect(ledger.get('a')).toBeUndefined()
  expect(valueAfter(ledger, answer({ rev: 1, value: 'fresh', position: 1, start: tick() }))).toEqual(['fresh'])
})
