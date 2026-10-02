// SPDX-License-Identifier: AGPL-3.0-only
// AEON-511 approved planning fragment, persistence and usage permission gates.
import { mkdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, type Fixtures } from './work-fixtures'
import type { TicketPlanning } from '../src/lib/planning'

const shots = process.env.PLANNING_SHOTS ?? '../.agent-shots/planning'
const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const route = { display_name: 'Codex Sol', short_name: 'Sol', model_version: '6.1', effort_level: 4, label: 'Codex sol · xhigh', profile: 'codex-sol-xhigh', harness: 'codex', model: 'gpt-6-sol', effort: 'xhigh', revision: '3f9a1c2b' }
// This whole response comes from TestPlanningListHoverFixture, including the
// server's usage-less session flags. Serve its bytes without a client adapter.
const serverListBody = readFileSync(new URL('./fixtures/planning-list.json', import.meta.url), 'utf8')
function planning(spent: number | null, estimated: number | null, running = 0): TicketPlanning {
  return { route, tokens: { spent, estimated, running, input: spent ?? 0, output: 0, cached: Math.round((spent ?? 0) * .8), sessions: spent === null ? 0 : 1, unreported: 0 } }
}
function cost(spent: string | null, estimated: string | null, subscription = false): NonNullable<TicketPlanning['cost']> {
  return { list_spent: spent, list_estimated: estimated, list_unpriced: false, paid_spent: subscription ? '0' : spent, paid_estimated: estimated, paid_unknown: false, plans: subscription ? ['Pro'] : [], billing_modes: [subscription ? 'subscription' : 'api'] }
}
function world(): Fixtures {
  const data = fixtures()
  const set = (key: string, plan: TicketPlanning) => { data.nodes.find(n => n.key === key)!.planning = plan }
  const planned = planning(null, 2_400_000); planned.cost = cost(null, '4.20'); set('PHAROS-11', planned)
  const live = planning(1_100_000, 2_400_000, 1); live.cost = cost('1.93', '4.20')
  live.models = [{ display_name: 'Codex Sol', short_name: 'Sol', model_version: '6.1', label: 'Codex sol', harness: 'codex', model: 'gpt-6-sol', sessions: [{ id: 's-live', effort: 'xhigh', effort_level: 4, role: 'worker', running: true, tokens: 1_100_000 }] }]; set('PHAROS-12', live)
  const done = planning(1_900_000, 99_000_000); done.cost = cost('3.33', '999')
  done.models = [{ display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5', label: 'Claude opus', harness: 'claude', model: 'opus', sessions: [{ id: 's-done', effort: 'high', effort_level: 3, role: 'worker', running: false, tokens: 1_900_000 }] }, { display_name: 'Codex Sol', short_name: 'Sol', model_version: '6.1', label: 'Codex sol', harness: 'codex', model: 'gpt-6-sol', sessions: [{ id: 's-other', effort: 'xhigh', effort_level: 4, role: 'worker', running: false, tokens: 0 }] }]
  done.estimate_snapshot = { id: 'snapshot', started_at: '2026-10-01T09:12:00Z', source: 'session', estimate_hours: 3, estimated_tokens: 2_400_000, estimated_cost_usd: '4.20', route, rate_basis: { basis: 'median', tickets: 12, tokens_per_hour: 800_000 } }; set('PHAROS-13', done)
  const over = planning(2_160_000, 1_600_000); over.models = [{ display_name: 'Codex Sol', short_name: 'Sol', model_version: '6.1', label: 'Codex sol', harness: 'codex', model: 'gpt-6-sol', sessions: [{ id: 's-over', effort: 'xhigh', effort_level: 4, role: 'worker', running: false, tokens: 2_160_000 }] }]; over.cost = cost('3.78', '2.80'); set('PHAROS-14', over)
  const plan = planning(1_770_000, 2_000_000); plan.models = [{ display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5', label: 'Claude opus', harness: 'claude', model: 'opus', sessions: [{ id: 's-plan', effort: 'high', effort_level: 3, role: 'worker', running: false, tokens: 1_770_000 }] }]; plan.cost = cost('3.10', '3.50', true); set('PHAROS-15', plan)
  data.preferences['list:p-pharos'] = { visible: ['status', 'estimate', 'model', 'tokens', 'list_cost'] }
  return data
}
const display = (page: Page) => page.getByRole('button', { name: 'Display: Display' }).click()

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-10-01T12:00:00Z')) })

test('server list response preserves exact usage-less and mixed-session hovers', async ({ page }) => {
  const data = fixtures()
  const project = JSON.parse(serverListBody).items[0].project
  data.projects[0]!.id = project.id
  data.projects[0]!.key = project.key
  data.projects[0]!.title = project.title
  data.preferences[`list:${project.id}`] = { visible: ['model', 'tokens', 'list_cost'] }
  await mockWork(page, data)
  await page.route('**/api/nodes?**', async route => {
    if (new URL(route.request().url()).searchParams.get('kind') === 'project') return route.fallback()
    await route.fulfill({ contentType: 'application/json', body: serverListBody })
  })
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  const assertTip = async (key: string, column: string, expected: string) => {
    const cell = row(page, key).locator(`.${column} .plan-figure`)
    await expect(cell).toHaveAccessibleDescription(expected)
    await cell.hover()
    await expect(page.locator('.tooltip')).toHaveText(expected)
  }
  await expect(row(page, 'HOVER-1').locator('.c-tokens .plan-figure')).toHaveText('—')
  await assertTip('HOVER-1', 'c-tokens', 'Usage not reported yet\n1 session running on Cursor grok-4.7')
  await assertTip('HOVER-2', 'c-tokens', 'Usage not reported yet')
  await assertTip('HOVER-6', 'c-tokens', 'Uncalibrated · insufficient model history (n=0)\n1 session running on Cursor grok-4.7')
  for (const key of ['HOVER-1', 'HOVER-2']) await assertTip(key, 'c-list-cost', 'Billing not reported yet')
  await assertTip('HOVER-6', 'c-list-cost', 'Uncalibrated · insufficient model history (n=0)')
  await assertTip('HOVER-3', 'c-tokens', 'Measured so far 1.1M · Cursor grok-4.7\n1 session running · input 1,100,000 (0 cached) · output 0')
  for (const [key, spent, input, value] of [['HOVER-4', '1M', '1,000,000', '$2.00'], ['HOVER-5', '0', '0', '$0']] as const) {
    await assertTip(key, 'c-tokens', `Measured so far ${spent} · Cursor grok-4.7\n1 session running · input ${input} (0 cached) · output 0\n1 session has no usage report yet`)
    await assertTip(key, 'c-list-cost', `Measured so far ${value}\nAPI-billed · at list prices`)
  }
  await assertTip('HOVER-7', 'c-list-cost', 'Measured so far $2.00\nAPI-billed · Billing not reported yet · at list prices\nPart of this has no list price, so it is a lower bound\nPart of this has no billing on record')
  await assertTip('HOVER-8', 'c-list-cost', 'Included in your plan · list value $2.00\nSubscription: not charged per use\nPro')
  await expect(row(page, 'HOVER-8').locator('.c-list-cost .plan-tag')).toHaveText('plan')
})

test('approved cells show estimates, running figures, measured checks and session models', async ({ page }) => {
  await mockWork(page, world())
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  for (const name of ['Model', 'Tokens', 'Cost']) await expect(page.getByRole('columnheader', { name: new RegExp(`^${name}\\b`) })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: 'Paid', exact: true })).toHaveCount(0)
  await expect(row(page, 'PHAROS-11').locator('.c-tokens .plan-figure')).toHaveText('~2.4M')
  await expect(row(page, 'PHAROS-11').locator('.plan-model')).toHaveText('~Sol 6.1')
  await expect(row(page, 'PHAROS-11').locator('.plan-model')).toHaveAttribute('aria-label', /estimated/)
  await expect(row(page, 'PHAROS-12').locator('.c-tokens .plan-figure')).toHaveText('1.1M/~2.4M')
  await expect(row(page, 'PHAROS-12').locator('.c-tokens .plan-figure')).toHaveAccessibleDescription(/Measured so far 1.1M · estimated ~2.4M \(46%\) · Codex sol/)
  await expect(row(page, 'PHAROS-12').locator('.c-list-cost .plan-figure')).toHaveText('$1.93/~$4.20')
  await expect(row(page, 'PHAROS-12').locator('.c-list-cost .plan-figure')).toHaveAccessibleDescription(/Measured so far \$1.93 · estimated ~\$4.20 \(46%\)\s+API-billed · at list prices/)
  await expect(row(page, 'PHAROS-12').locator('.plan-model')).toHaveAttribute('data-tip', 'Used: Codex Sol 6.1 · xhigh · Effort xhigh · 4 of 5 · 1 session, running\nPlanned: Codex Sol 6.1 · xhigh, as used')
  const measured = row(page, 'PHAROS-13')
  await expect(measured.locator('.c-tokens .plan-figure')).toHaveText('1.9M')
  await expect(measured.locator('.c-tokens .measured')).toHaveCount(1)
  await expect(measured.locator('.c-tokens .plan-figure')).toHaveAccessibleName(/measured/)
  await expect(measured.locator('.c-tokens .plan-figure')).toHaveAccessibleDescription(/Estimated ~2.4M · measured 1.9M \(−21%\)/)
  await expect(measured.locator('.plan-model')).toHaveText('Opus 5.5+1')
  await expect(measured.locator('.plan-model')).toHaveAttribute('data-tip', 'Used, per session:\nClaude Opus 5.5 · high · Effort high · 3 of 5 · 1 session · 1.9M\nCodex Sol 6.1 · xhigh · Effort xhigh · 4 of 5 · 1 session · 0\nPlanned: Codex Sol 6.1 · xhigh')
  await expect(row(page, 'PHAROS-14').locator('.c-tokens .plan-figure')).not.toHaveClass(/over/)
  await expect(row(page, 'PHAROS-14').locator('.c-tokens .plan-figure')).toHaveAttribute('data-tip', /\(\+35%\)/)
  await expect(row(page, 'PHAROS-15').locator('.c-list-cost .plan-figure')).toHaveText('plan$3.10')
  await expect(row(page, 'PHAROS-15').locator('.c-list-cost .plan-figure')).toHaveAccessibleDescription(/Included in your plan · list value \$3.10/)
  for (const key of ['PHAROS-11', 'PHAROS-12', 'PHAROS-13']) expect(await row(page, key).locator('.c-tokens .slot').evaluate(el => el.getBoundingClientRect().width)).toBe(12)
  await measured.locator('.c-tokens .plan-figure').hover()
  await expect(page.locator('.tooltip')).toHaveText(/Estimate taken when work started/)
  await expect(page.locator('.plan-model[tabindex], .plan-figure[tabindex]')).toHaveCount(0)
})

test('Cursor Grok hovers match profile keys and explain runs without a planned role', async ({ page }) => {
  const data = world()
  const planned = data.nodes.find(n => n.key === 'PHAROS-12')!.planning!
  planned.route = { ...route, display_name: 'Cursor Grok', short_name: 'Grok', model_version: '4.7', label: 'Cursor grok-4.7 · xhigh', profile: 'cursor-grok-4-7-xhigh', harness: 'cursor', model: 'grok-4.7-xhigh' }
  planned.models = [{ display_name: 'Cursor Grok', short_name: 'Grok', model_version: '4.7', label: 'Cursor grok-4.7', harness: 'cursor', model: 'grok-4.7', sessions: [{ id: 's-grok', effort: 'xhigh', effort_level: 4, role: 'worker', running: true, tokens: 1_100_000 }] }]
  const unplanned = data.nodes.find(n => n.key === 'PHAROS-11')!.planning!
  unplanned.route = null
  unplanned.models = planned.models
  unplanned.tokens.running = 1
  unplanned.tokens.sessions = 1
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  await expect(row(page, 'PHAROS-12').locator('.plan-model')).toHaveAttribute('data-tip', 'Used: Cursor Grok 4.7 · xhigh · Effort xhigh · 4 of 5 · 1 session, running\nPlanned: Cursor Grok 4.7 · xhigh, as used')
  const noPlan = row(page, 'PHAROS-11').locator('.plan-model')
  await expect(noPlan).toHaveAttribute('data-tip', 'Used: Cursor Grok 4.7 · xhigh · Effort xhigh · 4 of 5 · 1 session, running\nNo model planned: no role set')
  await noPlan.hover()
  await expect(page.locator('.tooltip')).toHaveText('Used: Cursor Grok 4.7 · xhigh · Effort xhigh · 4 of 5 · 1 session, running\nNo model planned: no role set')
})

test('unknown planned and used effort stays undrawn and is explained to hover and screen readers', async ({ page }) => {
  const data = world()
  const planned = data.nodes.find(n => n.key === 'PHAROS-11')!.planning!
  planned.route = { ...route, label: 'Cursor Composer · default', display_name: 'Cursor Composer', short_name: 'Composer', model_version: '', harness: 'cursor', model: 'composer', effort: 'default', effort_level: null }
  const used = data.nodes.find(n => n.key === 'PHAROS-12')!.planning!.models![0]!
  used.sessions[0]!.effort = 'ultra'
  delete used.sessions[0]!.effort_level
  const grouped = data.nodes.find(n => n.key === 'PHAROS-13')!.planning!.models!
  delete grouped[1]!.sessions[0]!.effort_level
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  for (const key of ['PHAROS-11', 'PHAROS-12']) {
    const model = row(page, key).locator('.plan-model')
    await expect(model.locator('.effort')).toHaveCount(0)
    await expect(model).toHaveAccessibleName(/Effort not reported/)
    await expect(model).toHaveAccessibleDescription(/Effort not reported/)
    await model.hover()
    await expect(page.locator('.tooltip')).toHaveText(/Effort not reported/)
  }
  await expect(row(page, 'PHAROS-11').locator('.plan-model')).toHaveText('~Composer')
  await expect(row(page, 'PHAROS-12').locator('.plan-model')).toHaveAttribute('data-tip', /ultra · Effort not reported/)
  const mixed = row(page, 'PHAROS-13').locator('.plan-model')
  await expect(mixed.locator('.effort rect.on')).toHaveCount(3)
  await expect(mixed).toHaveAccessibleDescription(/Codex Sol 6.1 · xhigh · Effort not reported/)
  await display(page)
  await page.getByRole('radiogroup', { name: 'Effort meter', exact: true }).getByRole('radio', { name: 'Off', exact: true }).click()
  await page.keyboard.press('Escape')
  for (const key of ['PHAROS-11', 'PHAROS-12']) await expect(row(page, key).locator('.plan-model')).toHaveAccessibleName(/Effort not reported/)
  await expect(mixed.locator('.effort')).toHaveCount(0)
  await expect(mixed).toHaveAccessibleName(/Effort high · 3 of 5/)
})

test('omitted model versions do not match explicit versions even when version display is hidden', async ({ page }) => {
  const data = world()
  const cases = [
    ['PHAROS-11', undefined, '5.5', false], ['PHAROS-12', '5.5', undefined, false],
    ['PHAROS-14', undefined, undefined, true], ['PHAROS-15', '', '', true],
  ] as const
  for (const [key, plannedVersion, usedVersion] of cases) {
    const planning = data.nodes.find(n => n.key === key)!.planning!
    planning.route = { ...route, label: 'Claude Opus · high', display_name: 'Claude Opus', short_name: 'Opus', harness: 'claude', model: 'opus', model_version: plannedVersion, effort: 'high', effort_level: 3 }
    planning.models = [{ label: 'Claude Opus', display_name: 'Claude Opus', short_name: 'Opus', harness: 'claude', model: 'opus', model_version: usedVersion, sessions: [{ id: `session-${key}`, effort: 'high', effort_level: 3, role: 'worker', running: false, tokens: 1 }] }]
  }
  data.preferences['list:display'] = { modelVersion: 'hide' }
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  for (const [key, plannedVersion, , same] of cases) {
    const model = row(page, key).locator('.plan-model')
    await expect(model).toHaveText('Opus')
    const plannedName = ['Claude Opus', plannedVersion].filter(Boolean).join(' ')
    await expect(model).toHaveAttribute('data-tip', new RegExp(`Planned: ${plannedName.replaceAll('.', '\\.')} · high${same ? ', as used' : ' \\(a different model ran\\)'}$`))
    await expect(model).toHaveAccessibleDescription(same ? /as used/ : /a different model ran/)
  }
})

test('planning hovers match effort, running usage and pre-session calibration', async ({ page }) => {
  const data = world()
  const estimate = data.nodes.find(n => n.key === 'PHAROS-11')!.planning!
  estimate.tokens.calibration = { basis: 'median', tickets: 12, tokens_per_hour: 800_000 }
  const live = data.nodes.find(n => n.key === 'PHAROS-12')!.planning!
  live.route = { ...route, display_name: 'Cursor Grok', short_name: 'Grok', model_version: '4.7', label: 'Cursor grok-4.7 · xhigh', profile: 'cursor-grok-4-7-xhigh', harness: 'cursor', model: 'grok-4.7-xhigh' }
  live.models = [{ display_name: 'Cursor Grok', short_name: 'Grok', model_version: '4.7', label: 'Cursor grok-4.7', harness: 'cursor', model: 'grok-4.7-high', sessions: [{ id: 's-high', effort: 'high', effort_level: 3, role: 'worker', running: true, tokens: 1_100_000 }] }]
  live.tokens.calibration = estimate.tokens.calibration
  const unreported = planning(null, null, 1)
  unreported.route = null
  unreported.tokens.sessions = 1
  unreported.tokens.unreported = 1
  unreported.cost = cost(null, null)
  unreported.cost.list_unpriced = true
  unreported.models = [{ label: 'Cursor grok-4.7', harness: 'cursor', model: 'grok-4.7', sessions: [{ id: 's-no-usage', effort: 'xhigh', role: 'worker', running: true, tokens: null }] }]
  data.nodes.find(n => n.key === 'PHAROS-14')!.planning = unreported
  const empty = planning(null, null)
  empty.route = null
  data.nodes.find(n => n.key === 'PHAROS-15')!.planning = empty
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  const estimated = row(page, 'PHAROS-11').locator('.c-tokens .plan-figure')
  const estimateTip = 'Estimated ~2.4M tokens · no agent session yet\n3h at 800k/h: median of the last 12 finished tickets on Codex sol'
  await expect(estimated).toHaveAttribute('data-tip', estimateTip)
  await estimated.hover()
  await expect(page.locator('.tooltip')).toHaveText(estimateTip)
  const model = row(page, 'PHAROS-12').locator('.plan-model')
  const modelTip = 'Used: Cursor Grok 4.7 · high · Effort high · 3 of 5 · 1 session, running\nPlanned: Cursor Grok 4.7 · xhigh'
  await expect(model).toHaveAttribute('data-tip', modelTip)
  await model.hover()
  await expect(page.locator('.tooltip')).toHaveText(modelTip)
  const running = row(page, 'PHAROS-12').locator('.c-tokens .plan-figure')
  await expect(running).toHaveAttribute('data-tip', /\n1 session running · input 1,100,000 \(880,000 cached\) · output 0/)
  await expect(running).not.toHaveAttribute('data-tip', /median of|default .*until/)
  await expect(row(page, 'PHAROS-13').locator('.c-tokens .plan-figure')).not.toHaveAttribute('data-tip', /median of|default .*until/)
  const missingUsage = row(page, 'PHAROS-14').locator('.c-tokens .plan-figure')
  const usageTip = 'Usage not reported yet\n1 session running on Cursor grok-4.7'
  await expect(missingUsage).toHaveText('—')
  await expect(missingUsage).toHaveAccessibleDescription(usageTip)
  await missingUsage.hover()
  await expect(page.locator('.tooltip')).toHaveText(usageTip)
  const noModel = row(page, 'PHAROS-15').locator('.plan-model')
  const emptyTip = 'No agent session yet\nNo model planned: set a role and area'
  await expect(noModel).toHaveAttribute('data-tip', emptyTip)
  await noModel.hover()
  await expect(page.locator('.tooltip')).toHaveText(emptyTip)
})

test('empty saved ticks persist after toggles, narrow desktop layout and reload', async ({ page }) => {
  const data = fixtures(); data.preferences['list:p-pharos'] = { visible: ['status'] }
  await mockWork(page, data)
  await page.setViewportSize({ width: 1000, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  await display(page)
  const picker = page.getByRole('dialog', { name: 'Display options' })
  await expect(picker.getByRole('checkbox', { name: 'Cost', exact: true })).not.toBeChecked()
  await expect(picker.locator('.col-note')).toHaveCount(0)
  for (const name of ['Model', 'Tokens', 'Cost']) await picker.getByRole('checkbox', { name, exact: true }).check()
  for (const name of ['Model', 'Tokens', 'Cost']) await expect(picker.getByRole('checkbox', { name, exact: true })).toBeChecked()
  await expect(picker.locator('.col-note')).toHaveText('Nothing reported in this list yet; cells show —')
  await expect(picker.getByRole('checkbox', { name: 'Paid', exact: true })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(row(page, 'PHAROS-11').locator('.c-list-cost .plan-figure')).toHaveText('—')
  await expect(row(page, 'PHAROS-11').locator('.c-list-cost .plan-figure')).toHaveAttribute('data-tip', 'No agent session yet')
  await expect.poll(() => data.preferences['list:p-pharos']?.visible).toEqual(['status', 'model', 'tokens', 'list_cost'])
  await page.reload()
  for (const name of ['Model', 'Tokens', 'Cost']) await expect(page.getByRole('columnheader', { name: new RegExp(`^${name}\\b`) })).toBeVisible()
  await display(page)
  for (const name of ['Model', 'Tokens', 'Cost']) await expect(picker.getByRole('checkbox', { name, exact: true })).toBeChecked()
  await picker.getByRole('checkbox', { name: 'Cost', exact: true }).uncheck()
  await expect(picker.locator('.col-note')).toHaveCount(0)
  await picker.getByRole('checkbox', { name: 'Cost', exact: true }).check()
  await expect(picker.locator('.col-note')).toHaveText('Nothing reported in this list yet; cells show —')
  await picker.getByRole('button', { name: 'Automatic', exact: true }).click()
  await expect(picker.locator('.col-note')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('columnheader', { name: 'Tokens', exact: true })).toHaveCount(0)
})

test('mixed billing totals carry no plan chip', async ({ page }) => {
  const data = world()
  data.nodes.find(n => n.key === 'PHAROS-13')!.planning!.cost!.billing_modes = ['api', 'subscription']
  data.nodes.find(n => n.key === 'PHAROS-14')!.planning!.cost!.billing_modes = ['subscription', 'unknown']
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  for (const key of ['PHAROS-13', 'PHAROS-14']) {
    const figure = row(page, key).locator('.c-list-cost .plan-figure')
    await expect(figure.locator('.plan-tag')).toHaveCount(0)
    await expect(figure).toHaveAccessibleDescription(/Subscription portion included in your plan/)
  }
  await expect(row(page, 'PHAROS-15').locator('.plan-tag')).toHaveText('plan')
})

test('double-click fits visible planning values without measuring hidden descriptions', async ({ page }) => {
  const data = world()
  data.preferences['list:p-pharos']!.widths = { model: 260, tokens: 180, list_cost: 170 }
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key&closed=1')
  await expect(row(page, 'PHAROS-13')).toBeVisible()
  // Long accessible hovers must not change the fit of these same visible values.
  await page.locator('.cell > .sr-only').evaluateAll(elements => {
    for (const element of elements) element.textContent += ' Full planning description'.repeat(100)
  })
  for (const [id, cls, max] of [['model', 'c-model', 260], ['tokens', 'c-tokens', 180], ['list_cost', 'c-list-cost', 170]] as const) {
    await page.locator(`th.${cls} .col-resize`).dblclick()
    await expect.poll(() => data.preferences['list:p-pharos']?.widths?.[id]).toBeLessThan(max)
    for (const cell of await page.locator(`td.${cls} .plan-figure, td.${cls} .model-name`).all()) {
      expect(await cell.evaluate(el => el.scrollWidth <= el.clientWidth + 1), id).toBe(true)
    }
  }
})

test('legacy Paid preferences and view links map to Cost once', async ({ page }) => {
  const data = fixtures(); data.preferences['list:p-pharos'] = { visible: ['status', 'paid'], order: ['paid', 'status'] }
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  await expect(page.locator('th.c-list-cost')).toHaveCount(1)
  await page.goto('/p/PHAROS?sort=key&cols=paid,tokens,list_cost')
  await expect(page.locator('th.c-list-cost')).toHaveCount(1)
  await expect(page.locator('th.c-tokens')).toHaveCount(1)
})

test('Cost stays behind harness.read even when saved; models and tokens remain visible', async ({ page }) => {
  await mockWork(page, world(), { readOnly: true })
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  await expect(page.locator('th.c-list-cost')).toHaveCount(0)
  await expect(page.locator('th.c-tokens')).toBeVisible()
  await expect(page.locator('th.c-model')).toBeVisible()
  await display(page)
  await expect(page.getByRole('dialog').getByRole('checkbox', { name: 'Cost', exact: true })).toHaveCount(0)
})

test('planning fragment implementation in light and dark; phones retain the card layout', async ({ page }) => {
  const data = world(); await mockWork(page, data)
  mkdirSync(shots, { recursive: true })
  for (const theme of ['light', 'dark']) {
    data.preferences.theme = { choice: theme }
    await page.setViewportSize({ width: 1600, height: 900 })
    await page.goto('/p/PHAROS?sort=key&closed=1')
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    await expect(row(page, 'PHAROS-13')).toBeVisible()
    await page.screenshot({ path: join(shots, `planning-${theme}.png`) })
    const meter = row(page, 'PHAROS-13').locator('.effort')
    expect(await meter.locator('rect.on').first().evaluate(el => getComputedStyle(el).fill)).toBe(theme === 'light' ? 'rgb(14, 111, 108)' : 'rgb(164, 229, 223)')
    const plannedMeter = row(page, 'PHAROS-11').locator('.effort rect.on').first()
    expect(await plannedMeter.evaluate(el => getComputedStyle(el).fill)).toBe(theme === 'light' ? 'rgba(32, 60, 61, 0.42)' : 'rgba(237, 244, 240, 0.42)')
    await display(page)
    await page.getByRole('radiogroup', { name: 'Effort meter', exact: true }).scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(shots, `display-${theme}.png`) })
    await page.keyboard.press('Escape')
    for (const cls of ['c-tokens', 'c-list-cost']) {
      const clipped = await row(page, 'PHAROS-12').locator(`.${cls} .plan-figure`).evaluate(el => el.scrollWidth > el.clientWidth + 1)
      expect(clipped, cls).toBe(false)
    }
    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.locator('td.c-model, td.c-tokens, td.c-list-cost')).toHaveCount(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
    await page.screenshot({ path: join(shots, `planning-phone-${theme}.png`) })
  }
  if (process.env.PLANNING_FRAGMENT) {
    await page.setViewportSize({ width: 1600, height: 1100 })
    await page.setContent(readFileSync(process.env.PLANNING_FRAGMENT, 'utf8'))
    for (const theme of ['light', 'dark']) {
      await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
      await page.screenshot({ path: join(shots, `approved-fragment-${theme}.png`), fullPage: true })
    }
  }
})

test('Display model preferences persist per person and preserve full hovers and effort', async ({ page }) => {
 const data = world(); await mockWork(page, data)
 await page.setViewportSize({ width: 1600, height: 1000 })
 await page.goto('/p/PHAROS?sort=key&closed=1')
 const model = row(page, 'PHAROS-13').locator('.plan-model')
 await expect(model).toHaveText('Opus 5.5+1')
 await expect(model).toHaveAccessibleName(/Claude Opus 5.5, measured, Effort high · 3 of 5/)
 await expect(model.locator('.effort rect.on')).toHaveCount(3)
 expect(await model.locator('.effort').evaluate(el => el.getBoundingClientRect().width)).toBe(6.5)
 expect(await model.locator('.effort').evaluate(el => el.getBoundingClientRect().height)).toBe(12)
 expect(await page.locator('col.c-model').evaluate(el => el.getBoundingClientRect().width)).toBe(176)
 await display(page)
 const panel = page.getByRole('dialog', { name: 'Display options' })
 for (const [setting,choice] of [['Effort meter','On'],['Model names','Short'],['Version','Show']]) {
  await expect(panel.getByRole('radiogroup', { name: setting, exact: true }).getByRole('radio', { name: choice, exact: true })).toHaveAttribute('aria-checked','true')
 }
 await panel.getByRole('radiogroup',{name:'Model names',exact:true}).getByRole('radio',{name:'Full',exact:true}).click()
 await expect(model).toHaveText('Claude Opus 5.5+1')
 await panel.getByRole('radiogroup',{name:'Version',exact:true}).getByRole('radio',{name:'Hide',exact:true}).click()
 await expect(model).toHaveText('Claude Opus+1')
 await panel.getByRole('radiogroup',{name:'Effort meter',exact:true}).getByRole('radio',{name:'Off',exact:true}).click()
 await expect(model.locator('.effort')).toHaveCount(0)
 await expect(model).toHaveAccessibleDescription(/Claude Opus 5.5.*Effort high · 3 of 5/s)
 await expect.poll(() => data.preferences['list:display']).toMatchObject({ effortMeter: false, modelNames: 'full', modelVersion: 'hide' })
 await page.keyboard.press('Escape'); await page.reload()
 await expect(model).toHaveText('Claude Opus+1'); await expect(model.locator('.effort')).toHaveCount(0)
 await display(page)
 await expect(panel.getByRole('radiogroup',{name:'Version',exact:true}).getByRole('radio',{name:'Hide',exact:true})).toHaveAttribute('aria-checked','true')
})
