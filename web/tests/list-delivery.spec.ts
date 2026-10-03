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
const shot = (name: string) => { const dir = resolve('test-results/aeon-596-p5'); mkdirSync(dir, { recursive: true }); return resolve(dir, name) }
const matches = (values: string[], actual: string) => {
  const positive = values.filter(value => !value.startsWith('!'))
  return (!positive.length || positive.includes(actual)) && !values.includes(`!${actual}`)
}
async function world(page: Page, state = { failFacet: false }) {
  const data = fixtures()
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
async function desktopFacet(page: Page) {
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await page.getByRole('menuitem', { name: 'In release', exact: true }).click()
  const menu = page.getByRole('dialog', { name: 'Filter by In release' })
  await expect(menu.getByRole('checkbox', { name: /Release 122/ })).toBeVisible()
  return menu
}
const start = '/p/PHAROS?view=list&type=!epic&closed=1&sort=order'

test('1440: UUID filter, exclusions, reload and stationary facet controls', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const errors = watchErrors(page), queries = await world(page)
  await page.goto(start)
  await expect(keys(page)).toHaveText(['PHAROS-13', 'PHAROS-11', 'PHAROS-12', 'PHAROS-15', 'PHAROS-14', 'PHAROS-16'])
  const menu = await desktopFacet(page), release = menu.locator('.facet-option').filter({ hasText: 'Release 122' })
  await expectStableControls({ controls: { options: menu.locator('.facet-options'), row: release, selector: release.getByRole('checkbox'), exclude: release.getByRole('button', { name: 'Exclude Release 122' }) }, interactions: [
    { name: 'include release', run: async () => { await release.getByRole('checkbox').check(); await expect(keys(page)).toHaveText(['PHAROS-11', 'PHAROS-12']); await expect.poll(() => queries.filter(query => query.get('limit') === '200').at(-1)?.get('ships_in')).toBe(releaseA) } },
    { name: 'exclude release', run: async () => { await release.getByRole('button', { name: 'Exclude Release 122' }).click(); await expect(keys(page)).toHaveText(['PHAROS-13', 'PHAROS-15', 'PHAROS-14', 'PHAROS-16']) } },
    { name: 'clear exclusion', run: async () => { await release.getByRole('button', { name: 'Exclude Release 122' }).click(); await expect(rows(page)).toHaveCount(6) } },
  ] })
  await release.getByRole('checkbox').check()
  await expect(page).toHaveURL(new RegExp(`ships_in=${releaseA}`))
  await page.screenshot({ path: shot('list-facet-1440.png'), fullPage: true })
  await page.keyboard.press('Escape')
  await expect(page.getByLabel('Applied filters')).toContainText('In releaseRelease 122')
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await expect(page.getByRole('menuitem', { name: 'Imported release', exact: true })).toBeVisible()
  await page.keyboard.press('Escape')
  await page.reload()
  await expect(keys(page)).toHaveText(['PHAROS-11', 'PHAROS-12'])
  await expect(page.getByLabel('Applied filters')).toContainText('Release 122')
  expect(errors).toEqual([])
})
test('390: full-height sheet, pinned action and stable option rows', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const errors = watchErrors(page)
  await world(page); await page.goto(start); await expect(rows(page)).toHaveCount(6)
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  const section = sheet.locator('section').filter({ has: page.getByText('In release', { exact: true }) })
  const release = section.locator('.facet-option').filter({ hasText: 'Release 122' })
  await release.scrollIntoViewIfNeeded(); await expect(release.getByRole('checkbox')).toBeVisible()
  await expectStableControls({ controls: { frame: sheet, action: sheet.locator('footer button'), options: section.locator('.facet-options'), row: release, selector: release.getByRole('checkbox') }, scrollAreas: { body: sheet.locator('.sheet-scroll') }, interactions: [
    { name: 'choose release', run: async () => { await release.getByRole('checkbox').check(); await expect(sheet.locator('footer button')).toHaveText('Show 2 tickets') } },
    { name: 'clear release', run: async () => { await release.getByRole('checkbox').uncheck(); await expect(sheet.locator('footer button')).toHaveText('Show 6 tickets') } },
  ] })
  await release.getByRole('checkbox').check(); await expect(sheet.locator('footer button')).toHaveText('Show 2 tickets')
  await page.screenshot({ path: shot('list-facet-390.png') })
  await sheet.locator('footer button').click(); await expect(keys(page)).toHaveText(['PHAROS-11', 'PHAROS-12'])
  await page.screenshot({ path: shot('list-filtered-390.png') }); expect(errors).toEqual([])
})
test('failed facet read is visible and reopening retries it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const state = { failFacet: true }; await world(page, state); await page.goto(start)
  await expect(rows(page)).toHaveCount(6)
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await page.getByRole('menuitem', { name: 'In release', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('Release counts could not be loaded')
  await page.keyboard.press('Escape'); state.failFacet = false
  await desktopFacet(page); await expect(page.getByRole('alert')).toHaveCount(0)
})
test('Display offers Release order and sends the complete key', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const queries = await world(page); await page.goto(start.replace('sort=order', 'sort=priority'))
  await expect(rows(page)).toHaveCount(6); await page.getByRole('button', { name: /^Display/ }).click()
  await page.getByRole('dialog', { name: 'Display options' }).getByLabel('Sort key 1').selectOption('order')
  await expect.poll(() => queries.filter(query => query.get('limit') === '200').at(-1)?.get('sort')).toBe('order')
  await expect(keys(page)).toHaveText(['PHAROS-13', 'PHAROS-11', 'PHAROS-12', 'PHAROS-15', 'PHAROS-14', 'PHAROS-16'])
})
