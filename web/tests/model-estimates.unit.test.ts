// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'
import ModelEstimateHint from '../src/components/settings/ModelEstimateHint.vue'
import { modelEstimateHint, modelEstimateHistory, type ModelEstimateHistory } from '../src/lib/modelEstimates'
import { modelCell, tokensCell, listCostCell, planningSortValue, type PlanningRow } from '../src/lib/planning'

vi.mock('../src/lib/api', () => ({ api: vi.fn() }))

const measured: ModelEstimateHistory = { state: 'calibrated', basis: 'median of 12 complex backend tickets on astra xhigh (cell; based on 6; n=12)', tickets: 12, hours: 2.4, tokens: 1_900_000, speed_factor: 1.2, speed_tickets: 12 }

describe('model picker history', () => {
  it('names the work, complexity and sample count', () => {
    expect(modelEstimateHint(measured, 'Backend', 'complex')).toBe('typically ~2.4 h · ~1.9M tokens for complex Backend (n=12)')
  })
  it('shows nothing for insufficient or invalid history', () => {
    for (const history of [null, undefined, { ...measured, tickets: 4 }, { ...measured, state: 'uncalibrated' as const }, { ...measured, hours: null }, { ...measured, tokens: null }, { ...measured, hours: NaN }, { ...measured, tokens: Infinity }]) {
      expect(modelEstimateHint(history, 'Backend', 'complex')).toBe('')
    }
  })
  it('keeps the hint element in place while loading or uncalibrated', async () => {
    for (const history of [undefined, { ...measured, tickets: 4 }, measured]) {
      const html = await renderToString(createSSRApp(ModelEstimateHint, { history, kindLabel: 'Backend', bucket: 'complex' }))
      expect(html).toContain('class="model-estimate-hint"')
      expect(html).toContain('aria-live="polite"')
      if (history?.tickets === 12) { expect(html).toContain('n=12'); expect(html).toContain(measured.basis) }
      else expect(html).not.toContain('typically')
    }
  })
  it('passes the captured picker context and cancellation signal to the API', async () => {
    const { api } = await import('../src/lib/api')
    vi.mocked(api).mockResolvedValue(new Response(JSON.stringify(measured)))
    const signal = new AbortController().signal
    await expect(modelEstimateHistory('p', 'backend', 'complex', 'project', signal)).resolves.toEqual(measured)
    expect(api).toHaveBeenCalledWith('/usage/model-estimates?profile_id=p&kind=backend&bucket=complex&project_id=project', { signal })
    vi.mocked(api).mockResolvedValue(new Response('', { status: 403 }))
    await expect(modelEstimateHistory('p', 'backend', 'complex')).rejects.toThrow('could not be loaded')
  })
})

describe('honest planning estimates', () => {
  const row: PlanningRow = { kind_slug: 'ticket', fields: { estimate_hours: 2 }, planning: {
    route: { label: 'Codex astra', profile: 'p', harness: 'codex', model: 'gpt-6-astra', effort: 'xhigh', revision: '1' },
    model_estimate: { ...measured, hours: 2.4 },
    tokens: { spent: null, input: 0, output: 0, cached: 0, sessions: 0, unreported: 0, estimated: 10_000_000,
      calibration: { basis: 'default', tickets: 0, tokens_per_hour: 5_000_000, level: 'default', basis_text: 'uncalibrated (n=0)' } },
    cost: { list_spent: null, list_estimated: '10.000000', list_unpriced: false, paid_spent: null, paid_estimated: null, paid_unknown: true, plans: [] },
  } }
  it('does not display fallback tokens or cost as measured model history', () => {
    expect(tokensCell(row).estimated).toBe('')
    expect(tokensCell(row).tip).toContain('Uncalibrated')
    expect(listCostCell(row).estimated).toBe('')
    expect(listCostCell(row).tip).toContain('Uncalibrated')
  })
  it('names the calibration evidence when cost is shown without tokens', () => {
    const copy = structuredClone(row)
    copy.planning!.tokens.calibration = { basis: 'median', tickets: 12, tokens_per_hour: 800_000, level: 'cell', basis_text: measured.basis }
    expect(tokensCell(copy).tip).toContain(measured.basis)
    expect(listCostCell(copy).tip).toContain(measured.basis)
  })
  it('shows model-adjusted hours and their speed sample count', () => {
    expect(modelCell(row).tip).toContain('~2.4 h (time factor 1.2, n=12)')
    const copy = structuredClone(row)
    copy.planning!.model_estimate = { ...measured, hours: null, speed_factor: null, speed_tickets: 4 }
    expect(modelCell(copy).tip).toContain('Model-adjusted hours: uncalibrated (n=4)')
    expect(modelCell(copy).tip).not.toContain('time factor')
  })
  it('keeps frozen fallback comparisons with an uncalibrated label', () => {
    const copy = structuredClone(row)
    copy.state = 'in_progress'
    copy.planning!.tokens.spent = 11_000_000
    copy.planning!.cost!.list_spent = '11.000000'
    copy.planning!.estimate_snapshot = { id: 's', started_at: '2026-10-02T10:00:00Z', source: 'session', estimate_hours: 2,
      estimated_tokens: 10_000_000, estimated_cost_usd: '10.000000', route: copy.planning!.route,
      rate_basis: { basis: 'default', tickets: 0, tokens_per_hour: 5_000_000 } }
    for (const cell of [tokensCell(copy), listCostCell(copy)]) {
      expect(cell.estimated).not.toBe('')
      expect(cell.over).toBe(true)
      expect(cell.tip).toContain('Uncalibrated')
      expect(cell.tip).toContain('Estimate taken when work started')
    }
  })
  it('sorts live fallback estimates as missing and preserves measured values', () => {
    expect(planningSortValue(row, 'tokens')).toBeNull()
    const copy = structuredClone(row)
    copy.planning!.tokens.spent = 0
    expect(planningSortValue(copy, 'tokens')).toBe(0)
  })
  it('labels epic sums partial when uncalibrated children are excluded', () => {
    const copy = structuredClone(row)
    copy.kind_slug = 'epic'
    delete copy.planning!.tokens.calibration
    copy.planning!.children = { total: 3, estimated: 1, uncalibrated: 2 }
    for (const cell of [tokensCell(copy), listCostCell(copy)]) {
      expect(cell.tip).toContain('partial: 2 uncalibrated children excluded')
      expect(cell.tip).toContain('Sum of 1 of 3')
    }
  })

})
