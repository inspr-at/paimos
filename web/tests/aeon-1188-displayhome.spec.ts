// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1188: Display settings have one home, the Display menu; the Filters sheet
// keeps filtering and links to it. Risks: an entry (toolbar, collapsed header,
// phone) no longer reaches the menu; the link opens a different menu or strands
// focus; filtering in the sheet changes or moves its controls.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const errors = new WeakMap<Page, string[]>()
test.beforeEach(async ({ page }) => {
  const failures: string[] = []; errors.set(page, failures)
  page.on('pageerror', error => failures.push(error.message))
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
})
test.afterEach(async ({ page }) => { expect(errors.get(page), 'no browser runtime errors').toEqual([]) })

const toolbar = (page: Page) => page.getByRole('toolbar', { name: 'Ticket list controls' })
const filters = (page: Page) => page.getByRole('button', { name: 'Filters', exact: true })
const sheet = (page: Page) => page.getByRole('dialog', { name: 'Filters', exact: true })
const link = (page: Page) => sheet(page).getByRole('button', { name: 'Display settings…', exact: true })
const menu = (page: Page) => page.getByRole('dialog', { name: 'Display options', exact: true })
const projectPage = (page: Page) => page.locator('.project-page')
const SETTINGS = ['Group by', 'Row height', 'Project header density', 'Effort meter']
async function settled(page: Page) { await page.evaluate(async () => { await document.fonts.ready; await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))) }) }
async function capture(page: Page, name: string) { await settled(page); await page.screenshot({ path: test.info().outputPath(`${name}.png`) }) }
async function open(page: Page, width: number, theme = 'light') {
  await page.setViewportSize({ width, height: 900 })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { density: 'comfortable', headerGraph: false }
  await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(projectPage(page)).toHaveClass(/header-compact/)
  await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  await settled(page)
  return data
}

for (const width of [390, 768]) {
  for (const theme of ['light', 'dark'] as const) {
    test(`the Filters sheet filters and links to the Display menu at ${width}px ${theme}`, async ({ page }) => {
      const data = await open(page, width, theme)
      await filters(page).click()
      await expect(sheet(page)).toBeVisible()
      for (const name of SETTINGS) await expect(sheet(page).getByRole('radiogroup', { name, exact: true })).toHaveCount(0)
      await expect(sheet(page).getByRole('list', { name: 'Columns', exact: true })).toHaveCount(0)
      const backlog = sheet(page).locator('.facet-option').filter({ has: page.getByRole('checkbox', { name: /^Backlog/ }) })
      const today = sheet(page).getByRole('group', { name: 'Period' }).getByRole('button', { name: 'Today', exact: true })
      const done = sheet(page).locator('footer button')
      await expect(backlog).toBeVisible()
      // Filtering works as before and moves neither the link nor the sheet's other controls.
      await expectStableControls({ controls: { link: link(page), backlog, today, done }, scrollAreas: { body: sheet(page).locator('.sheet-scroll') }, interactions: [
        { name: 'filter by Backlog', run: async () => { await backlog.click(); await expect(backlog.getByRole('checkbox')).toBeChecked() } },
        { name: 'clear Backlog', run: async () => { await backlog.click(); await expect(backlog.getByRole('checkbox')).not.toBeChecked() } },
        { name: 'updated today', run: async () => { await today.click(); await expect(today).toHaveAttribute('aria-pressed', 'true') } },
        { name: 'any time', run: async () => { await today.click(); await expect(today).toHaveAttribute('aria-pressed', 'false') } },
      ] })
      await capture(page, `filters-${width}-${theme}`)

      await link(page).click()
      await expect(sheet(page)).toBeHidden()
      await expect(menu(page).locator('.display-menu.as-sheet')).toBeVisible()
      await expect(menu(page).getByRole('heading', { name: 'Display', exact: true })).toBeVisible()
      // The unfolded header keeps its sections, saved views and Hide: the menu holds display settings only.
      await expect(menu(page).getByRole('navigation')).toHaveCount(0)
      await expect(menu(page).getByRole('checkbox', { name: /^Hide / })).toHaveCount(0)
      for (const name of SETTINGS) await expect(menu(page).getByRole('radiogroup', { name, exact: true })).toBeVisible()
      await expect(menu(page).getByRole('list', { name: 'Columns', exact: true })).toBeVisible()
      if (width === 390) {
        const frame = (await menu(page).boundingBox())!
        expect([frame.x, frame.y, frame.width, frame.height], 'a full-height phone sheet').toEqual([0, 0, 390, 900])
      }
      await capture(page, `display-${width}-${theme}`)
      await menu(page).getByRole('radiogroup', { name: 'Row height', exact: true }).getByRole('radio', { name: 'Compact', exact: true }).click()
      await expect.poll(() => data.preferences['list:display']?.density).toBe('compact')
      await page.keyboard.press('Escape')
      await expect(menu(page)).toBeHidden()
      await expect(filters(page)).toBeFocused()
      await expect(page.locator('.table-card')).toHaveClass(/compact/)
      // Done closes it the same way.
      await filters(page).click()
      await link(page).click()
      await menu(page).getByRole('button', { name: /^Done/ }).click()
      await expect(menu(page)).toBeHidden()
      await expect(filters(page)).toBeFocused()
    })
  }
}

test('with the header folded on a phone, the link opens the Display menu beside New', async ({ page }) => {
  await open(page, 390)
  await page.locator('.phone-header-fold').click()
  await expect(projectPage(page)).toHaveClass(/header-collapsed/)
  await filters(page).click()
  await link(page).click()
  await expect(sheet(page)).toBeHidden()
  await expect(menu(page).locator('.display-menu.as-sheet')).toBeVisible()
  // The collapsed header's menu: what the header hides, above the same display settings.
  await expect(menu(page).getByRole('button', { name: 'Knowledge' })).toBeVisible()
  await expect(menu(page).locator('.menu-hide')).toBeVisible()
  for (const name of SETTINGS) await expect(menu(page).getByRole('radiogroup', { name, exact: true })).toBeVisible()
  await menu(page).getByRole('button', { name: /^Done/ }).click()
  await expect(menu(page)).toBeHidden()
  await expect(toolbar(page).getByRole('button', { name: /^Display:/ })).toBeFocused()
})

for (const theme of ['light', 'dark'] as const) {
  test(`desktop Display buttons open the Display menu at 1440px ${theme}`, async ({ page }) => {
    await open(page, 1440, theme)
    await expect(filters(page)).toBeHidden()
    const inBar = page.locator('.project-navigation').getByRole('button', { name: /^Display:/ })
    await inBar.click()
    for (const name of SETTINGS) await expect(menu(page).getByRole('radiogroup', { name, exact: true })).toBeVisible()
    await expect(menu(page).locator('.display-menu')).toHaveCount(0)
    await capture(page, `display-1440-${theme}`)
    await page.keyboard.press('Escape')
    await expect(inBar).toBeFocused()
    await page.getByRole('radio', { name: 'Collapsed project header', exact: true }).click()
    await expect(projectPage(page)).toHaveClass(/header-collapsed/)
    const beside = toolbar(page).getByRole('button', { name: /^Display:/ })
    await beside.click()
    await expect(menu(page).locator('.display-menu:not(.as-sheet)')).toBeVisible()
    for (const name of SETTINGS) await expect(menu(page).getByRole('radiogroup', { name, exact: true })).toBeVisible()
    await capture(page, `display-collapsed-1440-${theme}`)
    await page.keyboard.press('Escape')
    await expect(beside).toBeFocused()
  })
}
