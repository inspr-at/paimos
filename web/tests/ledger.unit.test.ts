// SPDX-License-Identifier: AGPL-3.0-only
// AEON-449: one revision authority per entity. Every answer that carries a row
// merges through the ledger, whichever read or write brought it, and an older
// answer never replaces a newer row: by the row's own revision first, then by
// the position of the list snapshot it came from, then by the order the requests
// started in. A row with no revision never replaces one that has it.
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import * as ledgerExports from '../src/lib/ledger'
import * as storeExports from '../src/stores/agents'
import { useAgents } from '../src/stores/agents'
import type { AgentRunRow, HarnessSessionRow } from '../src/lib/agentRows'
import { resetPositions } from '../src/lib/position'
import { tick } from '../src/lib/position'
import { wrapRow } from '../src/lib/wire'

type Row = AgentRunRow
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))
beforeEach(() => { setActivePinia(createPinia()); resetPositions() })
// Exercise the actual private ledger through the same canonical admission API
// components use. Tests cannot create an independent production ledger.
function canonicalLedger() {
  const agents = useAgents()
  return { merge: agents.admitRuns, get: (id: string) => agents.runs[id], clear: resetPositions }
}
// An answer for `id`, as a request that started at `start` brought it back: a read
// names the position of its snapshot, a write names none.
const answer = (fields: Partial<Row> & { position?: number; start?: number }, id = 'a') => {
  const { position, start, ...row } = fields
  return wrapRow({ id, requested_model: '', work_order_id: 'order', agent_principal_id: 'agent', status: 'queued' as const, model_evidence: 'unverified' as const, input_tokens: 0, output_tokens: 0, cost_micros: 0, created_at: '', ...row }, position, start ?? tick())
}
const valueAfter = (ledger: ReturnType<typeof canonicalLedger>, ...rows: ReturnType<typeof answer>[]) => ledger.merge(rows).map(row => row.requested_model)

it('the row version decides first, whatever the position or start order says', () => {
  const ledger = canonicalLedger()
  const askedEarly = tick()
  expect(valueAfter(ledger, answer({ row_version: 2, requested_model: 'restored', position: 11 }))).toEqual(['restored'])
  // A lower version with a higher position, from a request that started before the row landed.
  expect(valueAfter(ledger, answer({ row_version: 1, requested_model: 'archived', position: 99, start: askedEarly }))).toEqual(['restored'])
  expect(ledger.get('a')?.requested_model).toBe('restored')
  // A higher version wins even with a lower position.
  expect(valueAfter(ledger, answer({ row_version: 3, requested_model: 'newest', position: 5, start: askedEarly }))).toEqual(['newest'])
})

// Review of AEON-449 round 2, P1: a mutation result carried a watermark read after its
// commit, newer than its own body. It carries no position now, and its version outranks a
// read that only shares the position.
it('a write result older than a read at the position its watermark would have named does not replace the read', () => {
  const ledger = canonicalLedger()
  // The override's request starts first; the read starts later, sees the run after a daemon claimed it, and lands first.
  const overrideStarted = tick()
  ledger.merge([answer({ row_version: 4, requested_model: 'running', position: 11 })])
  // The override's own body was made at version 3 (queued); a request-order tie-break must not save it.
  expect(valueAfter(ledger, answer({ row_version: 3, requested_model: 'queued', start: overrideStarted }))).toEqual(['running'])
})

it('with equal versions, the list snapshot decides before the start order', () => {
  const ledger = canonicalLedger()
  const early = tick(), late = tick()
  expect(valueAfter(ledger, answer({ row_version: 5, requested_model: 'at-10', position: 10, start: late }))).toEqual(['at-10'])
  // Started earlier, read later: newer derived fields.
  expect(valueAfter(ledger, answer({ row_version: 5, requested_model: 'at-11', position: 11, start: early }))).toEqual(['at-11'])
  // Asked before the row landed and read lower: older.
  expect(valueAfter(ledger, answer({ row_version: 5, requested_model: 'at-9', position: 9, start: early }))).toEqual(['at-11'])
})

it('equal versions and positions, or no position on one side, go to the request that started later', () => {
  const ledger = canonicalLedger()
  const first = tick(), second = tick(), third = tick()
  ledger.merge([answer({ row_version: 7, requested_model: 'second', position: 12, start: second })])
  expect(valueAfter(ledger, answer({ row_version: 7, requested_model: 'first', position: 12, start: first }))).toEqual(['second'])
  expect(valueAfter(ledger, answer({ row_version: 7, requested_model: 'third', position: 12, start: third }))).toEqual(['third'])
  // A write result names no position: it is judged by when its request started.
  expect(valueAfter(ledger, answer({ row_version: 7, requested_model: 'write-earlier', start: first }))).toEqual(['third'])
  expect(valueAfter(ledger, answer({ row_version: 7, requested_model: 'write-later', start: tick() }))).toEqual(['write-later'])
})

it('a row with no version never replaces one that has it; the other way round it does', () => {
  const ledger = canonicalLedger()
  expect(valueAfter(ledger, answer({ requested_model: 'bare' }))).toEqual(['bare'])
  expect(valueAfter(ledger, answer({ row_version: 1, requested_model: 'versioned', position: 3 }))).toEqual(['versioned'])
  // Even one that started later and names a higher position: it cannot be judged, so it is refused.
  expect(valueAfter(ledger, answer({ requested_model: 'bare-again', position: 50, start: tick() }))).toEqual(['versioned'])
  expect(ledger.get('a')?.requested_model).toBe('versioned')
})

it('rows with no version are still ordered by position, then by start, among themselves', () => {
  const ledger = canonicalLedger()
  const early = tick(), late = tick()
  ledger.merge([answer({ requested_model: 'at-10', position: 10, start: late })])
  expect(valueAfter(ledger, answer({ requested_model: 'at-11', position: 11, start: early }))).toEqual(['at-11'])
  expect(valueAfter(ledger, answer({ requested_model: 'at-9', position: 9, start: early }))).toEqual(['at-11'])
})

it('an answer asked for after the held row landed may read lower: the log went backwards', () => {
  const ledger = canonicalLedger()
  ledger.merge([answer({ row_version: 40, requested_model: 'before-restore', position: 500 })])
  // A restored database: a read that began after the row landed answers far below it.
  expect(valueAfter(ledger, answer({ row_version: 7, requested_model: 'restored', position: 60 }))).toEqual(['restored'])
  expect(valueAfter(ledger, answer({ row_version: 8, requested_model: 'and-moves-on', position: 61 }))).toEqual(['and-moves-on'])
})

it('keeps the rows independent and answers in the order they came in', () => {
  const ledger = canonicalLedger()
  const early = tick()
  ledger.merge([answer({ row_version: 2, requested_model: 'a2', position: 11 }, 'a')])
  const standing = ledger.merge([
    answer({ row_version: 1, requested_model: 'a1', position: 10, start: early }, 'a'),
    answer({ row_version: 1, requested_model: 'b1', position: 10, start: early }, 'b'),
  ])
  expect(standing.map(row => row.requested_model)).toEqual(['a2', 'b1'])
})

it('exports no factory or ledger and shares admission across every store caller', () => {
  expect(Object.keys(ledgerExports)).toEqual([])
  expect(Object.keys(storeExports)).toEqual(['useAgents'])
  const first = useAgents(), second = useAgents()
  expect(second).toBe(first)
  const early = tick()
  const [newest] = first.admitRuns([answer({ row_version: 2, requested_model: 'newest' })])
  expect(second.admitRun(answer({ row_version: 1, requested_model: 'stale', start: early }))).toBe(newest)
  expect(first.runs.a).toBe(newest)
})

it('freezes admitted rows recursively, including carried evidence and Vue-held rows', () => {
  const agents = useAgents()
  const raw = { id: 'session', row_version: 1, phase: 'working', project: { id: 'p', key: 'P', title: 'Original' }, advertised_capabilities: ['watch'], metadata_history: [{ field: 'model', value: 'initial', previous_value: null, at: '' }] } as HarnessSessionRow
  const [row] = agents.admitSessions([wrapRow(raw, 1, tick())])
  expect(Object.isFrozen(row)).toBe(true)
  expect(Object.isFrozen(row.project)).toBe(true)
  expect(Object.isFrozen(row.advertised_capabilities)).toBe(true)
  expect(Object.isFrozen(row.metadata_history![0])).toBe(true)
  expect(() => { row.phase = 'stopped' }).toThrow(TypeError)
  expect(() => { row.row_version = 999 }).toThrow(TypeError)
  expect(() => { row.project!.title = 'Changed' }).toThrow(TypeError)
  expect(() => { row.advertised_capabilities.push('other') }).toThrow(TypeError)
  expect(() => { row.metadata_history![0]!.value = 'changed' }).toThrow(TypeError)
  expect(() => { raw.project!.title = 'Changed via wire alias' }).toThrow(TypeError)
  const [newer] = agents.admitSessions([wrapRow({ id: 'session', row_version: 2, phase: 'stopped' } as HarnessSessionRow, 2, tick())])
  expect(newer.project?.title).toBe('Original')
  expect(Object.isFrozen(newer.project)).toBe(true)
  expect(agents.sessionById('session')).toBe(newer)
  const run = agents.admitRun(answer({ row_version: 1, requested_model: 'frozen' }))
  expect(() => { agents.runs.a!.status = 'running' }).toThrow(TypeError)
  expect(agents.runs.a).toBe(run)
})

it('forgets every row when cleared', () => {
  const ledger = canonicalLedger()
  ledger.merge([answer({ row_version: 9, requested_model: 'held', position: 90 })])
  ledger.clear()
  expect(ledger.get('a')).toBeUndefined()
  expect(valueAfter(ledger, answer({ row_version: 1, requested_model: 'fresh', position: 1 }))).toEqual(['fresh'])
})
