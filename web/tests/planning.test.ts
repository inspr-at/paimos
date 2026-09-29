// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { compareModelSort, formatDollars, formatTokenCount, listCostCell, modelCell, paidCell, planningPresent, planningSortValue, tokensCell, type PlanningRow, type TicketPlanning } from '../src/lib/planning.ts'
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

test('model names the registry route, or says why there is none', () => {
  const resolved = modelCell(row({ route }))
  assert.equal(resolved.text, 'Codex astra · xhigh')
  assert.match(resolved.tip, /^Build hard · backend\nModel registry, revision 3f9a1c2b$/)
  assert.equal(modelCell(row(undefined, {})).text, '')
  assert.match(modelCell(row(undefined, {})).tip, /Set a role and area/)
  assert.match(modelCell(row({ route_gap: 'area' }, { route_role: 'build' })).tip, /Set an area/)
  assert.match(modelCell(row({ route_gap: 'review_gate' }, { route_role: 'review-gate', area: 'backend' })).tip, /family other than the author's/)
  assert.match(modelCell(row({ route_gap: 'registry' }, { route_role: 'mechanical', area: 'docs' })).tip, /no available route/)
  assert.match(modelCell(row(undefined, {}, 'epic')).tip, /Epics take no model/)
})

test('tokens read spent / estimated with the calibration basis', () => {
  const both = tokensCell(row({ route, tokens: tokens(1_540_000, 10_000_000, { calibration: { basis: 'default', tickets: 0, tokens_per_hour: 5_000_000 } }) }))
  assert.deepEqual([both.spent, both.estimated, both.over], ['1.54M', '10M', false])
  assert.match(both.tip, /Estimated 10,000,000 tokens\n2h at 5M\/h: default 5M\/h until 5 finished tickets on Codex astra · xhigh/)
  assert.equal(both.label, '1.54M tokens spent, 10M estimated')
  const median = tokensCell(row({ route, tokens: tokens(null, 12_000_000, { calibration: { basis: 'median', tickets: 12, tokens_per_hour: 6_000_000 } }) }))
  assert.equal(median.spent, '')
  assert.match(median.tip, /2h at 6M\/h: median of the last 12 finished tickets on Codex astra · xhigh/)
  const anyRoute = tokensCell(row({ tokens: tokens(null, 5_000_000, { calibration: { basis: 'default', tickets: 0, tokens_per_hour: 5_000_000, any_route: true } }) }))
  assert.match(anyRoute.tip, /on any route/)
  // Over the estimate: flagged in text, not only by colour.
  const over = tokensCell(row({ tokens: tokens(12_000_000, 10_000_000) }))
  assert.equal(over.over, true)
  assert.match(over.label, /over the estimate/)
  assert.match(over.tip, /Over the estimate by 2M/)
  const epic = tokensCell(row({ tokens: tokens(3_000_000, 15_000_000), children: { total: 3, estimated: 2 } }, {}, 'epic'))
  assert.match(epic.tip, /Sum of 2 of 3 open and done children with an estimate/)
  const unreported = tokensCell(row({ tokens: { ...tokens(1000, null), unreported: 1 } }))
  assert.match(unreported.tip, /1 session has no usage report yet/)
  assert.equal(tokensCell(row({ tokens: tokens(null, null) })).label, '')
})

test('≈ cost is always approximate; paid names the plan and counts it as $0', () => {
  const list = listCostCell(row({ route, tokens: tokens(1_540_000, 10_000_000, { calibration: { basis: 'default', tickets: 0, tokens_per_hour: 5_000_000 } }), cost: cost({ list_spent: '4.480000', list_estimated: '27.000000', list_unpriced: true }) }))
  assert.deepEqual([list.spent, list.estimated, list.over], ['$4.48', '$27', false])
  assert.match(list.label, /^approximately \$4.48 spent \$27 estimated$/)
  assert.match(list.tip, /At API list prices/)
  assert.match(list.tip, /Estimated ≈ \$27.00 \(\$13.50\/h\)/)
  assert.match(list.tip, /lower bound/)
  const paid = paidCell(row({ tokens: tokens(1_540_000, 10_000_000), cost: cost({ paid_spent: '0.000000', paid_estimated: '0.000000', plans: ['Max 20x'] }) }))
  assert.deepEqual([paid.spent, paid.estimated], ['$0', '$0'])
  assert.match(paid.tip, /Max 20x/)
  const overPaid = paidCell(row({ tokens: tokens(1, 1), cost: cost({ paid_spent: '30', paid_estimated: '27' }) }))
  assert.equal(overPaid.over, true)
  // Without cost (no harness.read) nothing shows.
  assert.equal(listCostCell(row({ tokens: tokens(1, 1) })).label, '')
  assert.equal(paidCell(row({ tokens: tokens(1, 1) })).label, '')
})

test('presence and sort values follow the list API', () => {
  const rows = [row({ route, tokens: tokens(5, null), cost: cost({ list_spent: '1' }) }), row(undefined, {})]
  assert.deepEqual(planningPresent(rows), { model: true, tokens: true, list_cost: true, paid: false })
  assert.deepEqual(planningPresent([row(undefined, {})]), { model: false, tokens: false, list_cost: false, paid: false })
  assert.equal(planningSortValue(rows[0]!, 'model'), '3:backend')
  assert.equal(planningSortValue(rows[1]!, 'model'), null)
  assert.equal(compareModelSort(rows[0]!, rows[1]!, false), -1)
  assert.equal(planningSortValue(rows[0]!, 'tokens'), 5)
  assert.equal(planningSortValue(rows[0]!, 'list_cost'), 1)
  assert.equal(planningSortValue(rows[0]!, 'paid'), null)
})

test('numeric planning sorts use spent else estimated, null last and ID ties', () => {
  const high = { ...row({ tokens: tokens(null, 100_000_000), cost: cost({ list_estimated: '100', paid_estimated: '100' }) }), id: 'a' } as ListItem
  const low = { ...row({ tokens: tokens(null, 10_000_000), cost: cost({ list_estimated: '10', paid_estimated: '10' }) }), id: 'c' } as ListItem
  const tie = { ...low, id: 'b' }
  const zero = { ...row({ tokens: tokens(0, 200_000_000), cost: cost({ list_spent: '0', list_estimated: '200', paid_spent: '0', paid_estimated: '200' }) }), id: 'd' } as ListItem
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

test('model sort keeps a missing area last in both directions', () => {
  const withArea = { ...row(undefined, { route_role: 'build', area: 'frontend' }), id: 'a' } as ListItem
  const noArea = { ...row(undefined, { route_role: 'build' }), id: 'b' } as ListItem
  const higher = { ...row(undefined, { route_role: 'build-hard', area: 'backend' }), id: 'c' } as ListItem
  const none = { ...row(undefined, {}), id: 'd' } as ListItem
  const rows = [noArea, none, higher, withArea]
  assert.deepEqual([...rows].sort(compareRows([{ field: 'model', desc: false }])).map(item => item.id), ['a', 'b', 'c', 'd'])
  assert.deepEqual([...rows].sort(compareRows([{ field: 'model', desc: true }])).map(item => item.id), ['c', 'a', 'b', 'd'])
})
