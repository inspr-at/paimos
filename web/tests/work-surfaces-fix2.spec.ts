// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockBusiness, businessData } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`incremental Outline and leaf progress ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const data = fixtures()
    data.preferences.theme = { choice: theme }
    const template = data.nodes.find(n => n.id === 'n-epic')!
    data.nodes = [
      { ...template, id: 'group', key: 'PHAROS-1', parent_id: 'p-pharos', title: 'Vorhaben mit ausführlicher Beschreibung für verschachtelte Arbeit', kind_slug: 'work', is_leaf: false, work_children_count: 2 },
      { ...template, id: 'nested', key: 'PHAROS-2', parent_id: 'group', title: 'Abgeschlossene Gruppe', kind_slug: 'work', is_leaf: false, state: 'done', work_children_count: 10 },
      { ...template, id: 'open', key: 'PHAROS-3', parent_id: 'group', title: 'Offener Arbeitsschritt', kind_slug: 'work', is_leaf: true },
      ...Array.from({ length: 10 }, (_, i) => ({ ...template, id: `done-${i}`, key: `PHAROS-${i + 4}`, parent_id: 'nested', title: `Abgeschlossener Schritt ${i + 1}`, kind_slug: 'work', is_leaf: true, state: 'done' })),
      { ...template, id: 'root-leaf', key: 'PHAROS-30', parent_id: 'p-pharos', title: 'Weiterer Arbeitsschritt', kind_slug: 'work', is_leaf: true },
    ]
    // Keep stream reconnects out of this pagination scenario: they legitimately
    // re-read the first page. Only the explicit paging action should advance it.
    await page.addInitScript(() => {
      window.EventSource = class {
        static CONNECTING = 0; static OPEN = 1; static CLOSED = 2
        readyState = 1
        addEventListener() {}
        close() { this.readyState = 2 }
      } as unknown as typeof EventSource
    })
    const calls = await mockWork(page, data, { admin: true, listPageSize: 1 })
    await page.goto('/p/PHAROS/tickets?view=outline&closed=1&sort=key')
    const tree = page.getByRole('treegrid', { name: 'Ticket outline' })
    await expect(tree.locator('.key').filter({ hasText: /^PHAROS-1$/ })).toBeVisible()
    await expect(tree.locator('.key').filter({ hasText: /^PHAROS-30$/ })).toHaveCount(0)
    const rootReads = () => calls.filter(c => c.method === 'GET' && c.path === '/api/nodes' && c.query.get('parent_id') === 'p-pharos')
    expect(rootReads()).toHaveLength(1)
    const create = page.getByRole('button', { name: 'New work item', exact: true })
    const stable = await controlStability(page, { create })
    await stable.check(() => tree.locator('.more-row button').click())
    await expect(tree.locator('.key').filter({ hasText: /^PHAROS-30$/ })).toBeVisible()
    expect(rootReads()).toHaveLength(2)
    await expect(tree.getByText('No epic', { exact: true })).toHaveCount(0)
    stable.done()
    await page.screenshot({ path: `test-results/aeon-655-wn-fix2/outline-${width}-${theme}.png`, fullPage: true })
    let releaseProgress!: () => void
    const heldProgress = new Promise<void>(resolve => { releaseProgress = resolve })
    await page.route('**/api/nodes?*', async route => {
      const query = new URL(route.request().url()).searchParams
      if (query.get('within') !== 'group' || query.get('facets') !== 'state' || query.get('shape') !== 'leaf') return route.fallback()
      await heldProgress
      await route.fulfill({ json: { items: [], next_cursor: null, facets: { state: { done: 10, open: 1 } } } })
    })
    await page.goto('/p/PHAROS/PHAROS-1?view=outline&closed=1')
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    const children = panel.getByRole('region', { name: 'Child work items' })
    await expect(children.getByRole('status')).toContainText('Loading leaf progress')
    const add = children.getByRole('button', { name: 'Add work item', exact: true })
    const childStable = await controlStability(page, { add })
    await childStable.check(async () => { releaseProgress(); await expect(children.locator('.pct')).toHaveText('10/11'); await expect(children.locator('.progress')).toHaveAttribute('data-tip', '10 of 11 leaves done') }); childStable.done()
    await page.screenshot({ path: `test-results/aeon-655-wn-fix2/progress-${width}-${theme}.png`, fullPage: true })
  })
  test(`vocabulary pending Reload ${width} ${theme}`, async ({ page }) => {
    const data = fixtures(); data.preferences.theme = { choice: theme }
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, data, { admin: true })
    await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
    await mockSettings(page, settingsData())
    await page.clock.setSystemTime(new Date('2026-10-04T10:00:00Z'))
    let release!: () => void, started!: () => void
    const held = new Promise<void>(r => { release = r }), readStarted = new Promise<void>(r => { started = r })
    let reads = 0, puts = 0, revision = 1
    await page.route('**/api/settings/work-vocabulary', async route => {
      if (route.request().method() === 'PUT') { puts++; revision++ }
      else if (++reads === 2) { started(); await held }
      await route.fulfill({ json: { revision, leaf: { name: `Arbeitsschritt ${revision}`, icon: '' }, levels: [] } })
    })
    await page.goto('/settings/workspace')
    const card = page.locator('#work-vocabulary'), reload = card.getByRole('button', { name: 'Reload', exact: true }), save = card.getByRole('button', { name: /Save names/ })
    await expect(card.getByLabel('Leaf name')).toHaveValue('Arbeitsschritt 1')
    await card.scrollIntoViewIfNeeded()
    const stable = await controlStability(page, { reload, save, actions: card.getByLabel('Vocabulary actions') })
    await stable.check(async () => { await reload.click(); await readStarted; await expect(reload).toBeDisabled(); await expect(save).toBeDisabled() })
    expect(puts).toBe(0)
    await stable.check(async () => { release(); await expect(save).toBeEnabled(); await save.click(); await expect(card.getByRole('status')).toContainText('Workspace names saved.') })
    stable.done()
    await expect(card.getByLabel('Leaf name')).toHaveValue('Arbeitsschritt 2')
    await page.screenshot({ path: `test-results/aeon-655-wn-fix2/vocabulary-${width}-${theme}.png`, fullPage: true })
  })
}
