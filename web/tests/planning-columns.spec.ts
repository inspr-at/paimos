// SPDX-License-Identifier: AGPL-3.0-only
// AEON-329: Model, Tokens, ≈ Cost and Paid in the ticket list.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, type Fixtures } from './work-fixtures'
import type { TicketPlanning } from '../src/lib/planning'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-329b'
const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const keys = (page: Page) => page.locator('tr.ticket-row:not(.ghost) .key').allTextContents()
const astra = { label: 'Codex astra · xhigh', profile: 'codex-astra-xhigh', harness: 'codex', model: 'gpt-6-astra', effort: 'xhigh', revision: '3f9a1c2b' }
const opus = { label: 'Claude opus · high', profile: 'claude-opus-high', harness: 'claude', model: 'opus', effort: 'high', revision: '3f9a1c2b' }
const tokens = (spent: number | null, estimated: number | null, calibration?: TicketPlanning['tokens']['calibration']) => ({ spent, input: spent ?? 0, output: 0, cached: Math.round((spent ?? 0) * 0.8), sessions: spent === null ? 0 : 2, unreported: 0, estimated, ...(calibration ? { calibration } : {}) })
const cost = (list: [string | null, string | null], paid: [string | null, string | null], plans: string[] = []) => ({ list_spent: list[0], list_estimated: list[1], list_unpriced: false, paid_spent: paid[0], paid_estimated: paid[1], paid_unknown: false, plans })
const PLANNING = ['model', 'tokens', 'list_cost', 'paid']

function world(): Fixtures {
  const data = fixtures()
  const set = (key: string, fields: Record<string, unknown>, planning?: TicketPlanning) => {
    const node = data.nodes.find(n => n.key === key)!
    Object.assign(node.fields, fields)
    if (planning) node.planning = planning
  }
  set('PHAROS-10', {}, { route: null, tokens: tokens(13_940_000, 17_000_000), cost: cost(['22.680000', '41.000000'], ['3.800000', '27.000000'], ['Max 20x']), children: { total: 2, estimated: 2 } })
  set('PHAROS-11', { route_role: 'build-hard', area: 'backend', estimate_hours: 2 }, {
    route: astra, tokens: tokens(1_540_000, 10_000_000, { basis: 'default', tickets: 0, tokens_per_hour: 5_000_000 }),
    cost: cost(['4.480000', '27.000000'], ['3.800000', '27.000000']),
  })
  // Over the estimate: 12.4M spent of 7M.
  set('PHAROS-12', { route_role: 'build', area: 'frontend', estimate_hours: 2 }, {
    route: opus, tokens: tokens(12_400_000, 7_000_000, { basis: 'median', tickets: 12, tokens_per_hour: 3_500_000 }),
    cost: cost(['18.200000', '14.000000'], ['0.000000', '0.000000'], ['Max 20x']),
  })
  set('PHAROS-13', { route_role: 'mechanical', area: 'docs', estimate_hours: 1 }, {
    route: null, route_gap: 'registry', tokens: tokens(null, 5_000_000, { basis: 'default', tickets: 0, tokens_per_hour: 5_000_000, any_route: true }),
    cost: cost([null, null], [null, null]),
  })
  set('PHAROS-14', { route_role: 'review-gate', area: 'backend' }, { route: null, route_gap: 'review_gate', tokens: tokens(null, null) })
  set('PHAROS-15', {}, { route: null, tokens: tokens(820_000, null), cost: cost(['2.100000', null], ['2.100000', null]) })
  return data
}
function chooseAll(data: Fixtures) {
  data.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'updated', ...PLANNING] }
}

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

test('planning columns show the route, spent / estimated, list price and paid, with reasons in tooltips', async ({ page }) => {
  const data = world()
  chooseAll(data)
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  for (const name of ['Model', 'Tokens', '≈ Cost', 'Paid']) await expect(page.getByRole('columnheader', { name })).toBeVisible()

  const built = row(page, 'PHAROS-11')
  await expect(built.locator('.plan-model')).toHaveText('Codex astra · xhigh')
  await expect(built.locator('.plan-model')).toHaveAttribute('data-tip', 'Build hard · backend\nModel registry, revision 3f9a1c2b')
  await expect(built.locator('.c-tokens .plan-figure')).toHaveText(/^1\.54M\s*\/\s*10M$/)
  await expect(built.locator('.c-tokens .plan-figure')).toHaveAttribute('data-tip', /2h at 5M\/h: default 5M\/h until 5 finished tickets on Codex astra · xhigh/)
  await expect(built.locator('.c-list-cost .plan-figure')).toHaveText(/^≈\s*\$4\.48\s*\/\s*\$27$/)
  await expect(built.locator('.c-list-cost .plan-figure')).toHaveAttribute('aria-label', 'approximately $4.48 spent $27 estimated')
  await expect(built.locator('.c-paid .plan-figure')).toHaveText(/^\$3\.80\s*\/\s*\$27$/)

  // Overrun: tinted spent figure, said in words too.
  const over = row(page, 'PHAROS-12')
  await expect(over.locator('.c-tokens .plan-figure')).toHaveClass(/over/)
  await expect(over.locator('.c-tokens .plan-figure')).toHaveAttribute('aria-label', /over the estimate/)
  const tint = await over.locator('.c-tokens .spent').evaluate(el => getComputedStyle(el).color)
  const plain = await built.locator('.c-tokens .spent').evaluate(el => getComputedStyle(el).color)
  expect(tint).not.toBe(plain)
  // No coloured edge: the cell keeps its plain borders.
  const edge = await over.locator('td.c-tokens').evaluate(el => { const s = getComputedStyle(el); return [s.borderLeftWidth, s.borderTopWidth] })
  expect(edge).toEqual(['0px', '0px'])
  await expect(over.locator('.c-paid .plan-figure')).toHaveText(/^\$0\s*\/\s*\$0$/)
  await expect(over.locator('.c-paid .plan-figure')).toHaveAttribute('data-tip', /Max 20x/)

  // Unresolved: a dash that explains itself.
  await expect(row(page, 'PHAROS-14').locator('.plan-model')).toHaveText('—')
  await expect(row(page, 'PHAROS-14').locator('.plan-model')).toHaveAttribute('data-tip', /family other than the author's/)
  await expect(row(page, 'PHAROS-13').locator('.plan-model')).toHaveAttribute('data-tip', /no available route/)
  // An estimate alone reads as one.
  await expect(row(page, 'PHAROS-13').locator('.c-tokens .plan-figure')).toHaveText('~5M')
  await expect(row(page, 'PHAROS-13').locator('.c-tokens .plan-figure')).toHaveAttribute('data-tip', /on any route/)
  // Epics roll up their children.
  await expect(row(page, 'PHAROS-10').locator('.c-tokens .plan-figure')).toHaveAttribute('data-tip', /Sum of 2 of 2 open and done children/)
  await expect(row(page, 'PHAROS-10').locator('.plan-model')).toHaveText('—')

  // Sortable: the header asks the server for that key.
  const sorts: string[] = []
  const asked = (key: string) => sorts.some(sort => sort.split(',')[0] === key)
  page.on('request', request => {
    const url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname === '/api/nodes' && url.searchParams.get('sort')) sorts.push(url.searchParams.get('sort')!)
  })
  await page.getByRole('columnheader', { name: 'Tokens' }).getByRole('button', { name: 'Tokens' }).click()
  await expect.poll(() => asked('tokens')).toBe(true)
  await page.getByRole('columnheader', { name: 'Tokens' }).getByRole('button', { name: 'Tokens' }).click()
  await expect.poll(() => asked('-tokens')).toBe(true)
  await expect.poll(async () => (await keys(page)).slice(0, 3)).toEqual(['PHAROS-10', 'PHAROS-12', 'PHAROS-11'])
  await page.getByRole('columnheader', { name: 'Model' }).getByRole('button', { name: 'Model' }).click()
  await expect.poll(() => asked('model')).toBe(true)
  await expect.poll(async () => (await keys(page)).slice(0, 4)).toEqual(['PHAROS-13', 'PHAROS-12', 'PHAROS-11', 'PHAROS-14'])
  await page.getByRole('columnheader', { name: '≈ Cost' }).getByRole('button', { name: '≈ Cost' }).click()
  await expect.poll(() => asked('list_cost')).toBe(true)
  await page.getByRole('columnheader', { name: 'Paid' }).getByRole('button', { name: 'Paid' }).click()
  await expect.poll(() => asked('paid')).toBe(true)

  // The column chooser lists them.
  await page.getByRole('button', { name: 'Display: Display' }).click()
  const names = await page.getByRole('dialog', { name: 'Display options' }).locator('.columns .name').allTextContents()
  for (const name of ['Model', 'Tokens', '≈ Cost', 'Paid']) expect(names).toContain(name)
})

test('cost stays with people who may see usage; tokens and model do not need it', async ({ page }) => {
  const data = world()
  chooseAll(data)
  await mockWork(page, data, { readOnly: true })
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  await expect(page.getByRole('columnheader', { name: 'Tokens' })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: 'Model' })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: '≈ Cost' })).toHaveCount(0)
  await expect(page.getByRole('columnheader', { name: 'Paid' })).toHaveCount(0)
  await expect(row(page, 'PHAROS-11').locator('.c-tokens .plan-figure')).toHaveText(/^1\.54M/)
  await page.getByRole('button', { name: 'Display: Display' }).click()
  const names = await page.getByRole('dialog', { name: 'Display options' }).locator('.columns .name').allTextContents()
  expect(names).toContain('Tokens')
  expect(names).not.toContain('≈ Cost')
  expect(names).not.toContain('Paid')
})

test('empty planning columns stay hidden, chosen or automatic', async ({ page }) => {
  const data = fixtures()
  chooseAll(data)
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  await expect(page.getByRole('columnheader', { name: 'Status' })).toBeVisible()
  for (const name of ['Model', 'Tokens', '≈ Cost', 'Paid']) await expect(page.getByRole('columnheader', { name })).toHaveCount(0)
  // A very wide screen adds filled planning columns on its own.
  const filled = world()
  await mockWork(page, filled)
  await page.setViewportSize({ width: 2600, height: 900 })
  await page.goto('/p/PHAROS?sort=key')
  await expect(page.getByRole('columnheader', { name: 'Model' })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: 'Tokens' })).toBeVisible()
})

for (const width of [1600, 390] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`planning columns at ${width}px ${theme}`, async ({ page }) => {
      const data = world()
      chooseAll(data)
      data.preferences.theme = { choice: theme }
      await page.addInitScript(value => { document.documentElement.dataset.theme = value }, theme)
      await mockWork(page, data)
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await page.goto('/p/PHAROS?sort=key')
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      await expect(row(page, 'PHAROS-11')).toBeVisible()
      if (width === 1600) {
        await expect(row(page, 'PHAROS-11').locator('.plan-model')).toHaveText('Codex astra · xhigh')
        await row(page, 'PHAROS-12').locator('.c-tokens .plan-figure').hover()
        await page.waitForTimeout(700)
        mkdirSync(shots, { recursive: true })
        await page.screenshot({ path: join(shots, `planning-columns__${width}__${theme}__tip.png`) })
        await page.mouse.move(0, 0)
        await expect(row(page, 'PHAROS-12').locator('.c-tokens .plan-figure')).toHaveClass(/over/)
        // Figures fit their default widths without clipping.
        for (const cls of ['c-tokens', 'c-list-cost', 'c-paid', 'c-model']) {
          const clipped = await row(page, 'PHAROS-11').locator(`td.${cls} .cell > *`).first().evaluate(el => el.scrollWidth > el.clientWidth + 1)
          expect(clipped, cls).toBe(false)
        }
      } else {
        // The phone card keeps its own calm set: planning stays in the ticket.
        await expect(page.locator('td.c-tokens, td.c-model, td.c-list-cost, td.c-paid')).toHaveCount(0)
      }
      const fits = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)
      expect(fits).toBe(true)
      mkdirSync(shots, { recursive: true })
      await page.screenshot({ path: join(shots, `planning-columns__${width}__${theme}.png`) })
    })
  }
}
