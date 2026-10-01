// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { compareModelSort, formatDollars, formatTokenCount, listCostCell, modelCell, planningPresent, planningSortValue, tokensCell, type PlanningRow, type TicketPlanning } from '../src/lib/planning.ts'
import { compareRows } from '../src/lib/ticketList.ts'
import type { ListItem } from '../src/lib/api.ts'

const route = { label: 'Codex astra · xhigh', profile: 'codex-astra-xhigh', harness: 'codex', model: 'gpt-6-astra', effort: 'xhigh', revision: '3f9a1c2b' }
function row(planning: Partial<TicketPlanning> | undefined, fields: Record<string, unknown> = { route_role: 'build-hard', area: 'backend' }, kind = 'ticket'): PlanningRow {
  return {
    kind_slug: kind, fields,
    planning: planning && { route: null, tokens: { spent: null, input: 0, output: 0, cached: 0, sessions: 0, unreported: 0, estimated: null }, ...planning },
  }
}
const tokens = (spent: number | null, estimated: number | null, extra: object = {}) => ({ spent, input: spent ?? 0, output: 0, cached: 0, sessions: spent === null ? 0 : 2, unreported: 0, estimated, ...extra })
const cost = (extra: object) => ({ list_spent: null, list_estimated: null, list_unpriced: false, paid_spent: null, paid_estimated: null, paid_unknown: false, plans: [], ...extra })

test('dense figures', () => {
  assert.equal(formatTokenCount(940), '940')
  assert.equal(formatTokenCount(9_120), '9.12k')
  assert.equal(formatTokenCount(1_540_000), '1.54M')
  assert.equal(formatTokenCount(10_000_000), '10M')
  assert.equal(formatDollars('0.000000'), '$0')
  assert.equal(formatDollars('0.004'), '<$0.01')
  assert.equal(formatDollars('4.480000'), '$4.48')
  assert.equal(formatDollars('27.000000'), '$27')
  assert.equal(formatDollars('1234.5'), '$1.23k')
  assert.equal(formatDollars(null), '')
})

test('planning cells distinguish a planned model and measured figures', () => {
  assert.equal(modelCell(row({ route })).text, 'astra')
  assert.equal(modelCell(row({ route })).state, 'planned')
  assert.match(modelCell(row({ route })).tip, /Model registry, revision 3f9a1c2b/)
  assert.match(modelCell(row(undefined, {})).tip, /Set a role and area/)
  assert.match(modelCell(row({ route_gap: 'area' }, { route_role: 'build' })).tip, /Set an area/)
  assert.match(modelCell(row({ route_gap: 'review_gate' }, { route_role: 'review-gate', area: 'backend' })).tip, /family other than the author's/)
  assert.match(modelCell(row({ route_gap: 'registry' }, { route_role: 'mechanical', area: 'docs' })).tip, /no available route/)
  assert.match(modelCell(row(undefined, {}, 'epic')).tip, /Epics take no model/)
  const measured = tokensCell(row({ tokens: tokens(12_000_000, 10_000_000) }))
  assert.equal(measured.state, 'measured')
  assert.equal(measured.over, false)
  assert.match(measured.tip, /\(\+20%\)/)
  assert.equal(tokensCell(row({ tokens: tokens(null, null) })).label, '')
  assert.equal(listCostCell(row({ tokens: tokens(1, 1) })).label, '')
})

test('presence and sort values follow the list API', () => {
  const rows = [row({ route, tokens: tokens(5, null), cost: cost({ list_spent: '1' }) }), row(undefined, {})]
  assert.deepEqual(planningPresent(rows), { model: true, tokens: true, list_cost: true, paid: false })
  assert.deepEqual(planningPresent([row(undefined, {})]), { model: false, tokens: false, list_cost: false, paid: false })
  assert.equal(planningSortValue(rows[0]!, 'model'), 'codex astra')
  assert.equal(planningSortValue(rows[1]!, 'model'), null)
  assert.equal(compareModelSort(rows[0]!, rows[1]!, false), -1)
  assert.equal(planningSortValue(rows[0]!, 'tokens'), 5)
  assert.equal(planningSortValue(rows[0]!, 'list_cost'), null)
  assert.equal(planningSortValue({ ...rows[0]!, planning: { ...rows[0]!.planning!, cost: cost({ list_spent: '1', list_cost_micros: '1000000' }) } }, 'list_cost'), 1000000n)
  assert.equal(planningSortValue(rows[0]!, 'paid'), null)
})

test('numeric planning sorts use spent else estimated, null last and ID ties', () => {
  const high = { ...row({ tokens: tokens(null, 100_000_000), cost: cost({ list_estimated: '100', paid_estimated: '100', list_cost_micros: '100000000', paid_micros: '100000000' }) }), id: 'a' } as ListItem
  const low = { ...row({ tokens: tokens(null, 10_000_000), cost: cost({ list_estimated: '10', paid_estimated: '10', list_cost_micros: '10000000', paid_micros: '10000000' }) }), id: 'c' } as ListItem
  const tie = { ...low, id: 'b' }
  const zero = { ...row({ tokens: tokens(0, 200_000_000), cost: cost({ list_spent: '0', list_estimated: '200', paid_spent: '0', paid_estimated: '200', list_cost_micros: '0', paid_micros: '0' }) }), id: 'd' } as ListItem
  const empty = { ...row(undefined), id: 'e' } as ListItem
  for (const field of ['tokens', 'list_cost', 'paid'] as const) {
    const rows = [high, low, empty, zero, tie]
    const asc = compareRows([{ field, desc: false }])
    const desc = compareRows([{ field, desc: true }])
    assert.deepEqual([...rows].sort(asc).map(r => r.id), ['d', 'b', 'c', 'a', 'e'])
    assert.deepEqual([...rows].sort(desc).map(r => r.id), ['a', 'b', 'c', 'd', 'e'])
    assert.equal(asc(low, low), 0)
  }
})

test('cost sort compares micro-dollar integers past the float mantissa', () => {
  const item = (id: string, usd: string, micros: string) => ({
    ...row({ cost: cost({ list_spent: usd, paid_spent: usd, list_cost_micros: micros, paid_micros: micros }) }),
    id,
  }) as ListItem
  // These two USD strings collapse to one JS number; the lower id holds the larger amount.
  const low = item('b', '10000000000.000001', '10000000000000001')
  const high = item('a', '10000000000.000002', '10000000000000002')
  const tie = item('c', '10000000000.000001', '10000000000000001')
  // A shorter integer must still sort before a longer one.
  const narrow = item('n', '0.000999', '999')
  const wide = item('w', '0.001000', '1000')
  for (const field of ['list_cost', 'paid'] as const) {
    assert.equal(planningSortValue(low, field), 10000000000000001n)
    assert.equal(planningSortValue(high, field), 10000000000000002n)
    const asc = compareRows([{ field, desc: false }])
    const desc = compareRows([{ field, desc: true }])
    assert.deepEqual([high, low].sort(asc).map(r => r.id), ['b', 'a'])
    assert.deepEqual([low, high].sort(desc).map(r => r.id), ['a', 'b'])
    assert.deepEqual([tie, low].sort(asc).map(r => r.id), ['b', 'c'])
    assert.deepEqual([tie, low].sort(desc).map(r => r.id), ['b', 'c'])
    assert.deepEqual([wide, narrow].sort(asc).map(r => r.id), ['n', 'w'])
    assert.deepEqual([narrow, wide].sort(desc).map(r => r.id), ['w', 'n'])
  }
})

test('model sort uses full identity and version, with absent models last', () => {
  const item = (id: string, name?: string, version = '') => ({ ...row(name ? { route: { ...route, display_name: name, short_name: name.split(' ').pop(), model_version: version } } : undefined, {}), id }) as ListItem
  const rows = [item('d'), item('c', 'Codex Sol', '6.1'), item('b', 'Claude Opus', '5.5'), item('a', 'Claude Opus', '5')]
  assert.deepEqual([...rows].sort(compareRows([{ field: 'model', desc: false }])).map(item => item.id), ['a', 'b', 'c', 'd'])
  assert.deepEqual([...rows].sort(compareRows([{ field: 'model', desc: true }])).map(item => item.id), ['c', 'b', 'a', 'd'])
})
