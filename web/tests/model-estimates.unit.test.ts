// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'
import ModelEstimateHint from '../src/components/settings/ModelEstimateHint.vue'
import { modelEstimateHint, modelEstimateHistory, type ModelEstimateHistory } from '../src/lib/modelEstimates'
import { modelCell, tokensCell, listCostCell, type PlanningRow } from '../src/lib/planning'

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
  it('shows model-adjusted hours and their speed sample count', () => {
    expect(modelCell(row).tip).toContain('~2.4 h (×1.2, n=12)')
    const copy = structuredClone(row)
    copy.planning!.model_estimate = { ...measured, hours: null, speed_factor: null, speed_tickets: 4 }
    expect(modelCell(copy).tip).toContain('Model-adjusted hours: uncalibrated (n=4)')
    expect(modelCell(copy).tip).not.toContain('×')
  })
})
