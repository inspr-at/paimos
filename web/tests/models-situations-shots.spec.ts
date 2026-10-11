// SPDX-License-Identifier: AGPL-3.0-only
// Evidence for AEON-1162 (Models v2 W3): Quick jobs with its Why?, the situation fold For everyone and on Just me, and
// a cell popover, at 1440, 1024 and 390 px, light and dark. Screenshots go to Playwright's output directory only.
import { expect, test, type Page } from '@playwright/test'
import { mockModels } from './models-simple-fixtures'
import { openModelsSettings } from './models-settings-page'

async function show(page: Page, theme: string, scope: 'For everyone' | 'Just me') {
  await mockModels(page, { situations: true, longLabels: true }); await openModelsSettings(page)
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
  await page.getByRole('button', { name: scope }).click(); await page.locator('#sx-fold').click()
  await expect(page.locator('#sx-fold')).toHaveAttribute('aria-expanded', 'true')
}
for (const size of [{ width: 1440, height: 1100 }, { width: 1024, height: 1000 }, { width: 390, height: 860 }]) test.describe(`at ${size.width}`, () => {
  test.use({ viewport: size, ...(size.width < 600 ? { hasTouch: true, isMobile: true } : {}) })
  for (const theme of ['light', 'dark']) {
    test(`situations for everyone ${theme}`, async ({ page }, testInfo) => {
      await show(page, theme, 'For everyone')
      await page.screenshot({ path: testInfo.outputPath(`situations-${size.width}-${theme}-everyone.png`), fullPage: true })
      await page.locator('[data-sit="other:stuck"]').scrollIntoViewIfNeeded(); await page.locator('[data-sit="other:stuck"]').click()
      await expect(page.getByRole('dialog', { name: 'Stuck · Default' })).toBeVisible()
      await page.screenshot({ path: testInfo.outputPath(`situations-${size.width}-${theme}-cell.png`) })
      await page.keyboard.press('Escape')
      await page.locator('[data-quick-why]').scrollIntoViewIfNeeded(); await page.locator('[data-quick-why]').click()
      await expect(page.getByRole('dialog', { name: 'Quick jobs' })).toBeVisible()
      await page.screenshot({ path: testInfo.outputPath(`situations-${size.width}-${theme}-quick-why.png`) })
    })
    test(`situations on just me ${theme}`, async ({ page }, testInfo) => {
      await show(page, theme, 'Just me')
      await expect(page.locator('.sx-lead')).toContainText('Change these under For everyone.')
      await page.screenshot({ path: testInfo.outputPath(`situations-${size.width}-${theme}-justme.png`), fullPage: true })
    })
  }
})
