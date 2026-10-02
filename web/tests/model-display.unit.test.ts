// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'
import HarnessMark from '../src/components/agents/HarnessMark.vue'
import { BRAND_MARKS } from '../src/components/agents/brandMarks'
import EffortMeter from '../src/components/work/EffortMeter.vue'
import { DEFAULT_MODEL_DISPLAY, effortLevel, effortTip, fullModelName, modelCell, shownModelName, type PlanningRow } from '../src/lib/planning'

describe('shared effort meter', () => {
  it.each([0, 1, 2, 3, 4, 5])('fills level %i from the bottom using one level colour', async level => {
    const svg = await renderToString(createSSRApp(EffortMeter, { level }))
    expect(svg).toContain('width="6.5" height="12"')
    expect(svg.match(/<rect /g)).toHaveLength(5)
    expect(svg.match(/class="on"/g) ?? []).toHaveLength(level)
    if (level) expect(svg).toContain(`--eff:var(--eff-${level})`)
    const ys = [...svg.matchAll(/ y="([\d.]+)"/g)].map(m => Number(m[1]))
    expect(ys).toEqual([10.25, 7.75, 5.25, 2.75, .25])
  })
  it('hides unknown, invalid and disabled meters, including an explicitly reported zero', async () => {
    for (const level of [null, -1, 6, 1.5]) expect(await renderToString(createSSRApp(EffortMeter, { level }))).not.toContain('<svg')
    expect(await renderToString(createSSRApp(EffortMeter, { level: 0, enabled: false }))).not.toContain('<svg')
    expect(effortLevel('high')).toBeNull()
    expect(effortLevel(0)).toBe(0)
    expect(effortTip(0)).toBe('Effort minimal · 0 of 5')
    for (const level of [null, undefined, 'xhigh', -1, 6, 1.5]) expect(effortTip(level)).toBe('Effort not reported')
  })
  it('uses the registered provider mark for models hosted by another harness', async () => {
    const mark = await renderToString(createSSRApp(HarnessMark, { harness: 'pi', provider: 'anthropic', size: 12 }))
    expect(mark).toContain(`viewBox="${BRAND_MARKS.anthropic.viewBox}"`)
  })
  it('marks planned meters for the muted theme token', async () => {
    expect(await renderToString(createSSRApp(EffortMeter, { level: 4, planned: true }))).toContain('class="effort planned"')
  })
})

describe('registry model names and versions', () => {
  const opus = { label: 'old alias', harness: 'claude', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5' }
  it('supports all four choices without changing the full identity', () => {
    expect(shownModelName(opus)).toBe('Opus 5.5')
    expect(shownModelName(opus, { ...DEFAULT_MODEL_DISPLAY, modelNames: 'full' })).toBe('Claude Opus 5.5')
    expect(shownModelName(opus, { ...DEFAULT_MODEL_DISPLAY, modelVersion: 'hide' })).toBe('Opus')
    expect(shownModelName(opus, { ...DEFAULT_MODEL_DISPLAY, modelNames: 'full', modelVersion: 'hide' })).toBe('Claude Opus')
    expect(fullModelName(opus)).toBe('Claude Opus 5.5')
    expect(shownModelName({ ...opus, model_version: '' })).toBe('Opus')
    // No string guessing when the registry supplies names or an unknown version.
    expect(shownModelName({ ...opus, display_name: 'Claude Model 2000', short_name: 'Model 2000', model_version: '' }, { ...DEFAULT_MODEL_DISPLAY, modelVersion: 'hide' })).toBe('Model 2000')
  })
  it('distinguishes declared versions when planned and used profiles share an alias', () => {
    const planned = { ...opus, profile: 'opus-5', model: 'opus', model_version: '5', effort: 'high', revision: '2' }
    const row: PlanningRow = { kind_slug: 'ticket', fields: {}, planning: { route: planned, tokens: { spent: 1, estimated: null, input: 1, output: 0, cached: 0, sessions: 1, unreported: 0 }, models: [{ ...opus, model: 'opus', sessions: [{ id: 's1', effort: 'high', effort_level: 3, role: 'builder', running: false, tokens: 1 }] }] } }
    const cell = modelCell(row, { effortMeter: false, modelNames: 'short', modelVersion: 'hide' })
    expect(cell.tip).toContain('Used: Claude Opus 5.5')
    expect(cell.tip).toContain('Planned: Claude Opus 5 · high (a different model ran)')
    expect(cell.tip).not.toContain('s1')
    row.planning!.route!.model_version = '5.5'
    expect(modelCell(row).tip).toContain('Planned: Claude Opus 5.5 · high, as used')
  })
  it.each([
    [undefined, undefined, true], ['', '', true], [undefined, '', true], ['', undefined, true],
    [undefined, '5.5', false], ['', '5.5', false], ['5.5', undefined, false], ['5.5', '', false],
    ['5.5', '5.5', true], ['5', '5.5', false],
  ] as const)('compares planned version %s and used version %s without a wildcard', (plannedVersion, usedVersion, same) => {
    const planned = { ...opus, profile: 'opus', model: 'opus', model_version: plannedVersion, effort: 'high', revision: '2' }
    const row: PlanningRow = { kind_slug: 'ticket', fields: {}, planning: { route: planned, tokens: { spent: 1, estimated: null, input: 1, output: 0, cached: 0, sessions: 1, unreported: 0 }, models: [{ ...opus, model: 'opus', model_version: usedVersion, sessions: [{ id: 's1', effort: 'high', effort_level: 3, role: 'worker', running: false, tokens: 1 }] }] } }
    // Hidden versions still participate in identity; omitted and empty are equivalent.
    const tip = modelCell(row, { ...DEFAULT_MODEL_DISPLAY, modelVersion: 'hide' }).tip
    expect(tip.split('\n').at(-1)).toBe(`Planned: ${fullModelName(planned)} · high${same ? ', as used' : ' (a different model ran)'}`)
    expect(tip).not.toContain('s1')
  })
  it.each(['default', 'ultra', 'xhigh', ''])('reports unknown effort for raw setting %s without guessing a level', effort => {
    for (const level of [undefined, null]) for (const effortMeter of [true, false]) {
      const route = { label: 'Cursor Composer', display_name: 'Cursor Composer', short_name: 'Composer', harness: 'cursor', model: 'composer', profile: 'composer', effort, effort_level: level, revision: '2' }
      const row: PlanningRow = { kind_slug: 'ticket', fields: {}, planning: { route, tokens: { spent: null, estimated: null, input: 0, output: 0, cached: 0, sessions: 0, unreported: 0 } } }
      const prefs = { ...DEFAULT_MODEL_DISPLAY, effortMeter }
      expect(modelCell(row, prefs)).toMatchObject({ state: 'planned', effort: null })
      expect(modelCell(row, prefs).tip).toContain('Effort not reported')
      expect(modelCell(row, prefs).label).toContain('Effort not reported')
      row.planning!.models = [{ ...route, sessions: [{ id: 'private-id', effort, effort_level: level, role: 'worker', running: true, tokens: null }] }]
      expect(modelCell(row, prefs)).toMatchObject({ state: 'measured', effort: null })
      expect(modelCell(row, prefs).tip).toContain('Effort not reported')
      expect(modelCell(row, prefs).label).toContain('Effort not reported')
      expect(modelCell(row, prefs).tip).not.toMatch(/of 5|private-id/)
    }
  })
  it('retains unknown effort alongside known effort in grouped used-model hovers', () => {
    const row: PlanningRow = { kind_slug: 'ticket', fields: {}, planning: { route: null, tokens: { spent: 1, estimated: null, input: 1, output: 0, cached: 0, sessions: 2, unreported: 1 }, models: [{ ...opus, model: 'opus', sessions: [
      { id: 'known', effort: 'high', effort_level: 3, role: 'worker', running: false, tokens: 1 },
      { id: 'unknown', effort: 'ultra', role: 'worker', running: false, tokens: null },
    ] }] } }
    const cell = modelCell(row)
    expect(cell.effort).toBe(3)
    expect(cell.tip).toContain('Effort high · 3 of 5 · Effort not reported')
  })
  it('keeps full hover/screen-reader identities and effort when the drawing is off', () => {
    const row: PlanningRow = { kind_slug: 'ticket', fields: {}, planning: { route: null, tokens: { spent: 1, estimated: null, input: 1, output: 0, cached: 0, sessions: 1, unreported: 0 }, models: [{ ...opus, model: 'opus', sessions: [{ id: 's1', effort: 'max', effort_level: 5, role: 'builder', running: false, tokens: 1 }] }] } }
    const cell = modelCell(row, { effortMeter: false, modelNames: 'short', modelVersion: 'hide' })
    expect(cell).toMatchObject({ text: 'Opus', effort: 5, fullName: 'Claude Opus 5.5' })
    expect(cell.label).toContain('Effort max · 5 of 5')
    expect(cell.tip).toContain('Claude Opus 5.5')
    expect(cell.tip).toContain('Effort max · 5 of 5')
    delete row.planning!.models![0]!.sessions[0]!.effort_level
    expect(modelCell(row).effort).toBeNull()
  })
})

describe('per-person Display preference persistence', () => {
  it('defaults to On/Short/Show, merges other preferences, and reloads from the server', async () => {
    vi.resetModules()
    let stored: Record<string, unknown> = { density: 'compact', headerGraph: false }
    const fetch = vi.fn(async (_input: unknown, init?: RequestInit) => {
      if (init?.method === 'PUT') stored = JSON.parse(String(init.body)).value
      return new Response(JSON.stringify({ value: stored }), { status: 200 })
    })
    vi.stubGlobal('fetch', fetch)
    try {
      const { setPreferenceOwner } = await import('../src/lib/preferences')
      setPreferenceOwner({ tenant: { id: 'test-tenant' }, principal: { id: 'test-person' } })
      const { useModelDisplay } = await import('../src/lib/prefs')
      const prefs = useModelDisplay(); await prefs.ready
      expect(prefs.modelDisplay.value).toEqual(DEFAULT_MODEL_DISPLAY)
      prefs.set('effortMeter', false); prefs.set('modelNames', 'full'); prefs.set('modelVersion', 'hide')
      await vi.waitFor(() => expect(stored).toMatchObject({ density: 'compact', headerGraph: false, effortMeter: false, modelNames: 'full', modelVersion: 'hide' }))
      expect(fetch.mock.calls.some(([path, init]) => decodeURIComponent(String(path)).includes('/preferences/list:display') && init?.method === 'PUT')).toBe(true)
      vi.resetModules()
      ;(await import('../src/lib/preferences')).setPreferenceOwner({ tenant: { id: 'test-tenant' }, principal: { id: 'test-person' } })
      const reloaded = (await import('../src/lib/prefs')).useModelDisplay(); await reloaded.ready
      expect(reloaded.modelDisplay.value).toEqual({ effortMeter: false, modelNames: 'full', modelVersion: 'hide' })
    } finally { vi.unstubAllGlobals() }
  })
})
