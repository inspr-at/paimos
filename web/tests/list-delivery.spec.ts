// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type MockNode } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import type { DeliveryOrder, ListItem } from '../src/lib/api'

const releaseA = '11111111-1111-4111-8111-111111111111', releaseB = '22222222-2222-4222-8222-222222222222'
const titles = { [releaseA]: 'Release 122', [releaseB]: 'Audit sweep' }
const order = (release_id: string | null, release_rank: string | null, rank: string | null, expedite = false): DeliveryOrder => ({ release_id, release_rank, rank, expedite })
const positions: Record<string, DeliveryOrder> = {
  'n-1': order(releaseA, 'B', 'Z'), 'n-2': order(releaseA, 'B', 'a'), 'n-3': order(releaseB, 'D', 'V', true),
  'n-4': order(null, null, 'V'), 'n-5': order(releaseB, 'D', 'B'), 'n-6': order(null, null, null), 'n-epic': order(null, null, null),
}
const serverOrder = ['n-3', 'n-1', 'n-2', 'n-5', 'n-4', 'n-6', 'n-epic']
const rows = (page: Page) => page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row:not(.ghost)')
const keys = (page: Page) => rows(page).locator('.key')
const shot = (name: string) => { const dir = resolve('test-results/aeon-596-int'); mkdirSync(dir, { recursive: true }); return resolve(dir, name) }
const matches = (values: string[], actual: string) => {
  const positive = values.filter(value => !value.startsWith('!'))
  return (!positive.length || positive.includes(actual)) && !values.includes(`!${actual}`)
}
async function world(page: Page, state = { failFacet: false }, theme: "light" | "dark" = "light") {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  for (const node of data.nodes) if (['n-1', 'n-2'].includes(node.id)) node.title = 'Freigabeplanung mit nachvollziehbaren Prüfungen und ausführlichem deutschem Projekttitel'
  await page.addInitScript(value => { document.addEventListener('DOMContentLoaded', () => { document.documentElement.dataset.theme = value }, { once: true }) }, theme)
  await mockWork(page, data)
  function item(node: MockNode): ListItem {
    return { ...node, kind_id: `k-${node.kind_slug}`, position: '0', deleted_at: null, kind_label: node.kind_slug,
      children_count: 0, assignee: null, priority: typeof node.fields.priority === 'string' ? node.fields.priority : null,
      parent: null, epic: null, project: { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }, delivery_order: positions[node.id] }
  }
  const queries: URLSearchParams[] = []
  await page.route('**/api/nodes?**', async route => {
    const query = new URL(route.request().url()).searchParams
    if (route.request().method() !== 'GET' || query.get('within') !== 'p-pharos') return route.fallback()
    queries.push(query)
    if (state.failFacet && query.get('facets') === 'ships_in') return route.fulfill({ status: 503, json: { error: 'Release count read failed' } })
    const values = (name: string) => (query.get(name) ?? '').split(',').filter(Boolean)
    let selected = data.nodes.filter(node => node.project === 'p-pharos')
      .filter(node => matches(values('kind'), node.kind_slug)).filter(node => matches(values('ids'), node.id))
      .filter(node => matches(values('ships_in'), positions[node.id]?.release_id ?? 'none'))
      .filter(node => query.get('hide_closed') !== 'true' || !['done', 'cancelled'].includes(node.state))
    // Independent response fixture; real SQL order and filtering have Go tests.
    selected = selected.toSorted((a, b) => (serverOrder.indexOf(a.id) - serverOrder.indexOf(b.id)) * (query.get('sort')?.startsWith('-order') ? -1 : 1))
    const facets: Record<string, Record<string, number>> = {}
    for (const facet of values('facets')) {
      facets[facet] = {}
      for (const node of selected) {
        const value = facet === 'ships_in' ? positions[node.id]?.release_id ?? 'none' : facet === 'kind' ? node.kind_slug : facet === 'state' ? node.state : facet === 'priority' ? String(node.fields.priority ?? 'none') : 'none'
        facets[facet][value] = (facets[facet][value] ?? 0) + 1
      }
    }
    const labels = Object.fromEntries(Object.entries(titles).filter(([id]) => facets.ships_in?.[id]))
    return route.fulfill({ json: { items: selected.slice(0, Number(query.get('limit') ?? 200)).map(item), next_cursor: null, facets,
      ...(facets.ships_in ? { facet_labels: { ships_in: labels } } : {}) } })
  })
  return queries
}
const start = '/p/PHAROS?view=list&type=!epic&closed=1&sort=order'
for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`${width} ${theme}: scope bookmarks, removed facet and stable ordinary controls`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const errors = watchErrors(page), queries = await world(page, { failFacet: false }, theme)
    await page.goto(`${start}&ships_in=${releaseA}`)
    await expect(keys(page)).toHaveText(['PHAROS-11', 'PHAROS-12'])
    await page.reload()
    await expect(keys(page)).toHaveText(['PHAROS-11', 'PHAROS-12'])
    expect(queries.filter(query => query.get('limit') === '200').at(-1)?.get('ships_in')).toBe(releaseA)
    if (width === 390) {
      await page.getByRole('button', { name: 'Filters', exact: true }).click()
      const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
      await expect(sheet.getByText('In release', { exact: true })).toHaveCount(0)
      const option = sheet.getByRole('checkbox', { name: /^High /  })
      await option.scrollIntoViewIfNeeded()
      const row = option.locator('xpath=ancestor::div[contains(@class,"facet-option")][1]')
      await expectStableControls({ controls: { frame: sheet, action: sheet.locator('footer button'), selector: option, row }, scrollAreas: { body: sheet.locator('.sheet-scroll') }, interactions: [
        { name: 'ordinary priority', run: async () => { await option.check() } },
        { name: 'clear priority', run: async () => { await option.uncheck() } },
      ] })
      await sheet.locator('footer button').click()
    } else {
      await page.getByRole('button', { name: 'Filter by more' }).click()
      await expect(page.getByRole('menuitem', { name: 'In release', exact: true })).toHaveCount(0)
      await expect(page.getByRole('menuitem', { name: 'Imported release', exact: true })).toBeVisible()
      await page.keyboard.press('Escape')
      const display = page.getByRole('button', { name: /^Display/ })
      await display.click()
      const dialog = page.getByRole('dialog', { name: 'Display options' }), selector = dialog.getByLabel('Sort key 1')
      await expectStableControls({ controls: { action: display, selector }, interactions: [
        { name: 'ordinary sort', run: async () => { await selector.selectOption('priority') } },
        { name: 'release order', run: async () => { await selector.selectOption('order') } },
      ] })
      await page.keyboard.press('Escape')
    }
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await page.screenshot({ path: shot(`release-scope-${width}-${theme}.png`), fullPage: true })
    await page.goto(`${start}&ships_in=none`)
    await expect(keys(page)).toHaveText(['PHAROS-14', 'PHAROS-16'])
    await page.screenshot({ path: shot(`backlog-scope-${width}-${theme}.png`), fullPage: true })
    expect(errors).toEqual([])
  })
}
test('ambiguous saved scope gives visible repair and no hidden list request', async ({ page }) => {
  const queries = await world(page)
  await page.goto(`${start}&ships_in=${releaseA},!none`)
  await expect(page.getByText('Choose a release scope', { exact: true })).toBeVisible()
  expect(queries.filter(query => query.get('limit') === '200')).toHaveLength(0)
})
