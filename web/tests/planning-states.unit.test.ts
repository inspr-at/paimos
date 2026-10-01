// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { listCostCell, modelCell, tokensCell, type PlanningRow, type TicketPlanning } from '../src/lib/planning'
import { normalizeColumnIds, pickerColumns, visibleColumns } from '../src/lib/columns'
import { filtersFromQuery } from '../src/lib/ticketList'

const route = { label: 'Codex sol · xhigh', harness: 'codex', model: 'gpt-6-sol', profile: 'codex-sol', effort: 'xhigh', revision: 'abc123' }
function row(spent: number | null, estimated: number | null, running = 0): PlanningRow {
  return { kind_slug: 'ticket', fields: {}, planning: { route, tokens: { spent, estimated, running, sessions: spent === null ? 0 : 1, unreported: 0, input: spent ?? 0, cached: 0, output: 0 } } }
}
const cost = (extra: Partial<NonNullable<TicketPlanning['cost']>> = {}) => ({ list_spent: '3.10', list_estimated: '3.50', list_unpriced: false, paid_spent: '0', paid_estimated: '0', paid_unknown: false, plans: [], ...extra })

describe('approved planning figure states', () => {
  it('maps missing, estimate, running and measured, including a real zero', () => {
    expect(tokensCell(row(null, null))).toMatchObject({ state: 'none', tip: 'No agent session yet' })
    expect(tokensCell(row(null, 2_400_000))).toMatchObject({ state: 'estimated', estimated: '2.4M' })
    expect(tokensCell(row(1_100_000, 2_400_000, 1))).toMatchObject({ state: 'running', spent: '1.1M', estimated: '2.4M', over: false })
    expect(tokensCell(row(0, 2_400_000))).toMatchObject({ state: 'measured', spent: '0', over: false })
    expect(tokensCell(row(3_000_000, 2_400_000))).toMatchObject({ state: 'measured', over: false })
    expect(tokensCell(row(3_000_000, 2_400_000, 1))).toMatchObject({ state: 'running', over: true })
    expect(tokensCell(row(3_000_000, 2_400_000)).tip).toContain('(+25%)')
    expect(tokensCell(row(3_000_000, 2_400_000, 1)).label).toContain('measured so far')
  })
  it('uses immutable work-start token and cost estimates after re-estimation', () => {
    const r = row(1_900_000, 99_000_000)
    r.planning!.cost = cost({ list_spent: '3.33', list_estimated: '999', billing_modes: ['api'] })
    r.planning!.estimate_snapshot = { id: 'snapshot', started_at: '2026-10-01T09:12:00Z', source: 'session', estimate_hours: 3, estimated_tokens: 2_400_000, estimated_cost_usd: '4.20', route, rate_basis: { basis: 'median', tickets: 12, tokens_per_hour: 800_000 } }
    expect(tokensCell(r).tip).toContain('Estimated ~2.4M · measured 1.9M (−21%)')
    expect(listCostCell(r).tip).toContain('Estimated ~$4.20 (−21%)')
    expect(tokensCell(r).tip).toContain('Estimate taken when work started')
    r.planning!.estimate_snapshot.estimated_tokens = null
    delete r.planning!.estimate_snapshot.estimated_cost_usd
    expect(tokensCell(r).estimated).toBe('')
    expect(listCostCell(r).estimated).toBe('')
  })
  it('marks actual subscription usage, never a planned or unknown subscription', () => {
    const r = row(1_770_000, 2_000_000)
    r.planning!.cost = cost({ billing_modes: ['subscription'], plans: ['Pro'] })
    expect(listCostCell(r)).toMatchObject({ state: 'measured', plan: true })
    expect(listCostCell(r).tip).toContain('Included in your plan · list value $3.10')
    r.planning!.cost = cost({ billing_modes: ['api'], plans: ['Future planned subscription'] })
    expect(listCostCell(r).plan).toBe(false)
    expect(listCostCell(r).tip).toContain('API-billed')
    r.planning!.cost = cost({ billing_modes: ['api', 'subscription', 'unknown'], paid_unknown: true })
    expect(listCostCell(r).tip).toContain('Subscription portion included in your plan')
    expect(listCostCell(r).tip).toContain('Billing not reported yet')
  })
  it('explains unreported usage and preserves permission-gated cost absence', () => {
    const r = row(null, null, 1)
    r.planning!.tokens.sessions = 1
    expect(tokensCell(r).tip).toBe('Usage not reported yet')
    expect(listCostCell(r)).toMatchObject({ state: 'none', tip: 'Billing not reported yet' })
  })
})

describe('actual models and saved picker choices', () => {
  it('shows what ran, keeps effort in hovers and explains all sessions', () => {
    const r = row(2_800_000, 3_200_000)
    r.planning!.models = [
      { label: 'Claude opus', harness: 'claude', model: 'opus', sessions: [{ id: 's1', effort: 'high', role: 'reviewer', running: false, tokens: 2_300_000 }] },
      { label: 'Codex gpt-6.1-sol', harness: 'codex', model: 'gpt-6.1-sol', sessions: [{ id: 's2', model_raw: 'gpt-6-sol-xhigh', effort: 'xhigh', role: 'worker', running: true, tokens: 500_000 }] },
    ]
    expect(modelCell(r)).toMatchObject({ text: 'Claude opus', state: 'measured', more: 1 })
    expect(modelCell(r).tip).toContain('session s1 (reviewer)')
    expect(modelCell(r).tip).toContain('reported: gpt-6-sol-xhigh')
    expect(modelCell(r).tip).toContain('Planned: Codex sol · xhigh')
    r.planning!.models.reverse()
    expect(modelCell(r).text).toBe('Codex sol')
    delete r.planning!.models
    expect(modelCell(r)).toMatchObject({ text: 'Codex sol', state: 'planned', more: 0 })
  })
  it('keeps empty saved ticks through width changes, reloads, views and phone cards', () => {
    const prefs = { order: ['model', 'paid', 'tokens'] as const, visible: ['model', 'paid', 'tokens'] as const }
    const saved = JSON.parse(JSON.stringify(prefs))
    const drawn = visibleColumns(740, { phone: false, prefs: saved, present: {} }).columns.map(c => c.id)
    expect(drawn).toEqual(['key', 'title', 'model', 'list_cost', 'tokens'])
    expect(pickerColumns(saved, ['key', 'title', 'status'], true).visible).toEqual(['model', 'list_cost', 'tokens'])
    expect(pickerColumns(saved, drawn, false).visible).toEqual(['model', 'tokens'])
    expect(filtersFromQuery({ cols: 'paid,tokens,list_cost' }).cols).toEqual(['list_cost', 'tokens'])
    expect(normalizeColumnIds(['paid', 'list_cost'])).toEqual(['list_cost'])
    expect(visibleColumns(2600, { phone: false, present: {} }).columns.some(c => c.id === 'tokens')).toBe(false)
    expect(visibleColumns(390, { phone: true, prefs: saved }).columns.some(c => c.id === 'tokens')).toBe(false)
    expect(prefs.visible).toEqual(['model', 'paid', 'tokens'])
  })
})
