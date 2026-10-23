// SPDX-License-Identifier: AGPL-3.0-only
// Evidence for AEON-1011 and Models v2 W1 (AEON-1160): the card at desktop and phone width, light and dark, with the
// picker, the lock switch, a pick that cannot run, a new model, and many models with older versions open.
// Screenshots go to Playwright's output directory only.
import { expect, test, type Page } from '@playwright/test'
import { mockModels, type MockOptions } from './models-simple-fixtures'
import { openModelsSettings } from './models-settings-page'
import { fixtures, mockWork } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const sizes = [{ width: 1440, height: 1100 }, { width: 1024, height: 1000 }, { width: 390, height: 900 }]
async function show(page: Page, options: MockOptions, theme: string, size: { width: number; height: number }, query = '') {
  await page.setViewportSize(size)
  await mockModels(page, options); await openModelsSettings(page, query)
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
}
for (const size of sizes) for (const theme of ['light', 'dark']) {
  test(`card at ${size.width} ${theme}`, async ({ page }, testInfo) => {
    // AEON-1688: establish shell behaviour in the actual app, not the headerless harness.
    await page.setViewportSize(size)
    const work = fixtures()
    work.preferences.theme = { choice: theme }
    await mockWork(page, work)
    await mockModels(page, { down: true, fresh: true, longLabels: true, fallback: true })
    await page.goto('/settings/models')
    await expect(page.locator('[data-unavailable]').first()).toBeVisible()
    await page.evaluate(() => document.fonts.ready)
    await page.getByRole('button', { name: 'For everyone', exact: true }).click()
    const header = page.locator('.app-header'), trigger = page.locator('[data-pick="design"]')
    await trigger.scrollIntoViewIfNeeded()
    const before = await header.boundingBox()
    const scroll = () => page.evaluate(() => ({ x: scrollX, y: scrollY, shell: document.querySelector('.app-shell')!.scrollTop }))
    expect(await scroll()).toEqual({ x: 0, y: 0, shell: 0 })
    await page.screenshot({ path: testInfo.outputPath(`AEON-1688/models-${size.width}-${theme}.png`), fullPage: true })
    expect(await header.boundingBox(), 'capture does not move the nav').toEqual(before)
    await expectStableControls({ controls: { nav: header, trigger }, interactions: [
      { name: 'open the model picker', run: async () => { await trigger.click(); await expect(page.locator('.mdl-pop')).toBeVisible() } },
      { name: 'move the active model with the keyboard', run: async () => { await page.getByRole('combobox').press('ArrowDown') } },
    ] })
    expect(await header.boundingBox(), 'picker moves the nav by 0 px').toEqual(before)
    expect(await scroll(), 'picker never scrolls the document or shell').toEqual({ x: 0, y: 0, shell: 0 })
    await page.screenshot({ path: testInfo.outputPath(`AEON-1688/models-${size.width}-${theme}-picker.png`) })
  })
  test(`for everyone with the lock at ${size.width} ${theme}`, async ({ page }, testInfo) => {
    await show(page, {}, theme, size)
    await page.getByRole('button', { name: 'For everyone' }).click()
    await expect(page.locator('[data-row="design"] [data-lock]')).toBeVisible()
    await page.locator('[data-pick="design"]').click()
    await expect(page.locator('.mdl-pop .pm-lock')).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`models-${size.width}-${theme}-everyone-lock.png`) })
  })
  test(`many models at ${size.width} ${theme}`, async ({ page }, testInfo) => {
    await show(page, { many: true }, theme, size)
    await page.locator('[data-pick="all"]').click()
    await page.locator('.mdl-pop [data-older="anthropic"]').click()
    await expect(page.locator('.mdl-pop .opt.is-old')).toHaveCount(3)
    // AEON-1240: each older version shows its own levels, like the current rows.
    await expect(page.locator('.mdl-pop .opt.is-old [data-old-levels]')).toHaveCount(3)
    await page.screenshot({ path: testInfo.outputPath(`models-${size.width}-${theme}-many-picker.png`) })
  })
  test(`why at ${size.width} ${theme}`, async ({ page }, testInfo) => {
    await show(page, { down: true }, theme, size)
    await page.locator('[data-why]').click()
    await expect(page.getByRole('dialog', { name: /^Why /i })).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`models-${size.width}-${theme}-why.png`) })
  })
}
