// SPDX-License-Identifier: AGPL-3.0-only
// Evidence for AEON-1011: the card at desktop and phone width, light and dark, with the picker, the lock switch, a
// pick that cannot run and a new model open. Screenshots go to Playwright's output directory only.
import { expect, test, type Page } from '@playwright/test'
import { mockModels, type MockOptions } from './models-simple-fixtures'
import { openModelsSettings } from './models-settings-page'

const sizes = [{ width: 1440, height: 1100 }, { width: 1024, height: 1000 }, { width: 400, height: 900 }]
async function show(page: Page, options: MockOptions, theme: string, size: { width: number; height: number }, query = '') {
  await page.setViewportSize(size)
  await mockModels(page, options); await openModelsSettings(page, query)
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
}
for (const size of sizes) for (const theme of ['light', 'dark']) {
  test(`card at ${size.width} ${theme}`, async ({ page }, testInfo) => {
    await show(page, { down: true, fresh: true, longLabels: true }, theme, size)
    await expect(page.locator('[data-unavailable]').first()).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`models-${size.width}-${theme}.png`), fullPage: true })
    await page.locator('[data-pick="all"]').click()
    await expect(page.locator('.mdl-pop')).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`models-${size.width}-${theme}-picker.png`) })
  })
  test(`for everyone with the lock at ${size.width} ${theme}`, async ({ page }, testInfo) => {
    await show(page, {}, theme, size)
    await page.getByRole('button', { name: 'For everyone' }).click()
    await expect(page.locator('[data-row="design"] [data-lock]')).toBeVisible()
    await page.locator('[data-pick="design"]').click()
    await expect(page.locator('.mdl-pop .pm-lock')).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`models-${size.width}-${theme}-everyone-lock.png`) })
  })
  test(`why at ${size.width} ${theme}`, async ({ page }, testInfo) => {
    await show(page, { down: true }, theme, size)
    await page.locator('[data-why]').click()
    await expect(page.getByRole('dialog', { name: /^Why /i })).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`models-${size.width}-${theme}-why.png`) })
  })
}
